package agent

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/docker/go-connections/nat"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// Container/volume labels and naming. Labels let the Agent rediscover the servers
// it manages after a restart.
const (
	labelManaged  = "kraken.managed"
	labelServerID = "kraken.server_id"
)

func containerName(serverID string) string { return "kraken_" + serverID }

// DockerRuntime implements Runtime over the Docker Engine API. It runs each
// server in its own container with a persistent data volume, and a one-shot
// container for the install/update phase.
type DockerRuntime struct {
	cli *client.Client
	// images is the same client, narrowed to the image calls, so the pull policy
	// can be exercised against a fake in tests (#288). Never nil.
	images imageAPI
	// containers is the same client again, narrowed to the container calls the
	// removal, install-guard, stop and name-race paths make (containerOps), so
	// they can be exercised against a fake (#354, #351). Never nil.
	containers containerOps
	// pullPolicy is how hard this node tries the registry before using a local
	// copy of an image (KRAKEN_IMAGE_PULL).
	pullPolicy imagePullPolicy
	// pullMu guards pulls, the image references currently being downloaded in
	// the background by the start path, keyed so a second start joins the pull
	// already running instead of starting a competing one.
	pullMu sync.Mutex
	pulls  map[string]*inflightPull
	// imagePrune enables the weekly dangling-image prune (KRAKEN_IMAGE_PRUNE),
	// and pruneClock persists when it last ran (#289).
	imagePrune  bool
	pruneClock  *pruneClock
	nodeID      string
	wineEnabled bool
	version     string
	dataDir     string // the Agent's own root for per-server data dirs (native file ops, backups, SFTP)
	hostDataDir string // the same root as the Docker daemon sees it — bind-mount sources only
	backupDir   string // node-local directory for backup archives (env default)
	specDir     string // persisted runtime specs, so a restarted Agent knows what it manages
	// winIsolation is the isolation mode for Windows containers (Hyper-V by
	// default; see windowsIsolation). Unused on Linux daemons.
	winIsolation container.Isolation

	// hmu guards the container-runtime health snapshot, refreshed on every
	// NodeInfo poll (see checkHealth). osType lives here because the daemon's
	// container mode is only knowable while the daemon is reachable: it starts as
	// the operator-configured value and is corrected on first contact.
	hmu        sync.Mutex
	osType     string // daemon OS: "linux" or "windows" (one daemon per agent)
	runtimeOK  bool
	runtimeErr string

	// bmu guards the backup targets, which the Panel can hot-swap at runtime via
	// ApplyNodeConfig. backups is the primary store; replicate, when non-nil, is
	// the off-node remote every new backup is also mirrored to.
	bmu       sync.RWMutex
	backups   backupTarget        // primary store (local fs, mounted share, SFTP or SMB)
	replicate backupTarget        // optional off-node mirror (nil when replication is off)
	nodeCfg   *agentpb.NodeConfig // last-applied config; source for per-server path templating (nil = defaults)
	sftpPort  int32               // port the Agent's SFTP server bound (0 = SFTP off); reported in NodeInfo

	mu    sync.Mutex
	specs map[string]*agentpb.ServerSpec // serverID → runtime spec

	monMu    sync.Mutex
	monitors map[string]*monitor // serverID → crash watchdog

	// installs is the set of servers with an install pass running (see
	// installGate). While a server is in it, nothing may start its game
	// container: not a Power START/RESTART, not the crash watchdog.
	installs installGate
	// installCleanup tracks the background removals of finished install
	// containers (removeInstallContainerLater), so a test can wait for them.
	installCleanup sync.WaitGroup

	// bjMu guards backupJobs: the live state of asynchronous backups, keyed by
	// "<serverID>/<id>". Holds in-flight (PENDING) jobs and recently-finished ones
	// so ListBackups can report archiving + replication state the on-disk listing
	// can't. Cleared on restart (on-disk archives then list as READY).
	bjMu       sync.Mutex
	backupJobs map[string]*agentpb.BackupInfo
	// failures persists FAILED records across restarts (#221); nil in tests
	// that assemble a DockerRuntime by hand — every use is nil-guarded.
	failures *failureLog
	// removeAll deletes a server's data dir; nil means os.RemoveAll. A seam
	// only, so a deletion the filesystem refuses is testable on any OS (#354).
	removeAll func(string) error

	// pcMu guards playerSamples: the last online-player count per server, TTL-cached
	// so StreamStats (live) and Status (reconcile poll) share one query rather than
	// each hitting the game server.
	pcMu          sync.Mutex
	playerSamples map[string]playerSample
	// rosters holds the "log" player-query roster per server (see roster.go),
	// armed and forgotten with the server's watchdog. Also under pcMu.
	rosters map[string]*logRoster
}

// playerSample is a cached online-player reading for one server.
type playerSample struct {
	players, maxPlayers int32
	known               bool
	at                  time.Time
}

// NewDockerRuntime returns a runtime backed by the local Docker daemon
// (honoring DOCKER_HOST). An unreachable daemon is *not* an error: the Agent
// still answers RPCs, reports its identity, and serves file operations, but
// reports its container runtime as unavailable so the Panel can show the node as
// partial rather than pretending it is either healthy or gone. The daemon is
// re-probed on every NodeInfo poll, so one that comes up later is picked up
// without restarting the Agent. Only a client that cannot be constructed at all
// (a malformed DOCKER_HOST) is fatal.
//
// nodeOS is the operator's declared container mode, used until the daemon can be
// asked directly.
func NewDockerRuntime(ctx context.Context, nodeID, nodeOS string, wineEnabled bool, version string) (*DockerRuntime, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("docker: new client: %w", err)
	}
	osType := nodeOS
	if osType != "windows" {
		osType = "linux"
	}
	backupDir := os.Getenv("KRAKEN_BACKUP_DIR")
	if backupDir == "" {
		backupDir = "backups"
	}
	// Absolute, like the data dir below: it is where archives land, and it is a
	// root the error scrubber rewrites — a relative "backups" would match the
	// word wherever a message happened to contain it.
	if abs, aerr := filepath.Abs(backupDir); aerr == nil {
		backupDir = abs
	}
	// Server data lives in a host directory bind-mounted into each container, so
	// the Agent has direct (native) filesystem access for the file browser and
	// backups — no Docker archive API or helper containers, and it works the same
	// on Linux and Windows.
	dataDir := os.Getenv("KRAKEN_DATA_DIR")
	if dataDir == "" {
		dataDir = "server-data"
	}
	if abs, aerr := filepath.Abs(dataDir); aerr == nil {
		dataDir = abs
	}
	hostDataDir := resolveHostDataDir(dataDir)
	// "agent-specs", not "specs": the state dir defaults to the working directory,
	// and in a source checkout that is the repo root — where specs/ is the tracked
	// Game Spec library. Runtime state must not land in it.
	stateDir := os.Getenv("KRAKEN_STATE_DIR")
	if stateDir == "" {
		stateDir = "."
	}
	d := &DockerRuntime{cli: cli, images: cli, containers: cli, pullPolicy: parsePullPolicy(os.Getenv("KRAKEN_IMAGE_PULL")), nodeID: nodeID, wineEnabled: wineEnabled, version: version, osType: osType, dataDir: dataDir, hostDataDir: hostDataDir, backupDir: backupDir, specDir: filepath.Join(stateDir, "agent-specs"), winIsolation: windowsIsolation(), specs: map[string]*agentpb.ServerSpec{}, monitors: map[string]*monitor{}, backupJobs: map[string]*agentpb.BackupInfo{}, failures: newFailureLog(stateDir), imagePrune: !strings.EqualFold(os.Getenv("KRAKEN_IMAGE_PRUNE"), "off"), pruneClock: newPruneClock(stateDir)}
	d.backups = selectBackupTarget(backupDir)
	// Failed backups outlive the process: without this an agent restart erased
	// every FAILED row from ListBackups and the operator saw a backup that
	// simply never appeared (#221).
	d.failures.loadInto(d.backupJobs)
	// Rehydrate what this node was managing before the restart, then re-arm the
	// watchdogs for whatever is still running. Order matters: adoption reads the
	// spec map for each server's restart policy and ready regex.
	d.loadSpecs()
	d.checkHealth(ctx)
	return d, nil
}

// checkHealth probes the Docker daemon and refreshes the cached health snapshot,
// returning it. Called at startup and on every NodeInfo poll, so a daemon that
// dies (or comes back) is reflected within one Panel poll instead of requiring an
// Agent restart.
//
// The first successful probe of a session also corrects the container mode from
// the daemon itself and re-adopts crash watchdogs — a daemon coming back up is
// indistinguishable, from the Agent's point of view, from an Agent restart.
func (d *DockerRuntime) checkHealth(ctx context.Context) (bool, string) {
	_, perr := d.cli.Ping(ctx)

	var mode string
	if perr == nil {
		if info, ierr := d.cli.Info(ctx); ierr == nil && info.OSType != "" {
			mode = info.OSType
		}
	}

	d.hmu.Lock()
	was := d.runtimeOK
	d.runtimeOK = perr == nil
	if perr != nil {
		d.runtimeErr = perr.Error()
	} else {
		d.runtimeErr = ""
		if mode != "" && mode != d.osType {
			slog.Info("docker daemon container mode differs from the configured node OS; using the daemon's",
				"configured", d.osType, "daemon", mode)
			d.osType = mode
		}
	}
	ok, rerr, recovered := d.runtimeOK, d.runtimeErr, !was && perr == nil
	d.hmu.Unlock()

	switch {
	case recovered:
		slog.Info("docker daemon reachable", "mode", d.OSType())
		d.adoptRunning(ctx)
	case was && perr != nil:
		slog.Error("docker daemon became unreachable — this node cannot install, start, or observe servers until it is back", "err", perr)
	}
	return ok, rerr
}

// RuntimeHealth reports whether the Docker daemon was reachable at the last
// probe, and why not when it wasn't.
func (d *DockerRuntime) RuntimeHealth() (bool, string) {
	d.hmu.Lock()
	defer d.hmu.Unlock()
	return d.runtimeOK, d.runtimeErr
}

// resolveHostDataDir returns the data root as the *Docker daemon* sees it.
//
// Bind sources in ContainerCreate are resolved by the daemon against the host
// filesystem, never against the Agent's mount namespace. A bare-metal Agent
// shares that view, so the two are identical. A containerized Agent does not:
// with `-v /srv/kraken/data:/data` and KRAKEN_DATA_DIR=/data it would ask the
// daemon to bind the host's /data — a different (usually empty, daemon-created)
// directory than the one its own file ops, backups, and SFTP jail see. Game
// servers then read and write somewhere the Agent can't see, silently.
//
// KRAKEN_HOST_DATA_DIR names the host path explicitly. Leaving it unset is
// correct whenever the Agent's view matches the host's — bare metal, or a
// container that mounts the data root at the same absolute path it uses
// internally (the arrangement deploy/ ships, and the one to prefer: there is no
// second value to keep in sync).
func resolveHostDataDir(dataDir string) string {
	v := strings.TrimSpace(os.Getenv("KRAKEN_HOST_DATA_DIR"))
	if v == "" {
		if inContainer() {
			// Not an error — same-path mounts are the recommended setup and need
			// no override — but it is the one misconfiguration that fails silently,
			// so say what will be handed to the daemon.
			slog.Warn("containerized Agent with no KRAKEN_HOST_DATA_DIR — game-container bind mounts will use this path as the daemon sees it; "+
				"mount the data root at the same absolute path inside this container, or set KRAKEN_HOST_DATA_DIR to the host path",
				"data_dir", dataDir)
		}
		return dataDir
	}
	if abs, err := filepath.Abs(v); err == nil {
		v = abs
	}
	if v != dataDir {
		slog.Info("data root differs between Agent and Docker daemon", "agent_view", dataDir, "daemon_view", v)
	}
	return v
}

// inContainer reports whether this process looks containerized. Only used to
// decide whether to warn about an unset KRAKEN_HOST_DATA_DIR, so a false
// negative (Windows containers have no /.dockerenv) costs a hint, not
// correctness.
func inContainer() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	if b, err := os.ReadFile("/proc/1/cgroup"); err == nil {
		s := string(b)
		return strings.Contains(s, "/docker/") || strings.Contains(s, "/containerd/")
	}
	return false
}

// windowsIsolation resolves the isolation mode for Windows containers. It
// defaults to Hyper-V: process isolation requires the host and the container's
// base image to share the same Windows build, so on a newer host running an
// older base (e.g. an ltsc2022 image on Windows 11) the daemon's default
// (process) isolation fails CreateComputeSystem with "The request is not
// supported". Hyper-V isolation works across build mismatches. Operators on a
// matching Windows Server host can set KRAKEN_WINDOWS_ISOLATION=process (or
// "default" to defer to the daemon).
func windowsIsolation() container.Isolation {
	switch strings.ToLower(os.Getenv("KRAKEN_WINDOWS_ISOLATION")) {
	case "process":
		return container.IsolationProcess
	case "default":
		return container.IsolationDefault
	default:
		return container.IsolationHyperV
	}
}

