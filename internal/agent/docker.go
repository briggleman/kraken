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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/docker/go-connections/nat"
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
	cli         *client.Client
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
	// an SFTP remote every new backup is also mirrored to.
	bmu       sync.RWMutex
	backups   backupTarget        // primary store (local fs or SFTP)
	replicate backupTarget        // optional SFTP mirror (nil when replication is off)
	nodeCfg   *agentpb.NodeConfig // last-applied config; source for per-server path templating (nil = defaults)
	sftpPort  int32               // port the Agent's SFTP server bound (0 = SFTP off); reported in NodeInfo

	mu    sync.Mutex
	specs map[string]*agentpb.ServerSpec // serverID → runtime spec

	monMu    sync.Mutex
	monitors map[string]*monitor // serverID → crash watchdog

	// bjMu guards backupJobs: the live state of asynchronous backups, keyed by
	// "<serverID>/<id>". Holds in-flight (PENDING) jobs and recently-finished ones
	// so ListBackups can report archiving + replication state the on-disk listing
	// can't. Cleared on restart (on-disk archives then list as READY).
	bjMu       sync.Mutex
	backupJobs map[string]*agentpb.BackupInfo

	// pcMu guards playerSamples: the last online-player count per server, TTL-cached
	// so StreamStats (live) and Status (reconcile poll) share one query rather than
	// each hitting the game server.
	pcMu          sync.Mutex
	playerSamples map[string]playerSample
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
	d := &DockerRuntime{cli: cli, nodeID: nodeID, wineEnabled: wineEnabled, version: version, osType: osType, dataDir: dataDir, hostDataDir: hostDataDir, backupDir: backupDir, specDir: filepath.Join(stateDir, "agent-specs"), winIsolation: windowsIsolation(), specs: map[string]*agentpb.ServerSpec{}, monitors: map[string]*monitor{}, backupJobs: map[string]*agentpb.BackupInfo{}}
	d.backups = selectBackupTarget(backupDir)
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
// backupDir. The Panel can later hot-swap this for an SFTP remote via ApplyNodeConfig.
func selectBackupTarget(backupDir string) backupTarget {
	return &localBackupTarget{dir: backupDir}
}

// replicateTarget returns the current SFTP mirror (nil when replication is off).
func (d *DockerRuntime) replicateTarget() backupTarget {
	d.bmu.RLock()
	defer d.bmu.RUnlock()
	return d.replicate
}