// applyIsolation sets the Windows isolation mode on a container's HostConfig.
// No-op on Linux daemons, where Isolation is meaningless.
func (d *DockerRuntime) applyIsolation(host *container.HostConfig) {
	if d.isWindows() {
		host.Isolation = d.winIsolation
	}
}

// selectBackupTarget returns the default node-local filesystem target rooted at
// backupDir. The Panel can later hot-swap this for a remote (share/SFTP/SMB)
// via ApplyNodeConfig.
func selectBackupTarget(backupDir string) backupTarget {
	return &localBackupTarget{dir: backupDir}
}

// replicateTarget returns the current off-node mirror (nil when replication is off).
func (d *DockerRuntime) replicateTarget() backupTarget {
	d.bmu.RLock()
	defer d.bmu.RUnlock()
	return d.replicate
}

// expandCfgPaths returns a NodeConfig copy with backup_dir, sftp_base_path and
// smb_base_path expanded for slug. Fields are copied by name (not *cfg) so the
// proto's internal lock isn't copied (go vet copylocks) — every field a target
// builder reads must be listed here, or it is silently lost for per-server
// operations.
func expandCfgPaths(cfg *agentpb.NodeConfig, slug string) *agentpb.NodeConfig {
	return &agentpb.NodeConfig{
		BackupTarget:     cfg.GetBackupTarget(),
		BackupDir:        expandBackupPath(cfg.GetBackupDir(), slug),
		SftpHost:         cfg.GetSftpHost(),
		SftpUser:         cfg.GetSftpUser(),
		SftpPassword:     cfg.GetSftpPassword(),
		SftpPrivateKey:   cfg.GetSftpPrivateKey(),
		SftpBasePath:     expandBackupPath(cfg.GetSftpBasePath(), slug),
		SftpKnownHostKey: cfg.GetSftpKnownHostKey(),
		ReplicateToSftp:  cfg.GetReplicateToSftp(),
		SmbHost:          cfg.GetSmbHost(),
		SmbShare:         cfg.GetSmbShare(),
		SmbUser:          cfg.GetSmbUser(),
		SmbPassword:      cfg.GetSmbPassword(),
		SmbDomain:        cfg.GetSmbDomain(),
		SmbBasePath:      expandBackupPath(cfg.GetSmbBasePath(), slug),
		ReplicateToSmb:   cfg.GetReplicateToSmb(),
	}
}

// backupTargetFor returns the primary backup target with path tokens (e.g.
// {{SLUG}}) expanded for the given server slug. With no applied config it falls
// back to the node-local default target.
func (d *DockerRuntime) backupTargetFor(slug string) backupTarget {
	d.bmu.RLock()
	cfg := d.nodeCfg
	base := d.backups
	d.bmu.RUnlock()
	if cfg == nil {
		return base
	}
	return d.buildTarget(expandCfgPaths(cfg, slug))
}

// replicateTargetFor returns the off-node mirror with path tokens expanded for
// slug (nil when replication is off).
func (d *DockerRuntime) replicateTargetFor(slug string) backupTarget {
	d.bmu.RLock()
	cfg := d.nodeCfg
	d.bmu.RUnlock()
	if cfg == nil {
		return d.replicateTarget()
	}
	mirror := buildReplicateTarget(expandCfgPaths(cfg, slug))
	if mirror == nil {
		return d.replicateTarget()
	}
	return mirror
}

// buildReplicateTarget constructs the mirror destination described by cfg, or
// nil when no replication is enabled. SFTP wins if both flags are set — the
// Panel rejects that combination, so it only happens with a hand-edited config,
// and one mirror is always better than an arbitrary one.
func buildReplicateTarget(cfg *agentpb.NodeConfig) backupTarget {
	switch {
	case cfg.GetReplicateToSftp():
		return buildSFTPTarget(cfg)
	case cfg.GetReplicateToSmb():
		return buildSMBTarget(cfg)
	default:
		return nil
	}
}

// buildTarget constructs the primary backup target described by cfg. An empty
// or "local" target (and an empty backup_dir) falls back to the env-derived
// default so the Panel can leave fields blank.
func (d *DockerRuntime) buildTarget(cfg *agentpb.NodeConfig) backupTarget {
	switch cfg.GetBackupTarget() {
	case "sftp":
		return buildSFTPTarget(cfg)
	case "smb":
		// An SMB server dialed with explicit credentials — no host mount, so it
		// works from a service account on either OS.
		return buildSMBTarget(cfg)
	case "share":
		// A mounted network share (SMB/NFS). The dir must point at the mount; it
		// is not defaulted (an empty/missing path must fail verify, not silently
		// write node-local). It's an explicit path, so archives go directly in it.
		return &shareBackupTarget{localBackupTarget{dir: cfg.GetBackupDir(), flat: true}}
	default: // "local" or ""
		dir := cfg.GetBackupDir()
		if dir == "" {
			// Zero-config default: namespace per server so multiple servers on the
			// node don't share one directory.
			return &localBackupTarget{dir: d.backupDir}
		}
		// An explicitly configured path is the exact destination — archives live
		// directly in it (use {{SLUG}} to separate games). No per-server subdir.
		return &localBackupTarget{dir: dir, flat: true}
	}
}

// buildSFTPTarget constructs an SFTP target from cfg's sftp_* fields.
func buildSFTPTarget(cfg *agentpb.NodeConfig) *sftpBackupTarget {
	return &sftpBackupTarget{cfg: sftpConfig{
		Host:         cfg.GetSftpHost(),
		User:         cfg.GetSftpUser(),
		Password:     cfg.GetSftpPassword(),
		PrivateKey:   cfg.GetSftpPrivateKey(),
		BasePath:     cfg.GetSftpBasePath(),
		KnownHostKey: cfg.GetSftpKnownHostKey(),
	}}
}

// buildSMBTarget constructs an SMB target from cfg's smb_* fields.
func buildSMBTarget(cfg *agentpb.NodeConfig) *smbBackupTarget {
	return &smbBackupTarget{cfg: smbConfig{
		Host:     cfg.GetSmbHost(),
		Share:    cfg.GetSmbShare(),
		User:     cfg.GetSmbUser(),
		Password: cfg.GetSmbPassword(),
		Domain:   cfg.GetSmbDomain(),
		BasePath: cfg.GetSmbBasePath(),
	}}
}

// verifiableTarget is a backup target that can be probed for reachability at
// config-apply time (a remote endpoint, or a mount that must already exist).
// The node-local target has nothing to prove, so it doesn't implement it.
type verifiableTarget interface {
	verify() error
}

// verifyTarget probes t when it supports verification; anything else (nil, or
// the node-local target) verifies trivially.
func verifyTarget(t backupTarget) error {
	if vt, ok := t.(verifiableTarget); ok {
		return vt.verify()
	}
	return nil
}

// ApplyNodeConfig hot-swaps the backup target(s) from Panel-managed config and,
// when verify is set (the operator-save path), reports whether the configured
// target(s) are reachable and writable. The swap takes effect even when
// verification fails, so the operator's intent persists.
func (d *DockerRuntime) ApplyNodeConfig(_ context.Context, cfg *agentpb.NodeConfig, verify bool) (bool, string) {
	if cfg == nil {
		return true, "no config"
	}
	primary := d.buildTarget(cfg)
	replicate := buildReplicateTarget(cfg)

	d.bmu.Lock()
	d.backups = primary
	d.replicate = replicate
	d.nodeCfg = cfg // retained so per-server backup ops can expand path tokens (e.g. {{SLUG}})
	d.bmu.Unlock()

	ok := true
	var msgs []string
	if verify {
		if err := verifyTarget(primary); err != nil {
			ok = false
			msgs = append(msgs, err.Error())
		}
		if err := verifyTarget(replicate); err != nil {
			ok = false
			msgs = append(msgs, "replication: "+err.Error())
		}
	}
	detail := fmt.Sprintf("primary=%s replication=%t", primary.Kind(), replicate != nil)
	if replicate != nil && cfg.GetReplicateToSftp() && cfg.GetReplicateToSmb() {
		// The Panel refuses this combination; a hand-edited config still reaches
		// here, so say which mirror won rather than silently picking one.
		detail += " (both sftp and smb mirrors set — using " + replicate.Kind() + ")"
	}
	if len(msgs) > 0 {
		detail = strings.Join(msgs, "; ")
	}
	slog.Info("node config applied", "primary", primary.Kind(), "replicate", replicate != nil, "ok", ok)
	return ok, detail
}

// ReplicateBackups mirrors a server's archives from the primary target to the
// configured SFTP remote, skipping archives already present on the remote.
func (d *DockerRuntime) ReplicateBackups(ctx context.Context, serverID, slug string) (int32, int32, error) {
	dst := d.replicateTargetFor(slug)
	if dst == nil {
		return 0, 0, fmt.Errorf("replication is not configured for this node")
	}
	src := d.backupTargetFor(slug)
	srcList, err := src.List(ctx, serverID)
	if err != nil {
		return 0, 0, fmt.Errorf("list source backups: %w", err)
	}
	dstList, _ := dst.List(ctx, serverID)
	present := make(map[string]bool, len(dstList))
	for _, b := range dstList {
		present[b.Id] = true
	}
	var mirrored, skipped int32
	for _, b := range srcList {
		if present[b.Id] {
			skipped++
			continue
		}
		r, oerr := src.Open(ctx, serverID, b.Id)
		if oerr != nil {
			return mirrored, skipped, fmt.Errorf("open %s: %w", b.Id, oerr)
		}
		perr := dst.Put(ctx, serverID, b.Id, r, b.Size)
		_ = r.Close()
		if perr != nil {
			return mirrored, skipped, fmt.Errorf("mirror %s: %w", b.Id, perr)
		}
		mirrored++
	}
	return mirrored, skipped, nil
}

var _ Runtime = (*DockerRuntime)(nil)

// Close releases the Docker client.
func (d *DockerRuntime) Close() error { return d.cli.Close() }

// OSType reports the daemon's container OS ("linux" or "windows"). Until the
// daemon has been reached once this is the operator-configured node OS.
func (d *DockerRuntime) OSType() string {
	d.hmu.Lock()
	defer d.hmu.Unlock()
	return d.osType
}

func (d *DockerRuntime) putSpec(spec *agentpb.ServerSpec) {
	d.mu.Lock()
	d.specs[spec.ServerId] = spec
	d.mu.Unlock()
	d.persistSpec(spec)
}

func (d *DockerRuntime) getSpec(serverID string) (*agentpb.ServerSpec, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	s, ok := d.specs[serverID]
	return s, ok
}

// specFile is the on-disk location of one server's persisted runtime spec.
func (d *DockerRuntime) specFile(serverID string) string {
	return filepath.Join(d.specDir, serverID+".json")
}

// persistSpec writes a server's runtime spec alongside the Agent's own state.
//
// The Panel is the source of truth and re-pushes specs on power actions, but a
// restarted Agent needs them *before* the next push: to re-arm crash watchdogs
// for servers that never stopped, and to recreate a container on start without
// waiting for the Panel to notice. Best-effort — a write failure costs recovery
// after a restart, not correctness now.
//
// Mode 0600: a spec carries the server's environment, which can include game
// admin and RCON passwords. That is not a new exposure (rendered config files
// with the same secrets already land in the server's data dir) but it is no
// reason to widen it.
func (d *DockerRuntime) persistSpec(spec *agentpb.ServerSpec) {
	if err := os.MkdirAll(d.specDir, 0o700); err != nil {
		slog.Warn("could not create spec dir; this server will not be re-adopted after an Agent restart", "dir", d.specDir, "err", err)
		return
	}
	b, err := protojson.Marshal(spec)
	if err != nil {
		slog.Warn("could not encode spec for persistence", "server", spec.GetServerId(), "err", err)
		return
	}
	if err := os.WriteFile(d.specFile(spec.GetServerId()), b, 0o600); err != nil {
		slog.Warn("could not persist spec; this server will not be re-adopted after an Agent restart", "server", spec.GetServerId(), "err", err)
	}
}

// forgetSpec drops a removed server's persisted spec.
func (d *DockerRuntime) forgetSpec(serverID string) {
	if err := os.Remove(d.specFile(serverID)); err != nil && !os.IsNotExist(err) {
		slog.Warn("could not remove persisted spec", "server", serverID, "err", err)
	}
}

// loadSpecs rehydrates the spec map from disk at startup. Best-effort per file:
// an unreadable or corrupt spec is logged and skipped rather than taking the
// whole Agent down, since the Panel will re-push it on the next power action.
func (d *DockerRuntime) loadSpecs() {
	entries, err := os.ReadDir(d.specDir)
	if err != nil {
		return // no persisted specs (fresh node, or a pre-persistence Agent)
	}
	loaded := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, rerr := os.ReadFile(filepath.Join(d.specDir, e.Name()))
		if rerr != nil {
			slog.Warn("could not read persisted spec", "file", e.Name(), "err", rerr)
			continue
		}
		var spec agentpb.ServerSpec
		if uerr := protojson.Unmarshal(b, &spec); uerr != nil {
			slog.Warn("could not decode persisted spec", "file", e.Name(), "err", uerr)
			continue
		}
		if spec.GetServerId() == "" {
			continue
		}
		d.mu.Lock()
		d.specs[spec.GetServerId()] = &spec
		d.mu.Unlock()
		loaded++
	}
	if loaded > 0 {
		slog.Info("restored persisted server specs", "count", loaded, "dir", d.specDir)
	}
}

// NodeInfo reports the node's identity, capacity, and container-runtime health.
// It does not fail when Docker is unreachable: an Agent that can be dialed is
// materially different from one that is down, and collapsing the two into a
// transport error is what made an unreachable daemon look like an offline node.
// Capacity fields are simply absent in that case (the Panel only backfills
// values it doesn't already have).
func (d *DockerRuntime) NodeInfo(ctx context.Context) (*agentpb.NodeInfo, error) {
	ok, rerr := d.checkHealth(ctx)
	d.bmu.RLock()
	sftpPort := d.sftpPort
	d.bmu.RUnlock()
	out := &agentpb.NodeInfo{
		NodeId:        d.nodeID,
		Os:            d.OSType(),
		WineEnabled:   d.wineEnabled,
		AgentVersion:  d.version,
		Host:          PrimaryIP(),
		HostAddresses: CandidateIPs(),
		ExternalIp:    ExternalIP(ctx),
		SftpPort:      sftpPort,
		RuntimeStatus: agentpb.RuntimeStatus_RUNTIME_STATUS_OK,
	}
	if !ok {
		out.RuntimeStatus = agentpb.RuntimeStatus_RUNTIME_STATUS_UNAVAILABLE
		out.RuntimeError = rerr
		return out, nil
	}
	if info, err := d.cli.Info(ctx); err == nil {
		out.TotalMemoryMb = info.MemTotal / (1024 * 1024)
	}
	managed, err := d.cli.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("label", labelManaged+"=true")),
	})
	if err != nil {
		// No list to report, and saying "reported" over an empty one would tell
		// the Panel this node has no containers at all. Leave the marker unset:
		// the Panel then falls back to the count, which is zero here as it always
		// was when the listing failed.
		return out, nil
	}
	// Every managed container, stopped ones included, each with Docker's own state
	// word — and the count is the running subset of that same listing, so the two
	// can never disagree: the count is what an older Panel reads (it always meant
	// running), the list is what lets a current one name the container it has no
	// row for, and tell an offline server whose container is merely stopped from
	// one that has no container until it starts. The server id comes from the
	// label the runtime writes at create time rather than from the name, so a
	// container renamed by hand still identifies itself. Docker reports the same
	// state words for Windows containers, so nothing here is OS-specific.
	out.ContainersReported = true
	for _, c := range managed {
		state := strings.ToLower(string(c.State))
		if state == "running" {
			out.RunningServers++
		}
		out.ManagedContainers = append(out.ManagedContainers, &agentpb.ManagedContainer{
			ServerId:      c.Labels[labelServerID],
			ContainerName: containerDisplayName(c.Names),
			State:         state,
		})
	}
	return out, nil
}

// containerDisplayName is the name Docker would print for a container: the first
// of its names with the leading slash stripped. Empty when Docker reported none.
func containerDisplayName(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return strings.TrimPrefix(names[0], "/")
}

func (d *DockerRuntime) Create(ctx context.Context, spec *agentpb.ServerSpec) error {
	if spec.GetServerId() == "" {
		return fmt.Errorf("docker: spec.server_id is required")
	}
	// Ensure the host data directory exists (idempotent); it is bind-mounted into
	// the container as the data volume.
	if err := os.MkdirAll(d.localDir(spec.ServerId), 0o755); err != nil {
		return fmt.Errorf("docker: create data dir: %w", err)
	}
	d.putSpec(spec)
	return nil
}

// Remove retires a server from this node: its watchdog, both of its containers,
// its runtime spec and its tracked backup jobs, and — only when the operator
// asked for it — its data directory. Backup archives are never touched here.
//
// It reports what it could not do. It used to discard every error and return
// nil, which let the Panel believe a removal had landed when the container was
// still running (#354): the Panel deleted its row, the watchdog re-adopted the
// container on the next Agent restart, and nothing owned it again. The Panel now
// keeps a removal that failed and retries it, so an honest error is what gets
// the job finished.
//
// A container that is already gone is success — that is what makes a retry safe.
// A container that will not go stops the removal before the data and the spec
// are touched: the server is still there, and a retry must find it whole.
func (d *DockerRuntime) Remove(ctx context.Context, serverID string, deleteData bool) error {
	// The id becomes a path under the data root and the spec dir. An empty one
	// is the data root itself, so it is checked before anything is touched —
	// the caller is a Panel, and not necessarily this version of it.
	if err := validRemoveID(serverID); err != nil {
		return err
	}
	d.stopMonitor(serverID)
	// The install container too: an install interrupted by an Agent restart or a
	// daemon hiccup can leave kraken_<id>_install behind, bind-mounted onto the
	// data dir the operator is deleting.
	for _, name := range []string{containerName(serverID), installContainerName(serverID)} {
		if err := d.clearContainerName(ctx, name); err != nil {
			return fmt.Errorf("docker: remove server %s: %w", serverID, err)
		}
	}
	var dataErr error
	if deleteData {
		removeAll := d.removeAll
		if removeAll == nil {
			removeAll = os.RemoveAll
		}
		if err := removeAll(d.localDir(serverID)); err != nil {
			dataErr = fmt.Errorf("docker: remove server %s: delete its data: %w", serverID, dataRemoveError(d.localDir(serverID), err))
		}
	}
	// The containers are gone, so the server is gone as far as this node is
	// concerned, even if some of its bytes stayed behind. Forgetting it now is
	// what keeps a restarted Agent from treating a half-deleted tree as a server.
	d.mu.Lock()
	delete(d.specs, serverID)
	d.mu.Unlock()
	d.forgetSpec(serverID)
	d.forgetServerBackupJobs(serverID)
	return dataErr
}

// PurgeBackups deletes a removed server's backup archives on the primary target
// and the mirror, where they are its own (see localBackupTarget.purgeServer),
// and names the locations it kept. The permanent delete of a retired server
// (#360) is the only caller; a plain Remove never touches an archive.
//
// Targets are resolved without a slug. That is exact for the only layout that
// is ever purged — the zero-config default has no path tokens — and a
// templated path is flat, so it is kept whatever it expands to.
func (d *DockerRuntime) PurgeBackups(_ context.Context, serverID string) (string, error) {
	if err := validRemoveID(serverID); err != nil {
		return "", err
	}
	var kept []string
	purge := func(t backupTarget, mirror bool) error {
		if t == nil {
			return nil
		}
		if p, ok := t.(serverPurger); ok {
			done, err := p.purgeServer(serverID)
			if err != nil {
				return err
			}
			if done {
				return nil
			}
		}
		kept = append(kept, keptLabel(t, mirror))
		return nil
	}
	if err := purge(d.backupTargetFor(""), false); err != nil {
		return "", fmt.Errorf("docker: purge backups of %s: %w", serverID, err)
	}
	if err := purge(d.replicateTargetFor(""), true); err != nil {
		return "", fmt.Errorf("docker: purge mirrored backups of %s: %w", serverID, err)
	}
	d.forgetServerBackupJobs(serverID)
	return strings.Join(kept, ", "), nil
}

// validRemoveID refuses a server id that could not name a single directory
// under the data root: empty, containing a path separator of either OS, or a
// dot-dot. InvalidArgument, because it is the request that is wrong.
func validRemoveID(serverID string) error {
	if serverID == "" || serverID == "." || strings.Contains(serverID, "..") ||
		strings.ContainsAny(serverID, `/\:`) {
		return grpcstatus.Errorf(codes.InvalidArgument, "remove: invalid server id %q", serverID)
	}
	return nil
}

// installContainerName is the one-shot install container's name for a server.
func installContainerName(serverID string) string { return containerName(serverID) + "_install" }

// dataRemoveError renders a failed data-dir deletion against the server's
// logical /data path, never the host one: the message reaches the Panel, its
// log and its node view, and where this node keeps its storage is not the
// operator's business through that channel (see statError). The cause — the
// errno an *os.PathError wraps — is kept, because "permission denied" and
// "the process cannot access the file" are different problems.
func dataRemoveError(root string, err error) error {
	pe, ok := err.(*os.PathError)
	if !ok || pe.Err == nil {
		return err
	}
	logical := "/data"
	if rel, rerr := filepath.Rel(root, pe.Path); rerr == nil && rel != "." && !strings.HasPrefix(rel, "..") {
		logical = path.Join(logical, filepath.ToSlash(rel))
	}
	return fmt.Errorf("%s %s: %w", pe.Op, logical, pe.Err)
}

func (d *DockerRuntime) Install(ctx context.Context, req *agentpb.InstallServerRequest, emit func(*agentpb.InstallEvent) error) error {
	// Nothing may start the game on this data dir while the pass runs (see
	// installgate.go). The watchdog goes first: the Panel has already stopped
	// the server, and the monitor is re-armed by the next start.
	//
	// The gate is released the moment the verdict (Completed or Failed) is
	// sent, not when Install returns: the Panel acts on the verdict at once —
	// the START after an update, the deploy form's start-after-install. The
	// exited install container is removed in the background after that (see
	// below); it holds nothing, and the next pass's guard clears it if it
	// lingers.
	leave := d.installs.enter(req.ServerId)
	defer leave()
	emit = releaseOnVerdict(emit, leave)
	d.stopMonitor(req.ServerId)

	dataPath := d.containerDataTarget(req.ServerId)
	// Ensure the host data dir exists even if Create was not called.
	if err := os.MkdirAll(d.localDir(req.ServerId), 0o755); err != nil {
		return d.fail(emit, "create data dir: "+err.Error())
	}

	if err := d.pullImage(ctx, req.Image, installPullTimeout, func(line string) { _ = emit(logLine(line)) }); err != nil {
		return d.fail(emit, "pull image: "+err.Error())
	}

	// Windows containers only: steamcmd.exe self-updates by spawning the new
	// binary and exiting, which lets cmd (PID 1) run off the end of the script
	// and kill the container mid-download. Prime the update and wait for every
	// steamcmd to be gone before the chain ends. See steamguard.go.
	script := req.InstallScript
	if d.isWindows() {
		if guarded, applied := guardWindowsSteamInstall(script); applied {
			script = guarded
			_ = emit(logLine("[kraken] windows SteamCMD guard applied: priming steamcmd.exe's self-update and waiting for steamcmd to exit before the install container ends"))
		}
	}

	// One-shot install container: run the install script against the data dir.
	cfg := &container.Config{
		Image:      req.Image,
		Entrypoint: d.shellEntrypoint(),
		Cmd:        []string{script},
		Env:        envSlice(req.Env),
		WorkingDir: dataPath,
		Labels:     map[string]string{labelManaged: "true", labelServerID: req.ServerId},
	}
	host := &container.HostConfig{
		// Use Binds (the "-v source:target" form), not the structured Mounts API:
		// Hyper-V-isolated Windows containers reject Mounts-API bind mounts with
		// "CreateComputeSystem ... The request is not supported". Binds works under
		// both isolation modes and on Linux.
		Binds: []string{d.bindSource(req.ServerId) + ":" + dataPath},
	}
	if req.MemoryLimitMb > 0 {
		host.Resources.Memory = req.MemoryLimitMb * 1024 * 1024
	}
	d.applyIsolation(host)

	// One pass, or two: a retryable SteamCMD failure that left orphaned staging
	// files behind is cleaned up and run once more if the deadline allows. See
	// steamrecovery.go.
	//
	// The exited install container of the LAST pass is removed only after the
	// verdict is sent, off the RPC's path: its removal can wait up to 30s for
	// the name on Windows, and the Panel moves on (applyConfig, START) the
	// moment the stream ends. Between passes it is removed synchronously —
	// the retry reuses the name.
	note := func(line string) { _ = emit(logLine(line)) }
	installName := installContainerName(req.ServerId)
	passes := 0
	pending := "" // the last pass's exited install container, not yet removed
	failure, err := runInstallWithRecovery(ctx, func(ctx context.Context) (string, error) {
		passes++
		if pending != "" {
			d.removeInstallContainer(pending, installName)
			pending = ""
		}
		steamErr, id, err := d.runInstallContainer(ctx, req.ServerId, cfg, host, passes == 1, emit)
		pending = id
		return steamErr, err
	}, hostStagingTree{root: d.localDir(req.ServerId)}, note, time.Now, noRetryReason(req))
	defer d.removeInstallContainerLater(pending, installName)
	if err != nil {
		return err
	}
	if failure != "" {
		// SteamCMD returned 0 but reported an app failure in its output — fail
		// loudly. The Panel prefixes "install failed: " itself.
		return d.fail(emit, failure)
	}
	_ = emit(&agentpb.InstallEvent{Event: &agentpb.InstallEvent_Progress{Progress: 100}})
	return emit(&agentpb.InstallEvent{Event: &agentpb.InstallEvent_Completed{Completed: true}})
}