// expandCfgPaths returns a NodeConfig copy with backup_dir and sftp_base_path
// expanded for slug. Fields are copied by name (not *cfg) so the proto's
// internal lock isn't copied (go vet copylocks).
func expandCfgPaths(cfg *agentpb.NodeConfig, slug string) *agentpb.NodeConfig {
	return &agentpb.NodeConfig{
		BackupTarget:    cfg.GetBackupTarget(),
		BackupDir:       expandBackupPath(cfg.GetBackupDir(), slug),
		SftpHost:        cfg.GetSftpHost(),
		SftpUser:        cfg.GetSftpUser(),
		SftpPassword:    cfg.GetSftpPassword(),
		SftpPrivateKey:  cfg.GetSftpPrivateKey(),
		SftpBasePath:    expandBackupPath(cfg.GetSftpBasePath(), slug),
		ReplicateToSftp: cfg.GetReplicateToSftp(),
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

// replicateTargetFor returns the SFTP mirror with path tokens expanded for slug
// (nil when replication is off).
func (d *DockerRuntime) replicateTargetFor(slug string) backupTarget {
	d.bmu.RLock()
	cfg := d.nodeCfg
	d.bmu.RUnlock()
	if cfg == nil || !cfg.GetReplicateToSftp() {
		return d.replicateTarget()
	}
	return buildSFTPTarget(expandCfgPaths(cfg, slug))
}

// buildTarget constructs the primary backup target described by cfg. An empty
// or "local" target (and an empty backup_dir) falls back to the env-derived
// default so the Panel can leave fields blank.
func (d *DockerRuntime) buildTarget(cfg *agentpb.NodeConfig) backupTarget {
	switch cfg.GetBackupTarget() {
	case "sftp":
		return buildSFTPTarget(cfg)
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

// ApplyNodeConfig hot-swaps the backup target(s) from Panel-managed config and
// reports whether the configured SFTP endpoint(s) are reachable. The swap takes
// effect even when verification fails, so the operator's intent persists.
func (d *DockerRuntime) ApplyNodeConfig(_ context.Context, cfg *agentpb.NodeConfig) (bool, string) {
	if cfg == nil {
		return true, "no config"
	}
	primary := d.buildTarget(cfg)
	var replicate backupTarget
	if cfg.GetReplicateToSftp() {
		replicate = buildSFTPTarget(cfg)
	}

	d.bmu.Lock()
	d.backups = primary
	d.replicate = replicate
	d.nodeCfg = cfg // retained so per-server backup ops can expand path tokens (e.g. {{SLUG}})
	d.bmu.Unlock()

	ok := true
	var msgs []string
	if st, isSFTP := primary.(*sftpBackupTarget); isSFTP {
		if err := st.verify(); err != nil {
			ok = false
			msgs = append(msgs, err.Error())
		}
	}
	if st, isShare := primary.(*shareBackupTarget); isShare {
		if err := st.verify(); err != nil {
			ok = false
			msgs = append(msgs, err.Error())
		}
	}
	if st, isSFTP := replicate.(*sftpBackupTarget); isSFTP {
		if err := st.verify(); err != nil {
			ok = false
			msgs = append(msgs, "replication: "+err.Error())
		}
	}
	detail := fmt.Sprintf("primary=%s replication=%t", primary.Kind(), replicate != nil)
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
	running, _ := d.cli.ContainerList(ctx, container.ListOptions{
		Filters: filters.NewArgs(filters.Arg("label", labelManaged+"=true")),
	})
	out.RunningServers = int32(len(running))
	return out, nil
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

func (d *DockerRuntime) Remove(ctx context.Context, serverID string, deleteData bool) error {
	d.stopMonitor(serverID)
	name := containerName(serverID)
	_ = d.cli.ContainerRemove(ctx, name, container.RemoveOptions{Force: true})
	if deleteData {
		_ = os.RemoveAll(d.localDir(serverID))
	}
	d.mu.Lock()
	delete(d.specs, serverID)
	d.mu.Unlock()
	d.forgetSpec(serverID)
	return nil
}

func (d *DockerRuntime) Install(ctx context.Context, req *agentpb.InstallServerRequest, emit func(*agentpb.InstallEvent) error) error {
	dataPath := d.containerDataTarget(req.ServerId)
	// Ensure the host data dir exists even if Create was not called.
	if err := os.MkdirAll(d.localDir(req.ServerId), 0o755); err != nil {
		return d.fail(emit, "create data dir: "+err.Error())
	}

	if err := d.pullImage(ctx, req.Image, func(line string) { _ = emit(logLine(line)) }); err != nil {
		return d.fail(emit, "pull image: "+err.Error())
	}

	// One-shot install container: run the install script against the data dir.
	cfg := &container.Config{
		Image:      req.Image,
		Entrypoint: d.shellEntrypoint(),
		Cmd:        []string{req.InstallScript},
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
	installName := containerName(req.ServerId) + "_install"
	_ = d.cli.ContainerRemove(ctx, installName, container.RemoveOptions{Force: true})
	created, err := d.cli.ContainerCreate(ctx, cfg, host, nil, nil, installName)
	if err != nil {
		return d.fail(emit, "create install container: "+err.Error())
	}
	defer func() {
		_ = d.cli.ContainerRemove(context.Background(), created.ID, container.RemoveOptions{Force: true})
	}()

	if err := d.cli.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		return d.fail(emit, "start install container: "+err.Error())
	}
	_ = emit(&agentpb.InstallEvent{Event: &agentpb.InstallEvent_Progress{Progress: 10}})

	// Stream install logs, watching for a SteamCMD app failure. SteamCMD exits 0
	// even when an app fails to download (e.g. "Missing configuration", "No
	// subscription", "Not for anonymous users"), so the exit code alone would let
	// a broken install pass as success — leaving a silent, empty server.
	var steamErr string
	if err := d.streamLogs(ctx, created.ID, "all", func(_ string, text string) error {
		// A later success supersedes an earlier transient failure — SteamCMD's
		// "Missing configuration" two-step prints a failure on the first app_update
		// pass and succeeds on the second.
		switch {
		case steamInstallSuccessRE.MatchString(text):
			steamErr = ""
		case steamInstallFailureRE.MatchString(text):
			steamErr = strings.TrimSpace(text)
		}
		return emit(logLine(text))
	}); err != nil && ctx.Err() == nil {
		return d.fail(emit, "stream install logs: "+err.Error())
	}

	// Wait for exit and check the code.
	statusCh, errCh := d.cli.ContainerWait(ctx, created.ID, container.WaitConditionNotRunning)
	select {
	case werr := <-errCh:
		if werr != nil {
			return d.fail(emit, "wait install: "+werr.Error())
		}
	case st := <-statusCh:
		if st.StatusCode != 0 {
			return d.fail(emit, fmt.Sprintf("install exited with code %d", st.StatusCode))
		}
	case <-ctx.Done():
		return ctx.Err()
	}
	// SteamCMD returned 0 but reported an app failure in its output — fail loudly.
	if steamErr != "" {
		return d.fail(emit, "install failed: "+steamErr)
	}
	_ = emit(&agentpb.InstallEvent{Event: &agentpb.InstallEvent_Progress{Progress: 100}})
	return emit(&agentpb.InstallEvent{Event: &agentpb.InstallEvent_Completed{Completed: true}})
}

// steamInstallFailureRE matches the SteamCMD lines that signal an app failed to
// install despite SteamCMD's process exiting 0. steamInstallSuccessRE matches the
// success line, which clears a prior (transient) failure — see the two-step quirk.
var (
	steamInstallFailureRE = regexp.MustCompile(`(?i)(ERROR! Failed to install app|Failed to install app .* \(|No subscription|Not for anonymous)`)
	steamInstallSuccessRE = regexp.MustCompile(`(?i)Success! App .* fully installed`)
)

func (d *DockerRuntime) Power(ctx context.Context, serverID string, action agentpb.PowerAction) (agentpb.ServerState, error) {
	switch action {
	case agentpb.PowerAction_POWER_ACTION_START:
		if err := d.ensureAndStart(ctx, serverID); err != nil {
			return agentpb.ServerState_SERVER_STATE_UNSPECIFIED, err
		}
		// Launch the crash watchdog; readiness (and thus running) is async, so
		// report STARTING and let the watchdog flip to RUNNING on ready_regex.
		d.startMonitor(serverID)
		return agentpb.ServerState_SERVER_STATE_STARTING, nil
	case agentpb.PowerAction_POWER_ACTION_RESTART:
		// Mark the in-flight monitor down first so the stop isn't read as a crash.
		d.markExpectedDown(serverID)
		_ = d.stop(ctx, serverID)
		if err := d.ensureAndStart(ctx, serverID); err != nil {
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
		_ = d.cli.ContainerKill(ctx, containerName(serverID), "SIGKILL")
		return agentpb.ServerState_SERVER_STATE_OFFLINE, nil
	default:
		return agentpb.ServerState_SERVER_STATE_UNSPECIFIED, fmt.Errorf("docker: unknown power action %v", action)
	}
}

func (d *DockerRuntime) ensureAndStart(ctx context.Context, serverID string) error {
	if err := d.ensureContainer(ctx, serverID); err != nil {
		return err
	}
	return d.cli.ContainerStart(ctx, containerName(serverID), container.StartOptions{})
}

// ensureContainer makes sure a runnable container exists for the server. A
// running container is kept as-is; a non-running one (created/exited/crashed) is
// removed and recreated so it starts clean and from the current image — data
// lives on the host bind mount, so nothing is lost, and this lets an image
// rebuild take effect on the next start.
func (d *DockerRuntime) ensureContainer(ctx context.Context, serverID string) error {
	name := containerName(serverID)
	if info, err := d.cli.ContainerInspect(ctx, name); err == nil {
		if info.State != nil && info.State.Running {
			return nil // already running
		}
		_ = d.cli.ContainerRemove(ctx, name, container.RemoveOptions{Force: true})
	}
	spec, ok := d.getSpec(serverID)
	if !ok {
		return fmt.Errorf("docker: no spec for server %q (call CreateServer first)", serverID)
	}
	return d.createRuntimeContainer(ctx, spec)
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
	timeout := 30
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
	return d.cli.ContainerStop(ctx, name, opts)
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
	if st, ok := d.monitorState(serverID); ok {
		state = st
	} else if insp, err := d.cli.ContainerInspect(ctx, containerName(serverID)); err != nil {
		// No container → treat as offline.
		return &agentpb.ServerStatus{ServerId: serverID, State: agentpb.ServerState_SERVER_STATE_OFFLINE}, nil
	} else {
		state = mapState(insp.State)
	}
	status := &agentpb.ServerStatus{ServerId: serverID, State: state}
	// Attach the online-player count so the reconciler can surface it fleet-wide
	// without an open stats stream (TTL-cached, so this poll rarely hits the game).
	if state == agentpb.ServerState_SERVER_STATE_RUNNING {
		if pl, mx, known := d.sampledPlayers(ctx, serverID); known {
			status.LastStats = &agentpb.ResourceStats{ServerId: serverID, Players: pl, MaxPlayers: mx, PlayersKnown: known}
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
		return "", fmt.Errorf("docker: path %q escapes %s", p, root)
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
		return nil, fmt.Errorf("docker: list %s: %w", p, err)
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

// ReadFile returns the contents of a single file in the volume, capped at
// maxBytes. It reports the file's true size, whether the returned bytes were
// truncated, and whether the content looks binary (contains a NUL byte).
func (d *DockerRuntime) ReadFile(ctx context.Context, serverID, p string, maxBytes int64) ([]byte, int64, bool, bool, error) {
	fp, err := d.safePath(p)
	if err != nil {
		return nil, 0, false, false, err
	}
	if maxBytes <= 0 {
		maxBytes = 1 << 20 // 1 MiB default
	}
	host := d.localOf(serverID, fp)
	st, err := os.Stat(host)
	if err != nil {
		return nil, 0, false, false, fmt.Errorf("docker: %s not found", p)
	}
	if st.IsDir() {
		return nil, 0, false, false, fmt.Errorf("docker: %s is a directory", p)
	}
	f, err := os.Open(host)
	if err != nil {
		return nil, 0, false, false, fmt.Errorf("docker: open %s: %w", p, err)
	}
	defer f.Close()
	buf, err := io.ReadAll(io.LimitReader(f, maxBytes))
	if err != nil {
		return nil, 0, false, false, fmt.Errorf("docker: read file: %w", err)
	}
	size := st.Size()
	truncated := size > int64(len(buf))
	binary := bytes.IndexByte(buf, 0) >= 0
	return buf, size, truncated, binary, nil
}

// DownloadFile streams a single file's raw bytes to w (no zip wrapper).
func (d *DockerRuntime) DownloadFile(_ context.Context, serverID, p string, w io.Writer) error {
	fp, err := d.safePath(p)
	if err != nil {
		return err
	}
	host := d.localOf(serverID, fp)
	st, err := os.Stat(host)
	if err != nil {
		return fmt.Errorf("docker: %s not found", p)
	}
	if st.IsDir() {
		return fmt.Errorf("docker: %s is a directory", p)
	}
	f, err := os.Open(host)
	if err != nil {
		return fmt.Errorf("docker: open %s: %w", p, err)
	}
	defer f.Close()
	_, err = io.Copy(w, f)
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
			return fmt.Errorf("docker: zip %s: %w", p, err)
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
		return fmt.Errorf("docker: mkdir %s: %w", p, err)
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
		return fmt.Errorf("docker: dir for %s: %w", p, err)
	}
	if err := os.WriteFile(host, content, 0o644); err != nil {
		return fmt.Errorf("docker: write %s: %w", p, err)
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
			return fmt.Errorf("docker: delete %s: %w", p, err)
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
		return fmt.Errorf("docker: cannot move the data root")
	}
	hostDst := d.localOf(serverID, dp)
	if err := os.MkdirAll(filepath.Dir(hostDst), 0o755); err != nil {
		return err
	}
	if err := os.Rename(d.localOf(serverID, s), hostDst); err != nil {
		return fmt.Errorf("docker: move %s → %s: %w", src, dst, err)
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
		return fmt.Errorf("docker: cannot copy the data root")
	}
	if dp == d.dataRoot() {
		return fmt.Errorf("docker: cannot copy onto the data root")
	}
	if err := copyTreeFS(d.localOf(serverID, s), d.localOf(serverID, dp)); err != nil {
		return fmt.Errorf("docker: copy %s → %s: %w", src, dst, err)
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
func (d *DockerRuntime) CreateBackup(_ context.Context, serverID, slug, name string) (*agentpb.BackupInfo, error) {
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
	go d.runBackup(serverID, slug, id)
	return cloneBackup(info), nil
}

// runBackup archives the data dir to the local store, then mirrors the finished
// archive off-node — each step a tracked state transition. Runs on its own
// long-lived context, independent of the request that triggered it.
func (d *DockerRuntime) runBackup(serverID, slug, id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()

	tmp, err := os.CreateTemp("", "kraken-backup-*.tar.gz")
	if err != nil {
		d.failBackup(serverID, id, err)
		return
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	defer tmp.Close()

	if err := d.archiveDataDir(serverID, tmp); err != nil {
		d.failBackup(serverID, id, err)
		return
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
		return
	}
	d.setReplication(serverID, id, agentpb.ReplicationState_REPLICATION_STATE_DONE)
}

// archiveDataDir tar+gzips a server's data dir into w.
func (d *DockerRuntime) archiveDataDir(serverID string, w io.Writer) error {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	root := d.localDir(serverID)
	walkErr := filepath.WalkDir(root, func(fp string, e os.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		rel, rerr := filepath.Rel(root, fp)
		if rerr != nil || rel == "." {
			return rerr
		}
		info, ierr := e.Info()
		if ierr != nil {
			return ierr
		}
		hdr, herr := tar.FileInfoHeader(info, "")
		if herr != nil {
			return herr
		}
		hdr.Name = filepath.ToSlash(rel)
		if e.IsDir() {
			hdr.Name += "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if e.IsDir() {
			return nil
		}
		f, oerr := os.Open(fp)
		if oerr != nil {
			return oerr
		}
		_, cerr := io.Copy(tw, f)
		f.Close()
		return cerr
	})
	if walkErr != nil {
		return fmt.Errorf("docker: archive data dir: %w", walkErr)
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

// ---- async backup job tracker (guarded by bjMu) ----

func backupJobKey(serverID, id string) string { return serverID + "/" + id }

func cloneBackup(b *agentpb.BackupInfo) *agentpb.BackupInfo {
	return &agentpb.BackupInfo{
		Id: b.Id, Name: b.Name, Size: b.Size, CreatedUnixMs: b.CreatedUnixMs,
		State: b.State, Replication: b.Replication,
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
	d.updateBackupJob(serverID, id, func(b *agentpb.BackupInfo) {
		b.State = agentpb.BackupState_BACKUP_STATE_FAILED
		if b.Replication == agentpb.ReplicationState_REPLICATION_STATE_PENDING {
			b.Replication = agentpb.ReplicationState_REPLICATION_STATE_UNSPECIFIED
		}
	})
}

func (d *DockerRuntime) forgetBackupJob(serverID, id string) {
	d.bjMu.Lock()
	defer d.bjMu.Unlock()
	delete(d.backupJobs, backupJobKey(serverID, id))
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
			// replication state (and any terminal archive state).
			onDisk.Replication = job.Replication
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

func (d *DockerRuntime) RestoreBackup(ctx context.Context, serverID, slug, id string) error {
	root := d.localDir(serverID)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	r, err := d.backupTargetFor(slug).Open(ctx, serverID, id)
	if err != nil {
		return fmt.Errorf("docker: open backup: %w", err)
	}
	defer r.Close()
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("docker: gunzip backup: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("docker: read backup: %w", err)
		}
		// Reject archive entries with path traversal or absolute-path prefixes
		// BEFORE joining them into a filesystem path — protects against Zip Slip
		// even if the downstream withinHostDir() check ever regresses. The check
		// below is the real second-line defense; this one is the CodeQL-visible
		// first line.
		if strings.Contains(hdr.Name, "..") || strings.HasPrefix(hdr.Name, "/") || strings.HasPrefix(hdr.Name, `\`) {
			return fmt.Errorf("docker: backup entry %q has illegal path", hdr.Name)
		}
		dest := filepath.Join(root, filepath.FromSlash(hdr.Name))
		if !d.withinHostDir(serverID, dest) {
			return fmt.Errorf("docker: backup entry %q escapes data dir", hdr.Name)
		}
		if hdr.FileInfo().IsDir() {
			if err := os.MkdirAll(dest, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		out, oerr := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
		if oerr != nil {
			return oerr
		}
		if _, cerr := io.Copy(out, tr); cerr != nil {
			out.Close()
			return cerr
		}
		out.Close()
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
	return err
}

// ---- helpers ----

func (d *DockerRuntime) fail(emit func(*agentpb.InstallEvent) error, msg string) error {
	_ = emit(&agentpb.InstallEvent{Event: &agentpb.InstallEvent_Failed{Failed: msg}})
	return fmt.Errorf("docker install: %s", msg)
}

func (d *DockerRuntime) pullImage(ctx context.Context, ref string, log func(string)) error {
	// A locally-present image is used as-is — this supports images built on the
	// host (e.g. ghcr.io/briggleman/kraken-steam-win, steam-base) that live in no registry.
	if _, err := d.cli.ImageInspect(ctx, ref); err == nil {
		log("Using local image " + ref)
		return nil
	}
	log("Pulling image " + ref)
	rc, err := d.cli.ImagePull(ctx, ref, image.PullOptions{})
	if err != nil {
		return err
	}
	defer rc.Close()
	if _, err = io.Copy(io.Discard, rc); err != nil { // drain progress stream
		return err
	}
	log("Image ready: " + ref)
	return nil
}

// streamLogs streams a container's logs to fn until the stream ends (used for
// the bounded install phase). demux is used for the unbounded console stream.
func (d *DockerRuntime) streamLogs(ctx context.Context, id, tail string, fn func(stream, text string) error) error {
	reader, err := d.cli.ContainerLogs(ctx, id, container.LogsOptions{
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