// removeInstallContainerLater removes a finished install container in the
// background, so the install RPC can end — and the Panel act on its verdict —
// without waiting for the name to come free. "" is a no-op. installCleanup
// lets a test wait for it.
func (d *DockerRuntime) removeInstallContainerLater(id, name string) {
	if id == "" {
		return
	}
	d.installCleanup.Add(1)
	go func() {
		defer d.installCleanup.Done()
		d.removeInstallContainer(id, name)
	}()
}

// runInstallContainer runs one install container to completion: guard the data
// dir, create, start, stream the logs, wait for the exit. It returns the
// SteamCMD failure line the output reported ("" for a clean pass). A failure of
// the pass itself is reported through fail and returned as err; a cancelled
// context is returned as is.
//
// Every pass goes through the guard, the retry included: it reuses the
// `_install` name, and the guard is what waits for the previous container's
// name to come free (#355).
func (d *DockerRuntime) runInstallContainer(ctx context.Context, serverID string, cfg *container.Config, host *container.HostConfig, first bool, emit func(*agentpb.InstallEvent) error) (steamErr, id string, err error) {
	installName := installContainerName(serverID)
	// Never run SteamCMD while anything else holds the data dir (#351). This
	// also clears a previous pass's install container, waiting for its name
	// the way ensureContainer does (#355) — the create below reuses it.
	//
	// On the first pass a guard failure — a refusal, or the old install
	// container's name never freeing — happens before anything touches the
	// tree, and says so, so the Panel does not mark it install_failed. On the
	// retry the first pass has already written to the tree, so it is an
	// ordinary failure.
	if err := clearDataDir(ctx, d.containers, serverID, d.bindSource(serverID), installName, d.foldHostPaths(),
		selfContainerID(), func(line string) { _ = emit(logLine(line)) }); err != nil {
		if first {
			return "", "", d.failUntouched(emit, err.Error())
		}
		return "", "", d.fail(emit, err.Error())
	}
	created, err := d.containers.ContainerCreate(ctx, cfg, host, nil, nil, installName)
	if err != nil {
		return "", "", d.fail(emit, "create install container: "+err.Error())
	}
	// Not removed here: the caller removes it — between passes at once, after
	// the verdict for the last one (see Install).
	id = created.ID

	if err := d.containers.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		return "", id, d.fail(emit, "start install container: "+err.Error())
	}
	_ = emit(&agentpb.InstallEvent{Event: &agentpb.InstallEvent_Progress{Progress: 10}})

	// Stream install logs, watching for a SteamCMD app failure. SteamCMD exits 0
	// even when an app fails to download (e.g. "Missing configuration", "No
	// subscription", "Not for anonymous users", "Error! App '…' state is 0x6
	// after update job."), so the exit code alone would let a broken install pass
	// as success — leaving a silent, empty server, or relaunching a stale build.
	if err := d.streamLogs(ctx, created.ID, "all", func(_ string, text string) error {
		steamErr = steamInstallOutcome(steamErr, text)
		return emit(logLine(text))
	}); err != nil && ctx.Err() == nil {
		return "", id, d.fail(emit, "stream install logs: "+err.Error())
	}

	// Wait for exit and check the code.
	statusCh, errCh := d.containers.ContainerWait(ctx, created.ID, container.WaitConditionNotRunning)
	select {
	case werr := <-errCh:
		if werr != nil {
			return "", id, d.fail(emit, "wait install: "+werr.Error())
		}
	case st := <-statusCh:
		if st.StatusCode != 0 {
			return "", id, d.fail(emit, fmt.Sprintf("install exited with code %d", st.StatusCode))
		}
	case <-ctx.Done():
		return "", id, ctx.Err()
	}
	return steamErr, id, nil
}

// steamInstallFailurePhrases are the SteamCMD output lines that mean an app
// failed to install even though the steamcmd process exits 0. The canonical set
// is LinuxGSM's error list (https://docs.linuxgsm.com/steamcmd/errors); the
// "state is 0x… after update job" family is the one that bit us live on
// 2026-09-15 (Dragonwilds, app 4019830, state 0x6 — no connection to the content
// servers — where both app_update passes exited 0 and the stale build relaunched).
//
// Matching here is per-line and the LAST relevant line wins: a later
// "Success! App … fully installed" clears an earlier match, which is what makes
// it safe to list transient first-pass phrases like "Missing configuration".
var steamInstallFailurePhrases = []string{
	`Failed to install app`, // "ERROR! Failed to install app '740' (No subscription)"
	// "Error! App '4019830' state is 0x6 after update job." — also seen without
	// the app id ("Error! State is 0x402 after update job.") and with SteamCMD's
	// own "state is is 0x2" typo. Never matches the benign "Update state (0x3)
	// reconfiguring, progress: …" progress lines.
	`state is (?:is )?0x[0-9a-f]+ after update job`,
	`Password check for AppId .* returned error`, // wrong/expired beta-branch password
	`No subscription`,          // the Steam account does not own the app
	`Not for anonymous`,        // the app requires a real Steam login
	`Missing configuration`,    // transient on the first two-step pass — cleared by the later success
	`Missing update files`,     // 0x626
	`Corrupt update files`,     // 0x6A6
	`Invalid platform`,         // wrong +@sSteamCmdForcePlatformType for the depot
	`Rate Limit Exceeded`,      // too many requests from this IP
	`Timeout downloading item`, // workshop item download timed out
	`Disk write failure`,       // 0x606 — permissions or a full volume
	`Depot download failed`,    // a depot could not be fetched
}

// steamInstallFailureRE matches the SteamCMD lines that signal an app failed to
// install despite SteamCMD's process exiting 0. steamInstallSuccessRE matches the
// success line, which clears a prior (transient) failure — see the two-step quirk.
var (
	steamInstallFailureRE = regexp.MustCompile(`(?i)(?:` + strings.Join(steamInstallFailurePhrases, `|`) + `)`)
	steamInstallSuccessRE = regexp.MustCompile(`(?i)Success! App .* fully installed`)
)

// steamInstallOutcome folds one SteamCMD log line into the pending install
// failure: a success line clears it, a failure line replaces it, anything else
// leaves it alone. The last relevant line therefore wins — a later success
// supersedes an earlier transient failure (SteamCMD's "Missing configuration"
// two-step fails the first app_update pass and succeeds on the second), and a
// failure on a later pass overrides an earlier pass's success.
func steamInstallOutcome(pending, text string) string {
	switch {
	case steamInstallSuccessRE.MatchString(text):
		return ""
	case steamInstallFailureRE.MatchString(text):
		return strings.TrimSpace(text)
	}
	return pending
}

func (d *DockerRuntime) Power(ctx context.Context, serverID string, action agentpb.PowerAction) (agentpb.ServerState, error) {
	switch action {
	case agentpb.PowerAction_POWER_ACTION_START, agentpb.PowerAction_POWER_ACTION_RESTART:
		// Never start the game under a running install pass (installgate.go).
		// Checked before a RESTART's stop, so a refused restart leaves the server
		// exactly as it was.
		if err := d.installs.check(serverID); err != nil {
			return agentpb.ServerState_SERVER_STATE_UNSPECIFIED, err
		}
	}
	switch action {
	case agentpb.PowerAction_POWER_ACTION_START:
		if err := d.ensureAndStart(ctx, serverID, refreshImage); err != nil {
			return agentpb.ServerState_SERVER_STATE_UNSPECIFIED, err
		}
		// Launch the crash watchdog; readiness (and thus running) is async, so
		// report STARTING and let the watchdog flip to RUNNING on ready_regex.
		d.startMonitor(serverID)
		return agentpb.ServerState_SERVER_STATE_STARTING, nil
	case agentpb.PowerAction_POWER_ACTION_RESTART:
		// Mark the in-flight monitor down first so the stop isn't read as a crash.
		d.markExpectedDown(serverID)
		// A stop that failed means the container may still be running, and
		// ensureContainer keeps a running container as-is — so carrying on
		// would report STARTING for a restart that never happened.
		if err := d.stop(ctx, serverID); err != nil {
			return agentpb.ServerState_SERVER_STATE_UNSPECIFIED, fmt.Errorf("restart: stop: %w", err)
		}
		if err := d.ensureAndStart(ctx, serverID, refreshImage); err != nil {
			return agentpb.ServerState_SERVER_STATE_UNSPECIFIED, err
		}
		d.startMonitor(serverID) // fresh monitor resets the crash-restart counter
		return agentpb.ServerState_SERVER_STATE_STARTING, nil
	case agentpb.PowerAction_POWER_ACTION_STOP:
		d.markExpectedDown(serverID)
		d.setMonitorState(serverID, agentpb.ServerState_SERVER_STATE_STOPPING)
		if err := d.stop(ctx, serverID); err != nil {
			return agentpb.ServerState_SERVER_STATE_UNSPECIFIED, err
		}
		return agentpb.ServerState_SERVER_STATE_OFFLINE, nil
	case agentpb.PowerAction_POWER_ACTION_KILL:
		d.markExpectedDown(serverID)
		d.setMonitorState(serverID, agentpb.ServerState_SERVER_STATE_STOPPING)
		_ = d.containers.ContainerKill(ctx, containerName(serverID), "SIGKILL")
		return agentpb.ServerState_SERVER_STATE_OFFLINE, nil
	default:
		return agentpb.ServerState_SERVER_STATE_UNSPECIFIED, fmt.Errorf("docker: unknown power action %v", action)
	}
}

// imageRefresh says whether a start should ask the registry for a newer image
// before recreating the container. It is a parameter rather than runtime state
// because the two callers want opposite things and both should read that way at
// the call site (#288).
type imageRefresh bool

const (
	// refreshImage is for operator-driven starts. Without it a server installed
	// months ago never moves onto a rebuilt image: ensureContainer recreates the
	// container from whatever happens to be on disk, and Install is the only
	// other thing that pulls.
	refreshImage imageRefresh = true
	// keepImage is for the crash watchdog. A crash loop must recover on the
	// image it was already running — swapping images mid-loop would change what
	// is being diagnosed — and must never stall on an unreachable registry.
	keepImage imageRefresh = false
)

// ensureAndStart is the one path that starts a server's game container — for a
// Power START/RESTART and for the crash watchdog's auto-restart alike — so it
// is where the install gate is enforced for all of them. It is checked again
// right before the start, to close the window where an install began while
// the container was being recreated.
func (d *DockerRuntime) ensureAndStart(ctx context.Context, serverID string, refresh imageRefresh) error {
	if err := d.installs.check(serverID); err != nil {
		return err
	}
	if err := d.ensureContainer(ctx, serverID, refresh); err != nil {
		return err
	}
	if err := d.installs.check(serverID); err != nil {
		return err
	}
	return d.cli.ContainerStart(ctx, containerName(serverID), container.StartOptions{})
}

// ensureContainer makes sure a runnable container exists for the server. A
// running container is kept as-is (image and all); a non-running one
// (created/exited/crashed) is removed and recreated so it starts clean and from
// the current image — data lives on the host bind mount, so nothing is lost, and
// this lets an image rebuild take effect on the next start.
func (d *DockerRuntime) ensureContainer(ctx context.Context, serverID string, refresh imageRefresh) error {
	name := containerName(serverID)
	// One retry: a single daemon hiccup here, during a watchdog auto-restart,
	// would otherwise land the server CRASHED with nothing to retry it.
	if info, err := inspectRetryOnce(ctx, d.containers, name, inspectRetryDelay); err == nil {
		if info.State != nil && info.State.Running {
			return nil // already running
		}
		// Another start caught between its create and its ContainerStart: the
		// container is this server's and only just created. Leave it for the
		// ContainerStart by name that follows — removing it would make the
		// other start's ContainerStart fail. A `created` container that has
		// sat there longer is a leftover from a start that failed, and is
		// recreated from the current spec as before.
		if adoptableCreated(info, serverID, time.Now()) {
			return nil
		}
		// The remove and the create that follows share one name, and Docker
		// frees it asynchronously — so wait for it, and report a removal that
		// failed instead of walking into the conflict. See containername.go.
		if err := d.removeAndAwaitName(ctx, info.ID, name); err != nil {
			return err
		}
	} else if !isNotFound(err) {
		return fmt.Errorf("docker: inspect %s: %w", name, err)
	}
	spec, ok := d.getSpec(serverID)
	if !ok {
		return fmt.Errorf("docker: no spec for server %q (call CreateServer first)", serverID)
	}
	if refresh {
		// Refresh before recreating, so the container that comes back is built on
		// the newer image when one arrives in time. This waits only a few seconds
		// — the Panel bounds the Power RPC — and otherwise leaves the download
		// running in the background; see refreshImageForStart.
		if err := d.refreshImageForStart(ctx, spec.Image, serverID); err != nil {
			return fmt.Errorf("docker: refresh image for %s: %w", serverID, err)
		}
	}
	err := d.createRuntimeContainer(ctx, spec)
	if !isNameConflict(err) {
		return err
	}
	// Someone else holds the name. If it is this server's own container and it
	// is running, another start won the race (a watchdog fast-restart, a
	// double-clicked Start) and the server is already ensured — the
	// ContainerStart that follows is a no-op on a running container. Anything
	// not running is an orphan from a removal that never landed: clear it once
	// and retry, so a stuck name resolves itself rather than needing an
	// operator with `docker rm` — which is what this cost us live (#353). A
	// running container that is not this server's is refused, never killed.
	adopted, cerr := resolveNameConflict(ctx, d.containers, serverID, name)
	if cerr != nil {
		return fmt.Errorf("docker: create %s: %w (resolving the container that held the name: %v)", name, err, cerr)
	}
	if adopted {
		slog.Info("container name already held by this server's running container — another start got there first",
			"server", serverID, "name", name)
		return nil
	}
	slog.Warn("container name was held by an orphan — cleared it, retrying the create",
		"server", serverID, "name", name, "err", err)
	return d.createRuntimeContainer(ctx, spec)
}

// removeAndAwaitName force-removes a container and does not return until its
// name is free for reuse. See removeAndAwait.
func (d *DockerRuntime) removeAndAwaitName(ctx context.Context, id, name string) error {
	return removeAndAwait(ctx, d.containers, id, name)
}

// clearContainerName removes whatever currently answers to name, whether or not
// the Agent put it there, running or not. A name that resolves to nothing needs
// no clearing. It is for Remove, where the server is being deleted; a start
// that loses the name race goes through resolveNameConflict instead, which
// never kills a running container.
func (d *DockerRuntime) clearContainerName(ctx context.Context, name string) error {
	info, err := d.containers.ContainerInspect(ctx, name)
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return err
	}
	return d.removeAndAwaitName(ctx, info.ID, name)
}

// ApplyConfig writes rendered config files into the server's data dir on the
// host (which is bind-mounted into the container).
func (d *DockerRuntime) ApplyConfig(_ context.Context, serverID string, files map[string]string) error {
	for p, content := range files {
		abs, err := d.safePath(strings.ReplaceAll(p, `\`, "/"))
		if err != nil {
			return err
		}
		host := d.localOf(serverID, abs)
		if err := os.MkdirAll(filepath.Dir(host), 0o755); err != nil {
			return fmt.Errorf("docker: config dir for %s: %w", p, err)
		}
		if err := os.WriteFile(host, []byte(content), 0o644); err != nil {
			return fmt.Errorf("docker: write config %s: %w", p, err)
		}
	}
	return nil
}

func (d *DockerRuntime) createRuntimeContainer(ctx context.Context, spec *agentpb.ServerSpec) error {
	dataPath := d.containerDataTarget(spec.ServerId)
	exposed := nat.PortSet{}
	bindings := nat.PortMap{}
	for _, p := range spec.Ports {
		proto := strings.ToLower(p.Protocol)
		if proto != "tcp" && proto != "udp" {
			proto = "tcp"
		}
		cp, err := nat.NewPort(proto, strconv.Itoa(int(p.ContainerPort)))
		if err != nil {
			return fmt.Errorf("docker: port %s: %w", p.Name, err)
		}
		exposed[cp] = struct{}{}
		bindings[cp] = []nat.PortBinding{{HostIP: "0.0.0.0", HostPort: strconv.Itoa(int(p.HostPort))}}
	}

	cfg := &container.Config{
		Image:        spec.Image,
		Entrypoint:   d.shellEntrypoint(),
		Cmd:          []string{spec.StartupCommand},
		Env:          envSlice(spec.Env),
		WorkingDir:   dataPath,
		ExposedPorts: exposed,
		OpenStdin:    true, // so SendCommand can attach and write to stdin
		Tty:          false,
		Labels:       map[string]string{labelManaged: "true", labelServerID: spec.ServerId},
	}
	host := &container.HostConfig{
		// Binds (not the Mounts API) — required for Hyper-V Windows isolation; see Install.
		Binds:        []string{d.bindSource(spec.ServerId) + ":" + dataPath},
		PortBindings: bindings,
	}
	if spec.MemoryLimitMb > 0 {
		host.Resources.Memory = spec.MemoryLimitMb * 1024 * 1024
	}
	d.applyIsolation(host)
	_, err := d.cli.ContainerCreate(ctx, cfg, host, nil, nil, containerName(spec.ServerId))
	return err
}

func (d *DockerRuntime) stop(ctx context.Context, serverID string) error {
	name := containerName(serverID)
	timeout := int(stopGrace / time.Second)
	opts := container.StopOptions{Timeout: &timeout}
	// Windows containers don't support arbitrary stop signals (the daemon sends a
	// shutdown event then kills); only honor a custom signal on Linux. A
	// Windows-only name (e.g. CTRL_SHUTDOWN_EVENT) can reach a Linux container
	// when a Windows-first spec is placed on linux-wine — the daemon would
	// reject it, so fall back to the default (SIGTERM) instead.
	if !d.isWindows() {
		if spec, ok := d.getSpec(serverID); ok && spec.StopSignal != "" && isPosixSignal(spec.StopSignal) {
			opts.Signal = spec.StopSignal
		}
	}
	// A missing container is already stopped, and a stop is not done until the
	// daemon says the container is not running — see stopAndConfirm.
	return stopAndConfirm(ctx, d.containers, name, opts, stopConfirmMargin)
}

// isPosixSignal reports whether s names a signal a Linux daemon accepts:
// SIG-prefixed names (SIGINT, SIGTERM, …) or a numeric value.
func isPosixSignal(s string) bool {
	if strings.HasPrefix(strings.ToUpper(s), "SIG") {
		return true
	}
	_, err := strconv.Atoi(s)
	return err == nil
}

func (d *DockerRuntime) Status(ctx context.Context, serverID string) (*agentpb.ServerStatus, error) {
	// The crash watchdog holds the authoritative lifecycle state (it knows the
	// difference between a graceful stop, a crash, and a not-yet-ready start). Use
	// it when present; otherwise fall back to inspecting the container directly.
	var state agentpb.ServerState
	// The last container exit code travels with the state: it is the only clue a
	// crashed server offers before its next start, and a Windows game that dies
	// on a missing DLL says nothing else anywhere (#280).
	var exitCode int64
	var exitKnown bool
	if st, ok := d.monitorState(serverID); ok {
		state = st
		exitCode, exitKnown = d.monitorExit(serverID)
	} else if insp, err := d.cli.ContainerInspect(ctx, containerName(serverID)); err != nil {
		// No container → treat as offline.
		return &agentpb.ServerStatus{ServerId: serverID, State: agentpb.ServerState_SERVER_STATE_OFFLINE}, nil
	} else {
		state = mapState(insp.State)
		// No watchdog (the Agent restarted after the crash) — the stopped
		// container still carries its exit code, so the diagnosis survives an
		// Agent restart the same way the state does.
		if insp.State != nil && !insp.State.Running && insp.State.Status == "exited" {
			exitCode, exitKnown = int64(insp.State.ExitCode), true
		}
	}
	status := &agentpb.ServerStatus{
		ServerId: serverID, State: state,
		LastExitCode: exitCode, ExitCodeKnown: exitKnown,
	}
	// Attach the online-player count so the reconciler can surface it fleet-wide
	// without an open stats stream (TTL-cached, so this poll rarely hits the game).
	if state == agentpb.ServerState_SERVER_STATE_RUNNING {
		if pl, mx, known := d.sampledPlayers(ctx, serverID); known {
			status.LastStats = &agentpb.ResourceStats{
				ServerId: serverID, Players: pl, MaxPlayers: mx, PlayersKnown: known,
				OnlinePlayers: d.sampledRoster(serverID),
			}
		}
	}
	return status, nil
}

func (d *DockerRuntime) StreamConsole(ctx context.Context, serverID string, tail int32, emit func(*agentpb.ConsoleLine) error) error {
	tailArg := "all"
	if tail >= 0 {
		tailArg = strconv.Itoa(int(tail))
	}
	reader, err := d.cli.ContainerLogs(ctx, containerName(serverID), container.LogsOptions{
		ShowStdout: true, ShowStderr: true, Follow: true, Tail: tailArg,
	})
	if err != nil {
		return fmt.Errorf("docker: container logs: %w", err)
	}
	defer reader.Close()
	return demux(reader, func(stream, text string) error {
		return emit(&agentpb.ConsoleLine{ServerId: serverID, TsUnixMs: nowMs(), Stream: stream, Text: text})
	})
}

func (d *DockerRuntime) SendCommand(ctx context.Context, serverID, command string) error {
	resp, err := d.cli.ContainerAttach(ctx, containerName(serverID), container.AttachOptions{
		Stream: true, Stdin: true,
	})
	if err != nil {
		return fmt.Errorf("docker: attach stdin: %w", err)
	}
	defer resp.Close()
	if _, err := resp.Conn.Write([]byte(command + "\n")); err != nil {
		return fmt.Errorf("docker: write stdin: %w", err)
	}
	return nil
}

func (d *DockerRuntime) StreamStats(ctx context.Context, serverID string, _ int32, emit func(*agentpb.ResourceStats) error) error {
	name := containerName(serverID)

	// Capture the container's start time once so we can report uptime per tick.
	var startedAt time.Time
	if insp, err := d.cli.ContainerInspect(ctx, name); err == nil && insp.State != nil {
		if t, perr := time.Parse(time.RFC3339Nano, insp.State.StartedAt); perr == nil {
			startedAt = t
		}
	}

	resp, err := d.cli.ContainerStats(ctx, name, true)
	if err != nil {
		return fmt.Errorf("docker: container stats: %w", err)
	}
	defer resp.Body.Close()
	dec := json.NewDecoder(resp.Body)

	// Disk usage is comparatively expensive (a `du` over the data dir), so sample
	// it on a slow cadence and reuse the last value between samples.
	var lastDiskMB int64
	var lastDiskAt time.Time
	// Player count is an out-of-band query to the game server (TTL-cached in
	// sampledPlayers, shared with Status so we don't double-query).
	var lastPlayers, lastMaxPlayers int32
	var lastPlayersKnown bool
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var s dockerStats
		if err := dec.Decode(&s); err != nil {
			if err == io.EOF || ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("docker: decode stats: %w", err)
		}
		var rx, tx uint64
		for _, n := range s.Networks {
			rx += n.RxBytes
			tx += n.TxBytes
		}
		var uptime int64
		if !startedAt.IsZero() {
			uptime = int64(time.Since(startedAt).Seconds())
		}
		if lastDiskAt.IsZero() || time.Since(lastDiskAt) > 12*time.Second {
			if mb, derr := d.dirSizeMB(ctx, serverID); derr == nil {
				lastDiskMB = mb
			}
			lastDiskAt = time.Now() // throttle even on error, so we don't hammer
		}
		lastPlayers, lastMaxPlayers, lastPlayersKnown = d.sampledPlayers(ctx, serverID)
		// Memory limit: Linux reports it in the stats; Windows doesn't, so fall
		// back to the container's configured limit.
		memLimitMb := int64(s.MemoryStats.Limit / (1024 * 1024))
		if memLimitMb == 0 {
			if spec, ok := d.getSpec(serverID); ok {
				memLimitMb = spec.MemoryLimitMb
			}
		}
		if err := emit(&agentpb.ResourceStats{
			ServerId:      serverID,
			TsUnixMs:      nowMs(),
			CpuPercent:    d.cpuPercent(s),
			MemoryUsedMb:  int64(d.memUsedBytes(s) / (1024 * 1024)),
			MemoryLimitMb: memLimitMb,
			NetRxBytes:    int64(rx),
			NetTxBytes:    int64(tx),
			UptimeSeconds: uptime,
			DiskUsedMb:    lastDiskMB,
			Players:       lastPlayers,
			MaxPlayers:    lastMaxPlayers,
			PlayersKnown:  lastPlayersKnown,
			OnlinePlayers: d.sampledRoster(serverID),
		}); err != nil {
			return err
		}
	}
}

// dirSizeMB returns the size of the server's data dir in MiB by walking the host
// directory natively (works the same on Linux and Windows).
func (d *DockerRuntime) dirSizeMB(_ context.Context, serverID string) (int64, error) {
	var total int64
	err := filepath.WalkDir(d.localDir(serverID), func(_ string, e os.DirEntry, walkErr error) error {
		if walkErr != nil || e.IsDir() {
			return nil // best-effort: skip unreadable entries
		}
		if info, ierr := e.Info(); ierr == nil {
			total += info.Size()
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return total / (1024 * 1024), nil
}

// isWindows reports whether this agent's daemon runs Windows containers.
func (d *DockerRuntime) isWindows() bool { return d.OSType() == "windows" }

// foldHostPaths reports whether host paths — a bind source, a mount source the
// daemon reports — compare case-insensitively. They do whenever either side is
// Windows: a Windows daemon, or an Agent on a Windows host (Docker Desktop).
func (d *DockerRuntime) foldHostPaths() bool {
	return d.isWindows() || runtime.GOOS == "windows"
}

// installRemoveTimeout bounds the removal of a finished install container,
// including the wait for its name to come free. The pass is already over; a
// removal that does not land in this time is logged, not failed, and the next
// pass's guard clears it.
const installRemoveTimeout = 30 * time.Second

// removeInstallContainer removes a finished install container and waits for
// its name, so the next pass — a retry, or the next update — does not walk
// into the name it still holds (#355).
func (d *DockerRuntime) removeInstallContainer(id, name string) {
	ctx, cancel := context.WithTimeout(context.Background(), installRemoveTimeout)
	defer cancel()
	if err := d.removeAndAwaitName(ctx, id, name); err != nil {
		slog.Warn("install container did not clear after the pass; the next pass will clear it",
			"name", name, "err", err)
	}
}

// dataRoot is the in-container mount point for the server's data dir, and the
// namespace the file browser is confined to. Windows containers use C:\data,
// Linux uses /data. It's kept in forward-slash POSIX form for path-safety logic
// (safePath); containerPath converts it to the OS form for Docker mounts.
func (d *DockerRuntime) dataRoot() string {
	if d.isWindows() {
		return "C:/data"
	}
	return "/data"
}

// shellEntrypoint is the OS-appropriate command interpreter for running a
// server's startup/install script.
func (d *DockerRuntime) shellEntrypoint() []string {
	if d.isWindows() {
		return []string{"cmd", "/S", "/C"}
	}
	return []string{"/bin/sh", "-c"}
}

// safePath cleans p and ensures it stays within d.dataRoot(), returning the absolute
// in-container path. Empty/"." resolves to d.dataRoot().
func (d *DockerRuntime) safePath(p string) (string, error) {
	root := d.dataRoot()
	p = strings.ReplaceAll(p, "\\", "/") // normalize any Windows separators
	if p == "" || p == "." {
		return root, nil
	}
	// Clients address the data dir by the logical "/data" root regardless of node
	// OS; on Windows map that onto the real "C:/data" root so it doesn't read as an
	// escape. ("/data" → "C:/data", "/data/x" → "C:/data/x".)
	if d.isWindows() && (p == "/data" || strings.HasPrefix(p, "/data/")) {
		p = "C:" + p
	}
	if !path.IsAbs(p) && !strings.HasPrefix(p, root) {
		p = path.Join(root, p)
	}
	clean := path.Clean(p)
	if clean != root && !strings.HasPrefix(clean, root+"/") {
		return "", badPath("docker: path %q escapes %s", p, root)
	}
	return clean, nil
}

func (d *DockerRuntime) ListFiles(_ context.Context, serverID, p string) ([]*agentpb.FileEntry, error) {
	dir, err := d.safePath(p)
	if err != nil {
		return nil, err
	}
	ents, err := os.ReadDir(d.localOf(serverID, dir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, d.fileErr(serverID, "list", p, "", err)
	}
	entries := make([]*agentpb.FileEntry, 0, len(ents))
	for _, e := range ents {
		var size, mod int64
		if info, ierr := e.Info(); ierr == nil {
			size = info.Size()
			mod = info.ModTime().UnixMilli()
		}
		entries = append(entries, &agentpb.FileEntry{
			Name:      e.Name(),
			Path:      containerJoin(dir, e.Name()),
			IsDir:     e.IsDir(),
			Size:      size,
			ModUnixMs: mod,
		})
	}
	return entries, nil
}

// statLocal resolves a logical path to its host file and stats it, applying the
// containment check (safePath) every file op shares and refusing a directory.
// The three single-file operations — read, stat, download — all begin this way,
// and having one copy is what keeps their containment and their error text
// identical.
//
// The message names the LOGICAL path only. Whatever this returns reaches an API
// client verbatim (see fileerrors.go), and an *os.PathError from os.Stat carries
// the resolved HOST path — so returning it as-is would teach anyone with
// server.files.read where the node keeps its storage. What is worth keeping is
// the distinction between the failures, not the filename that came with it:
// missing and unreadable are different problems, and an operator chasing one
// should not be told the other.
func (d *DockerRuntime) statLocal(serverID, p string) (string, os.FileInfo, error) {
	fp, err := d.safePath(p)
	if err != nil {
		return "", nil, err
	}
	host := d.localOf(serverID, fp)
	st, err := os.Stat(host)
	if err != nil {
		return "", nil, statError(p, err)
	}
	if st.IsDir() {
		return "", nil, badPath("docker: %s is a directory", p)
	}
	return host, st, nil
}

// ReadFile returns the contents of a single file in the volume, capped at
// maxBytes. It reports the file's true size, whether the returned bytes were
// truncated, and whether the content looks binary (contains a NUL byte).
func (d *DockerRuntime) ReadFile(ctx context.Context, serverID, p string, maxBytes int64) ([]byte, int64, bool, bool, error) {
	if maxBytes <= 0 {
		maxBytes = 1 << 20 // 1 MiB default
	}
	host, st, err := d.statLocal(serverID, p)
	if err != nil {
		return nil, 0, false, false, err
	}
	f, err := os.Open(host)
	if err != nil {
		return nil, 0, false, false, statError(p, err)
	}
	defer f.Close()
	buf, err := io.ReadAll(io.LimitReader(f, maxBytes))
	if err != nil {
		return nil, 0, false, false, statError(p, err)
	}
	size := st.Size()
	truncated := size > int64(len(buf))
	binary := bytes.IndexByte(buf, 0) >= 0
	return buf, size, truncated, binary, nil
}

// StatFile reports a single file's size on disk. Same containment as every
// other file op — safePath first, then the host mapping — and OS-agnostic:
// os.Stat is the same call on a Linux node and a Windows one.
func (d *DockerRuntime) StatFile(_ context.Context, serverID, p string) (int64, error) {
	_, st, err := d.statLocal(serverID, p)
	if err != nil {
		return 0, err
	}
	return st.Size(), nil
}

// DownloadFile streams a single file's raw bytes to w (no zip wrapper).
func (d *DockerRuntime) DownloadFile(_ context.Context, serverID, p string, w io.Writer) error {
	host, _, err := d.statLocal(serverID, p)
	if err != nil {
		return err
	}
	f, err := os.Open(host)
	if err != nil {
		return statError(p, err)
	}
	defer f.Close()
	// A read that fails mid-file (a byte-range lock a running game holds on its
	// save, on Windows) is an *os.PathError naming the host path, so it is
	// rendered like every other read failure; a write failure is the stream's.
	_, err = io.Copy(w, readErrs{r: f, wrap: func(rerr error) error { return statError(p, rerr) }})
	return err
}

func (d *DockerRuntime) ZipFiles(_ context.Context, serverID string, paths []string, w io.Writer) error {
	zw := zip.NewWriter(w)
	defer zw.Close()
	root := d.localDir(serverID)
	for _, p := range paths {
		src, err := d.safePath(p)
		if err != nil {
			return err
		}
		hostSrc := d.localOf(serverID, src)
		err = filepath.WalkDir(hostSrc, func(fp string, e os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if e.IsDir() {
				return nil // zip infers directories from file paths
			}
			rel, rerr := filepath.Rel(root, fp)
			if rerr != nil {
				return rerr
			}
			info, ierr := e.Info()
			if ierr != nil {
				return ierr
			}
			ze, cerr := zw.CreateHeader(&zip.FileHeader{Name: filepath.ToSlash(rel), Method: zip.Deflate, Modified: info.ModTime()})
			if cerr != nil {
				return cerr
			}
			f, oerr := os.Open(fp)
			if oerr != nil {
				return oerr
			}
			_, cerr = io.Copy(ze, f)
			f.Close()
			return cerr
		})
		if err != nil {
			return d.fileErr(serverID, "zip", p, "", err)
		}
	}
	return nil
}

// MakeDir creates a directory (and parents) in the server's data dir.
func (d *DockerRuntime) MakeDir(_ context.Context, serverID, p string) error {
	dir, err := d.safePath(p)
	if err != nil {
		return err
	}
	if dir == d.dataRoot() {
		return nil
	}
	if err := os.MkdirAll(d.localOf(serverID, dir), 0o755); err != nil {
		return d.fileErr(serverID, "mkdir", p, "", err)
	}
	return nil
}

// WriteFile writes/overwrites a single file in the server's data dir.
func (d *DockerRuntime) WriteFile(_ context.Context, serverID, p string, content []byte) error {
	fp, err := d.safePath(p)
	if err != nil {
		return err
	}
	host := d.localOf(serverID, fp)
	if err := os.MkdirAll(filepath.Dir(host), 0o755); err != nil {
		return d.fileErr(serverID, "create the folder for", p, "", err)
	}
	if err := os.WriteFile(host, content, 0o644); err != nil {
		return d.fileErr(serverID, "write", p, "", err)
	}
	return nil
}

// DeletePaths removes files/directories (recursively) from the data dir.
func (d *DockerRuntime) DeletePaths(_ context.Context, serverID string, paths []string) error {
	for _, p := range paths {
		sp, err := d.safePath(p)
		if err != nil {
			return err
		}
		if sp == d.dataRoot() {
			continue // never delete the data root
		}
		if err := os.RemoveAll(d.localOf(serverID, sp)); err != nil {
			return d.fileErr(serverID, "delete", p, "", err)
		}
	}
	return nil
}

// MovePath renames/moves a file or directory within the data dir.
func (d *DockerRuntime) MovePath(_ context.Context, serverID, src, dst string) error {
	s, err := d.safePath(src)
	if err != nil {
		return err
	}
	dp, err := d.safePath(dst)
	if err != nil {
		return err
	}
	if s == d.dataRoot() || dp == d.dataRoot() {
		return badPath("docker: cannot move the data root")
	}
	hostDst := d.localOf(serverID, dp)
	if err := os.MkdirAll(filepath.Dir(hostDst), 0o755); err != nil {
		return d.fileErr(serverID, "create the folder for", dst, "", err)
	}
	if err := os.Rename(d.localOf(serverID, s), hostDst); err != nil {
		return d.fileErr(serverID, "move", src, " → "+dst, err)
	}
	return nil
}

// CopyPath copies a file or directory within the data dir.
func (d *DockerRuntime) CopyPath(_ context.Context, serverID, src, dst string) error {
	s, err := d.safePath(src)
	if err != nil {
		return err
	}
	dp, err := d.safePath(dst)
	if err != nil {
		return err
	}
	if s == d.dataRoot() {
		return badPath("docker: cannot copy the data root")
	}
	if dp == d.dataRoot() {
		return badPath("docker: cannot copy onto the data root")
	}
	if err := copyTreeFS(d.localOf(serverID, s), d.localOf(serverID, dp)); err != nil {
		return d.fileErr(serverID, "copy", src, " → "+dst, err)
	}
	return nil
}

// containerPath converts an internal POSIX-style path to the form the Docker
// mount/WorkingDir fields expect for this daemon. On Windows that means
// backslashes (C:\data); Linux is unchanged. Path-safety logic (safePath) stays
// in POSIX form; this is applied only at the Docker API boundary.
func (d *DockerRuntime) containerPath(p string) string {
	if d.isWindows() {
		return strings.ReplaceAll(p, "/", `\`)
	}
	return p
}

// ---- backups ----

var backupNameRE = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// CreateBackup snapshots the server's data dir into a gzipped tar archive and
// writes it to the configured backup target (node-local or SFTP). The archive is
// staged in a temp file so its size and content hash are known before upload.
// Entries are relative to the data dir, so restore is a straight extraction.
// CreateBackup kicks off an asynchronous backup and returns immediately with a
// PENDING record. The archive is tar+gzipped to the local store in the
// background (runBackup) and then mirrored off-node when replication is
// configured. Callers poll ListBackups for the state transitions — this keeps a
// multi-GB game server from blocking (and timing out) the Panel→Agent RPC.
func (d *DockerRuntime) CreateBackup(_ context.Context, serverID, slug, name string, include, exclude []string) (*agentpb.BackupInfo, error) {
	if name == "" {
		name = "backup"
	}
	created := time.Now().UnixMilli()
	id := fmt.Sprintf("%d__%s", created, backupNameRE.ReplaceAllString(name, "-"))

	rep := agentpb.ReplicationState_REPLICATION_STATE_UNSPECIFIED
	if d.replicateTargetFor(slug) != nil {
		rep = agentpb.ReplicationState_REPLICATION_STATE_PENDING
	}
	info := &agentpb.BackupInfo{
		Id: id, Name: name, CreatedUnixMs: created,
		State:       agentpb.BackupState_BACKUP_STATE_PENDING,
		Replication: rep,
	}
	d.putBackupJob(serverID, info)
	go d.runBackup(serverID, slug, id, newBackupFilter(include, exclude))
	return cloneBackup(info), nil
}

// runBackup archives the data dir to the local store, then mirrors the finished
// archive off-node — each step a tracked state transition. Runs on its own
// long-lived context, independent of the request that triggered it.
func (d *DockerRuntime) runBackup(serverID, slug, id string, filter *backupFilter) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()

	tmp, err := os.CreateTemp("", "kraken-backup-*.tar.gz")
	if err != nil {
		d.failBackup(serverID, id, err)
		return
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	defer tmp.Close()

	stats, err := d.archiveDataDir(serverID, tmp, filter)
	if err != nil {
		d.failBackup(serverID, id, err)
		return
	}
	if stats.filtered > 0 || stats.prunedDirs > 0 {
		// Not a degradation — the whole point of #218 is that the install tree
		// stays out. Logged so an operator can see the globs took effect.
		slog.Info("backup filtered by spec globs", "server", serverID, "id", id,
			"captured", stats.files, "filtered", stats.filtered, "pruned_dirs", stats.prunedDirs)
	}
	degraded := stats.summary()
	if degraded != "" {
		slog.Warn("backup captured with degradations", "server", serverID, "id", id, "detail", degraded)
	}
	size, err := tmp.Seek(0, io.SeekEnd)
	if err != nil {
		d.failBackup(serverID, id, fmt.Errorf("size archive: %w", err))
		return
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		d.failBackup(serverID, id, err)
		return
	}

	target := d.backupTargetFor(slug)
	if err := target.Put(ctx, serverID, id, tmp, size); err != nil {
		d.failBackup(serverID, id, fmt.Errorf("store backup (%s): %w", target.Kind(), err))
		return
	}
	d.updateBackupJob(serverID, id, func(b *agentpb.BackupInfo) {
		b.Size = size
		b.State = agentpb.BackupState_BACKUP_STATE_READY
		b.Error = degraded
	})

	// Off-node mirror of the finished archive — a separate, best-effort step.
	rep := d.replicateTargetFor(slug)
	if rep == nil {
		return
	}
	if _, serr := tmp.Seek(0, io.SeekStart); serr != nil {
		d.setReplication(serverID, id, agentpb.ReplicationState_REPLICATION_STATE_FAILED)
		return
	}
	if err := rep.Put(ctx, serverID, id, tmp, size); err != nil {
		slog.Warn("backup replication failed", "server", serverID, "id", id, "err", err)
		d.setReplication(serverID, id, agentpb.ReplicationState_REPLICATION_STATE_FAILED)
		d.pruneBackups(ctx, serverID, slug)
		return
	}
	d.setReplication(serverID, id, agentpb.ReplicationState_REPLICATION_STATE_DONE)
	d.pruneBackups(ctx, serverID, slug)
}

// backupRetentionKeep is how many of a server's most recent archives are kept.
// Fixed for now — there is no per-server knob yet (see the retention issue). A
// scheduled nightly plus manual backups otherwise grow the store without bound.
const backupRetentionKeep = 5

// pruneBackups enforces retention after a successful backup: it keeps the
// backupRetentionKeep most recent archives and deletes the rest from the primary
// store AND the off-node mirror both, so an evicted save is gone everywhere the
// operator was told it would be (see The Spoken Mirror Rule in DESIGN.md). It is
// best-effort — a prune failure never fails the backup that triggered it — and it
// counts only real archives on disk (target.List), so a failed attempt, which
// captured nothing, occupies no retention slot.
func (d *DockerRuntime) pruneBackups(ctx context.Context, serverID, slug string) {
	list, err := d.backupTargetFor(slug).List(ctx, serverID)
	if err != nil {
		slog.Warn("retention: could not list backups to prune", "server", serverID, "err", err)
		return
	}
	if len(list) <= backupRetentionKeep {
		return
	}
	sortBackups(list) // newest first; the tail past the keep count is evicted
	for _, b := range list[backupRetentionKeep:] {
		// DeleteBackup drops the local archive, its off-node mirror, and the
		// tracked job — the same removal the operator's own delete performs.
		if derr := d.DeleteBackup(ctx, serverID, slug, b.Id); derr != nil {
			slog.Warn("retention: could not evict backup", "server", serverID, "id", b.Id, "err", derr)
			continue
		}
		slog.Info("retention: evicted oldest backup from node and mirror",
			"server", serverID, "id", b.Id, "keep", backupRetentionKeep)
	}
}

// archiveDataDir tar+gzips the filter-selected parts of a server's data dir into
// w, tolerating a live tree (see archive.go). A capture with zero regular files
// is an error — an archive of nothing would restore to nothing, and with globs in
// play that most likely means the spec's include list is wrong, which is exactly
// the failure that must never come back green.
func (d *DockerRuntime) archiveDataDir(serverID string, w io.Writer, filter *backupFilter) (archiveStats, error) {
	st, err := archiveTreeFiltered(d.localDir(serverID), w, filter)
	if err != nil {
		return st, fmt.Errorf("docker: archive data dir: %w", err)
	}
	if st.files == 0 {
		if st.filtered > 0 || st.prunedDirs > 0 {
			return st, fmt.Errorf("docker: archive data dir: the spec's backup globs matched no files (%d filtered, %d dir(s) pruned)", st.filtered, st.prunedDirs)
		}
		return st, fmt.Errorf("docker: archive data dir: no files under the data dir")
	}
	return st, nil
}

// ---- async backup job tracker (guarded by bjMu) ----

func backupJobKey(serverID, id string) string { return serverID + "/" + id }

func cloneBackup(b *agentpb.BackupInfo) *agentpb.BackupInfo {
	return &agentpb.BackupInfo{
		Id: b.Id, Name: b.Name, Size: b.Size, CreatedUnixMs: b.CreatedUnixMs,
		State: b.State, Replication: b.Replication, Error: b.Error,
	}
}

func (d *DockerRuntime) putBackupJob(serverID string, info *agentpb.BackupInfo) {
	d.bjMu.Lock()
	defer d.bjMu.Unlock()
	if d.backupJobs == nil {
		d.backupJobs = map[string]*agentpb.BackupInfo{}
	}
	d.backupJobs[backupJobKey(serverID, info.Id)] = cloneBackup(info)
}

func (d *DockerRuntime) updateBackupJob(serverID, id string, fn func(*agentpb.BackupInfo)) {
	d.bjMu.Lock()
	defer d.bjMu.Unlock()
	if b := d.backupJobs[backupJobKey(serverID, id)]; b != nil {
		fn(b)
	}
}

func (d *DockerRuntime) setReplication(serverID, id string, st agentpb.ReplicationState) {
	d.updateBackupJob(serverID, id, func(b *agentpb.BackupInfo) { b.Replication = st })
}

func (d *DockerRuntime) failBackup(serverID, id string, err error) {
	slog.Warn("backup failed", "server", serverID, "id", id, "err", err)
	var rec *failureRecord
	d.bjMu.Lock()
	if b := d.backupJobs[backupJobKey(serverID, id)]; b != nil {
		b.State = agentpb.BackupState_BACKUP_STATE_FAILED
		b.Error = err.Error()
		if b.Replication == agentpb.ReplicationState_REPLICATION_STATE_PENDING {
			b.Replication = agentpb.ReplicationState_REPLICATION_STATE_UNSPECIFIED
		}
		rec = &failureRecord{ID: b.Id, Name: b.Name, CreatedUnixMs: b.CreatedUnixMs, Error: b.Error}
	}
	d.bjMu.Unlock()
	// Persisted outside bjMu — file IO must not sit inside the tracker lock.
	if rec != nil && d.failures != nil {
		d.failures.record(serverID, *rec)
	}
}

func (d *DockerRuntime) forgetBackupJob(serverID, id string) {
	d.bjMu.Lock()
	delete(d.backupJobs, backupJobKey(serverID, id))
	d.bjMu.Unlock()
	if d.failures != nil {
		d.failures.forget(serverID, id)
	}
}

// forgetServerBackupJobs clears a removed server's tracked jobs and its
// persisted failure history — the records are tied to the server's existence.
func (d *DockerRuntime) forgetServerBackupJobs(serverID string) {
	prefix := serverID + "/"
	d.bjMu.Lock()
	for key := range d.backupJobs {
		if strings.HasPrefix(key, prefix) {
			delete(d.backupJobs, key)
		}
	}
	d.bjMu.Unlock()
	if d.failures != nil {
		d.failures.forgetServer(serverID)
	}
}

// ListBackups merges the on-disk archives (the source of truth for completed
// backups) with the in-memory job tracker, so callers see in-flight (PENDING) and
// FAILED archives plus the off-node replication state that the disk listing can't
// express. On-disk archives list as READY; tracked jobs overlay their state.
func (d *DockerRuntime) ListBackups(ctx context.Context, serverID, slug string) ([]*agentpb.BackupInfo, error) {
	disk, err := d.backupTargetFor(slug).List(ctx, serverID)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]*agentpb.BackupInfo, len(disk))
	for _, b := range disk {
		b.State = agentpb.BackupState_BACKUP_STATE_READY
		byID[b.Id] = b
	}
	d.bjMu.Lock()
	prefix := serverID + "/"
	for key, job := range d.backupJobs {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		if onDisk := byID[job.Id]; onDisk != nil {
			// Completed archive: keep its real size, but carry the tracked
			// replication state (and any terminal archive state / reason).
			onDisk.Replication = job.Replication
			onDisk.Error = job.Error
			if job.State != agentpb.BackupState_BACKUP_STATE_UNSPECIFIED {
				onDisk.State = job.State
			}
		} else {
			byID[job.Id] = cloneBackup(job)
		}
	}
	d.bjMu.Unlock()

	out := make([]*agentpb.BackupInfo, 0, len(byID))
	for _, b := range byID {
		out = append(out, b)
	}
	sortBackups(out)
	return out, nil
}

// RestoreBackup reproduces the archived state for everything the archive
// covers, leaving the rest of the data dir (the install tree) untouched. The
// mechanism — stage into a scratch dir inside the server's data dir, then swap
// the covered paths in — lives in restore.go, which documents why.
//
// The caller is expected to have stopped the server: the Panel refuses a
// restore otherwise (a running game holds the very files a save-set restore
// replaces), and the Agent cannot see the Panel's view of that state.
//
// Its failure reaches the operator verbatim, so it is scrubbed of the node's
// host paths on the way out (the staging dir and every unit it names live under
// the data dir) — the error chain is kept, so a locked file still classifies as
// one at the gRPC boundary.
func (d *DockerRuntime) RestoreBackup(ctx context.Context, serverID, slug, id string) error {
	return d.RestoreBackupStream(ctx, serverID, slug, id, nil)
}

// errRestoreOverRunning is the refusal for a restore aimed at a live game.
var errRestoreOverRunning = grpcstatus.Error(codes.FailedPrecondition,
	"the server's container is running — stop it before restoring; the live tree was not touched")

// refuseRestoreOverRunningContainer refuses a restore while kraken_<id> is
// running, restarting or paused: each of those holds the very save files the
// swap replaces. No container (a server that never started), or a stopped
// one, is what a restore wants. An inspect that fails for any other reason
// fails CLOSED with Unavailable: this check exists because the Panel's
// "stopped" can be stale, so it must not wave a restore through on the very
// occasion it could not look.
func (d *DockerRuntime) refuseRestoreOverRunningContainer(ctx context.Context, serverID string) error {
	status, found, err := d.inspectGameContainer(ctx, containerName(serverID))
	if err != nil {
		return grpcstatus.Errorf(codes.Unavailable,
			"could not check whether the server's container is running: %v; the live tree was not touched", err)
	}
	if !found {
		return nil
	}
	switch status {
	case "running", "restarting", "paused":
		return errRestoreOverRunning
	}
	return nil
}

// inspectGameContainer reports the game container's status, or found=false
// when there is none. It goes through the containerOps seam, so a test can
// stand a fake daemon in for it.
func (d *DockerRuntime) inspectGameContainer(ctx context.Context, name string) (status string, found bool, err error) {
	if d.containers == nil {
		return "", false, nil // file-ops-only runtime (tests): there is no container to hold anything
	}
	info, err := d.containers.ContainerInspect(ctx, name)
	if err != nil {
		if isNotFound(err) {
			return "", false, nil
		}
		return "", false, err
	}
	if info.ContainerJSONBase != nil && info.State != nil {
		status = info.State.Status
	}
	return status, true, nil
}

// RestoreBackupStream is RestoreBackup narrated through emit (#361): the phase,
// and the compressed bytes read against the archive's size. It is the one
// restore path; the unary RestoreBackup passes a nil emit.
//
// Every failure before the swap leaves the live tree untouched, and says so; a
// failure during the swap is unwound by applyRestore, which says whether the
// rollback was complete. The Panel shows the message to the operator verbatim,
// scrubbed of the node's host paths (see RestoreBackup).
func (d *DockerRuntime) RestoreBackupStream(ctx context.Context, serverID, slug, id string, emit func(*agentpb.RestoreEvent) error) error {
	// The Agent's own check, behind the Panel's: the Panel refuses a restore on
	// a server it believes is stopped, but its belief can be seconds stale — a
	// start whose Power call is still in flight has not written the row yet.
	// The container is the ground truth, so a running game is refused here.
	// Returned outside the scrub: the refusal is a gRPC status already (the
	// interceptor passes it through), names no host path, and must keep its
	// code for the Panel to read it as a restore that did not start.
	//
	// An install pass holds the same tree the restore would swap, so the
	// install gate refuses first, with the same Aborted a start gets there.
	if err := d.installs.check(serverID); err != nil {
		return err
	}
	if err := d.refuseRestoreOverRunningContainer(ctx, serverID); err != nil {
		return err
	}
	return d.scrubbed(serverID, d.restoreBackup(ctx, serverID, slug, id, emit))
}

func (d *DockerRuntime) restoreBackup(ctx context.Context, serverID, slug, id string, emit func(*agentpb.RestoreEvent) error) error {
	const untouched = "; the live tree was not touched"
	m := newRestoreMeter(ctx, emit)
	if err := m.enter(restorePhaseOpening); err != nil {
		return err
	}
	root := d.localDir(serverID)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	r, err := d.backupTargetFor(slug).Open(ctx, serverID, id)
	if err != nil {
		// The local store's *fs.PathError names the node's backup dir; the
		// backup's id is what the operator knows it by.
		return fmt.Errorf("%w%s", d.fileErr(serverID, "open backup", id, "", err), untouched)
	}
	defer r.Close()
	m.total = archiveSize(r)
	// Every read of the archive — gzip's header, then each tar entry — goes
	// through the store, and a store's read failure names the archive where it
	// lives (KRAKEN_BACKUP_DIR, a share). The backup's id is what the operator
	// knows it by. The meter counts outside that wrapper, so its own
	// cancellation and stream errors reach the caller unrendered.
	archive := readErrs{r: r, wrap: func(rerr error) error {
		return d.fileErr(serverID, "read backup", id, "", rerr)
	}}
	counted := m.reader(archive)
	gz, err := gzip.NewReader(counted)
	if err != nil {
		return fmt.Errorf("docker: gunzip backup: %w%s", restoreCause(ctx, m, err), untouched)
	}
	defer gz.Close()
	if err := m.enter(restorePhaseExtracting); err != nil {
		return err
	}

	staged, err := os.MkdirTemp(root, restoreScratchPrefix+"*")
	if err != nil {
		return fmt.Errorf("docker: restore staging dir: %w", err)
	}
	// The staging dir never outlives the restore, success or failure: what is
	// left in it is whatever the swap did not move into the live tree.
	defer func() {
		if rerr := os.RemoveAll(staged); rerr != nil {
			slog.Warn("could not remove restore staging dir", "server", serverID, "dir", staged, "err", rerr)
		}
	}()

	st, err := d.extractArchive(ctx, tar.NewReader(gz), serverID, staged, m)
	if err != nil {
		return fmt.Errorf("%w%s", err, untouched)
	}
	if st.links+st.irregular > 0 {
		slog.Warn("restore skipped archive entries it cannot materialize",
			"server", serverID, "id", id, "links", st.links, "irregular", st.irregular)
	}
	// The tar reader stops at the end-of-archive marker, which leaves the
	// block padding and the gzip trailer unread. Reading them out is what makes
	// the meter end at the archive's size, and it is also the first time the
	// gzip CRC is checked at all. A mismatch only warns: every entry already
	// staged cleanly, and the restore never used to look.
	if _, derr := io.Copy(io.Discard, gz); derr != nil {
		if cerr := restoreCause(ctx, m, derr); ctx.Err() != nil || m.err != nil {
			return fmt.Errorf("docker: restore: %w%s", cerr, untouched)
		}
		slog.Warn("restore: the archive's gzip trailer did not verify", "server", serverID, "id", id, "err", derr)
	}
	_, _ = io.Copy(io.Discard, counted) // anything after the gzip member, e.g. a foreign writer's padding
	if err := m.enter(restorePhaseApplying); err != nil {
		return fmt.Errorf("%w%s", err, untouched)
	}
	if err := d.applyRestore(ctx, serverID, root, staged, st); err != nil {
		return err
	}
	// Committed: the tree is restored whether or not anybody hears about it, so
	// a stream that died at the last moment is not a failure.
	if err := m.enter(restorePhaseDone); err != nil {
		slog.Warn("restore landed but the done event could not be sent", "server", serverID, "id", id, "err", err)
	}
	return nil
}

func (d *DockerRuntime) DeleteBackup(ctx context.Context, serverID, slug, id string) error {
	err := d.backupTargetFor(slug).Delete(ctx, serverID, id)
	// Also drop the off-node mirror (best-effort) and forget the tracked job —
	// otherwise the in-memory tracker re-adds the archive in ListBackups and the
	// backup appears to "come back" after deletion.
	if rep := d.replicateTargetFor(slug); rep != nil {
		_ = rep.Delete(ctx, serverID, id)
	}
	d.forgetBackupJob(serverID, id)
	if err != nil {
		return d.fileErr(serverID, "delete backup", id, "", err)
	}
	return nil
}

// ---- helpers ----

func (d *DockerRuntime) fail(emit func(*agentpb.InstallEvent) error, msg string) error {
	_ = emit(&agentpb.InstallEvent{Event: &agentpb.InstallEvent_Failed{Failed: msg}})
	return fmt.Errorf("docker install: %s", msg)
}

// failUntouched is fail for a pass that ended before anything could write to
// the install tree (InstallEvent.tree_untouched).
func (d *DockerRuntime) failUntouched(emit func(*agentpb.InstallEvent) error, msg string) error {
	_ = emit(untouchedFailure(msg))
	return fmt.Errorf("docker install: %s", msg)
}

// untouchedFailure is the Failed event for a pass that never touched the tree.
func untouchedFailure(msg string) *agentpb.InstallEvent {
	return &agentpb.InstallEvent{Event: &agentpb.InstallEvent_Failed{Failed: msg}, TreeUntouched: true}
}

// streamLogs streams a container's logs to fn until the stream ends (used for
// the bounded install phase). demux is used for the unbounded console stream.
func (d *DockerRuntime) streamLogs(ctx context.Context, id, tail string, fn func(stream, text string) error) error {
	reader, err := d.containers.ContainerLogs(ctx, id, container.LogsOptions{
		ShowStdout: true, ShowStderr: true, Follow: true, Tail: tail,
	})
	if err != nil {
		return err
	}
	defer reader.Close()
	return demux(reader, fn)
}

// demux splits a Docker multiplexed (stdout+stderr) log stream into lines and
// invokes fn for each. Both streams are surfaced; stderr lines are labeled.
func demux(reader io.Reader, fn func(stream, text string) error) error {
	prOut, pwOut := io.Pipe()
	prErr, pwErr := io.Pipe()
	go func() {
		_, err := stdcopy.StdCopy(pwOut, pwErr, reader)
		_ = pwOut.CloseWithError(err)
		_ = pwErr.CloseWithError(err)
	}()

	errc := make(chan error, 2)
	scan := func(r io.Reader, name string) {
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			if err := fn(name, sc.Text()); err != nil {
				errc <- err
				return
			}
		}
		errc <- sc.Err()
	}
	go scan(prOut, "stdout")
	go scan(prErr, "stderr")

	var firstErr error
	for i := 0; i < 2; i++ {
		if err := <-errc; err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func envSlice(m map[string]string) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	return out
}

func logLine(text string) *agentpb.InstallEvent {
	return &agentpb.InstallEvent{Event: &agentpb.InstallEvent_LogLine{LogLine: text}}
}

// dockerStats is a minimal projection of the Docker stats JSON, decoded directly
// to stay independent of SDK type churn across versions.
type dockerStats struct {
	// Read/PreRead bound the sample interval; Windows CPU% is derived from them.
	Read     time.Time `json:"read"`
	PreRead  time.Time `json:"preread"`
	NumProcs uint32    `json:"num_procs"` // Windows: logical processors
	CPUStats struct {
		CPUUsage struct {
			TotalUsage  uint64   `json:"total_usage"`
			PercpuUsage []uint64 `json:"percpu_usage"`
		} `json:"cpu_usage"`
		SystemUsage uint64 `json:"system_cpu_usage"` // Linux only
		OnlineCPUs  uint32 `json:"online_cpus"`      // Linux only
	} `json:"cpu_stats"`
	PreCPUStats struct {
		CPUUsage struct {
			TotalUsage uint64 `json:"total_usage"`
		} `json:"cpu_usage"`
		SystemUsage uint64 `json:"system_cpu_usage"`
	} `json:"precpu_stats"`
	MemoryStats struct {
		Usage             uint64 `json:"usage"`             // Linux
		Limit             uint64 `json:"limit"`             // Linux
		PrivateWorkingSet uint64 `json:"privateworkingset"` // Windows
	} `json:"memory_stats"`
	Networks map[string]struct {
		RxBytes uint64 `json:"rx_bytes"`
		TxBytes uint64 `json:"tx_bytes"`
	} `json:"networks"`
}

// cpuPercent computes CPU utilization from a stats sample, using the Linux
// (system-usage delta) or Windows (100ns-intervals × processors) formula as
// appropriate. Windows stats omit system_cpu_usage, so it's detected by that.
func (d *DockerRuntime) cpuPercent(s dockerStats) float64 {
	if d.isWindows() {
		// Max 100ns intervals available between reads × processors.
		possIntervals := uint64(s.Read.Sub(s.PreRead).Nanoseconds()) / 100 * uint64(s.NumProcs)
		used := s.CPUStats.CPUUsage.TotalUsage - s.PreCPUStats.CPUUsage.TotalUsage
		if possIntervals == 0 {
			return 0
		}
		return float64(used) / float64(possIntervals) * 100.0
	}
	cpuDelta := float64(s.CPUStats.CPUUsage.TotalUsage) - float64(s.PreCPUStats.CPUUsage.TotalUsage)
	sysDelta := float64(s.CPUStats.SystemUsage) - float64(s.PreCPUStats.SystemUsage)
	if sysDelta <= 0 || cpuDelta < 0 {
		return 0
	}
	cpus := float64(s.CPUStats.OnlineCPUs)
	if cpus == 0 {
		cpus = float64(len(s.CPUStats.CPUUsage.PercpuUsage))
	}
	if cpus == 0 {
		cpus = 1
	}
	return (cpuDelta / sysDelta) * cpus * 100.0
}

// memUsedBytes returns the container's memory usage: working set on Windows,
// cgroup usage on Linux.
func (d *DockerRuntime) memUsedBytes(s dockerStats) uint64 {
	if d.isWindows() {
		return s.MemoryStats.PrivateWorkingSet
	}
	return s.MemoryStats.Usage
}

// mapState maps Docker container state to the proto ServerState.
func mapState(st *container.State) agentpb.ServerState {
	if st == nil {
		return agentpb.ServerState_SERVER_STATE_OFFLINE
	}
	switch {
	case st.Restarting:
		return agentpb.ServerState_SERVER_STATE_STARTING
	case st.Running:
		return agentpb.ServerState_SERVER_STATE_RUNNING
	case st.Dead || (st.ExitCode != 0 && st.Status == "exited"):
		return agentpb.ServerState_SERVER_STATE_CRASHED
	default:
		return agentpb.ServerState_SERVER_STATE_OFFLINE
	}
}
