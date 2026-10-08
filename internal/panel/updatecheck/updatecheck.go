// Package updatecheck is the Panel's Steam build check (#392): for each server,
// the build its install tree holds — the buildid in the data dir's
// steamapps/appmanifest_<app>.acf — against the build Steam has on the branch
// it follows, which the Agent asks SteamCMD for (GetAppBuilds). The result is
// written to the server row (store.ServerBuild) and read back by the API.
//
// The Checker runs a fleet-wide pass on a timer and on demand, and checks one
// server on demand. It knows nothing of HTTP, so the start path can ask it
// whether a server is current before deciding to run the update pass at all.
package updatecheck

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/briggleman/kraken/internal/panel/cluster"
	"github.com/briggleman/kraken/internal/panel/store"
	"github.com/briggleman/kraken/internal/shared/agentpb"
	"github.com/briggleman/kraken/internal/shared/powerbudget"
	"github.com/briggleman/kraken/internal/shared/spec"
	"github.com/briggleman/kraken/internal/shared/steam"
)

// The statuses a server's build check reports.
const (
	// StatusCurrent: the installed build is the branch's current one.
	StatusCurrent = "current"
	// StatusAvailable: Steam has a newer build than the one installed.
	StatusAvailable = "available"
	// StatusUnknown: the check has not run, or could not compare the two
	// builds; Result.Error says why when it ran.
	StatusUnknown = "unknown"
	// StatusUnsupported: the spec has no build to check — not a Steam
	// install, or one that opted out with update_check method none.
	StatusUnsupported = "unsupported"
)

// Result is a server's build check as the API reports it.
type Result struct {
	Status           string     `json:"status"`
	InstalledBuild   string     `json:"installed_build,omitempty"`
	AvailableBuild   string     `json:"available_build,omitempty"`
	AvailableBuildAt *time.Time `json:"available_build_at,omitempty"`
	CheckedAt        *time.Time `json:"checked_at,omitempty"`
	Error            string     `json:"error,omitempty"`
	// AgentPredates is set by a check that ran when the node's Agent answered
	// GetAppBuilds with Unimplemented. The status is still unknown, but a
	// caller deciding what a start does treats it like unsupported: the check
	// cannot run there at all, which is not the same as a check that failed.
	// Not part of the API: it is only known for a check just run.
	AgentPredates bool `json:"-"`
}

// agentPredatesError is the builds error of a group whose last answer was an
// Agent without GetAppBuilds.
type agentPredatesError struct{ node string }

func (e *agentPredatesError) Error() string {
	return "the agent on node " + e.node + " predates the build check; update it from the node's page"
}

// Evaluate derives a server's build-check result from what its row recorded
// and what its spec says now. The status is worked out at read time rather
// than stored, so a spec edit — a method none added, an app id changed — is
// reflected on the next read instead of on the next check. sp may be nil (a
// spec that can no longer be loaded), which reads as unknown.
func Evaluate(sv *store.Server, sp *spec.Spec) Result {
	if sp == nil {
		return Result{Status: StatusUnknown, Error: "the server's game spec could not be loaded"}
	}
	if !sp.UpdateCheckFor(sv.Kind).Steam() {
		return Result{Status: StatusUnsupported}
	}
	return fromBuild(sv.Build)
}

// fromBuild is Evaluate for a server whose spec has a build to check.
func fromBuild(b store.ServerBuild) Result {
	r := Result{
		InstalledBuild:   b.InstalledBuild,
		AvailableBuild:   b.AvailableBuild,
		AvailableBuildAt: b.AvailableBuildAt,
		CheckedAt:        b.CheckedAt,
		Error:            b.CheckError,
	}
	switch {
	case b.CheckError != "", b.CheckedAt == nil, b.InstalledBuild == "", b.AvailableBuild == "":
		r.Status = StatusUnknown
	case b.InstalledBuild == b.AvailableBuild:
		r.Status = StatusCurrent
	default:
		r.Status = StatusAvailable
	}
	return r
}

// Store is the slice of the Panel's store the Checker reads and writes.
type Store interface {
	GetServer(ctx context.Context, id string) (*store.Server, error)
	ListServers(ctx context.Context) ([]*store.Server, error)
	GetSpec(ctx context.Context, id string) (*spec.Spec, error)
	GetNode(ctx context.Context, id string) (*cluster.Node, error)
	UpdateServerBuild(ctx context.Context, id string, b store.ServerBuild) error
}

// Clients hands out an Agent client for a node's dial target — the Panel's
// node pool.
type Clients interface {
	Client(target string) (agentpb.NodeServiceClient, error)
}

// LiveFunc reports nil when a node's Agent can be reached, and why not
// otherwise — the API's ensureNodeLive, which re-probes a node that reads
// offline rather than taking a stale status at its word.
type LiveFunc func(ctx context.Context, n *cluster.Node) error

// Timeouts for the two Agent calls a check makes.
var (
	// appBuildsTimeout bounds one GetAppBuilds call: the Agent's own worst
	// case — an image refresh that waits up to the start pull budget, then a
	// SteamCMD session it caps at three minutes (a Hyper-V steam-win boot and
	// SteamCMD's self-update both fit inside that) — plus the Panel's usual
	// margin. Shorter, and the Panel would give up on a session the Agent is
	// still running and would have answered.
	appBuildsTimeout = 3*time.Minute + powerbudget.StartPullBudget + powerbudget.DeadlineMargin
	// manifestTimeout bounds one manifest read: a few KB off the node's disk.
	manifestTimeout = 30 * time.Second
)

// ServerCheckTimeout is long enough for CheckServer to finish against an
// Agent at its slowest: the GetAppBuilds call, then the manifest read. A
// caller with an operator waiting caps the check here, and no lower.
var ServerCheckTimeout = appBuildsTimeout + manifestTimeout

// livenessTimeout bounds one liveness probe. The probe runs detached from
// the caller's cancellation (see liveness), so it needs a bound of its own.
const livenessTimeout = 15 * time.Second

// maxManifestBytes caps a manifest read. A real one is a few KB; anything
// past this is not a manifest worth parsing.
const maxManifestBytes = 256 << 10

// fleetParallelism is how many (image, platform) groups a fleet pass checks
// at once. Each is one SteamCMD session on some node, so this bounds how many
// of those the Panel asks for together.
const fleetParallelism = 4

// Checker runs build checks. The zero value is not usable; see New.
type Checker struct {
	store   Store
	clients Clients
	live    LiveFunc
	logger  *slog.Logger
	now     func() time.Time

	mu     sync.Mutex
	flight *fleetFlight
}

// fleetFlight is a CheckAll in progress, which a second caller joins.
type fleetFlight struct {
	done chan struct{}
	err  error
}

// New returns a Checker over the Panel's store, node pool and liveness probe.
func New(st Store, clients Clients, live LiveFunc, logger *slog.Logger) *Checker {
	return &Checker{store: st, clients: clients, live: live, logger: logger, now: time.Now}
}

// member is one server in a check: the row, the check its spec resolves to,
// and the node it lives on.
type member struct {
	sv   *store.Server
	uc   spec.UpdateCheck
	node *cluster.Node
}

// group is the servers one GetAppBuilds call answers for: the same image on
// the same platform, so one SteamCMD session can be asked about all of their
// apps at once.
type group struct {
	image   string
	kind    spec.PlatformKind
	members []member
}

// ErrNotPlaced is CheckServer's answer for a server that is on no node — a
// retired one — and so has no data dir to read a manifest from.
var ErrNotPlaced = errors.New("the server is on no node")

// CheckServer checks one server now, writes what it found to the row, and
// returns the result. A server whose spec has no build to check returns
// unsupported and writes nothing.
//
// It does not look at the server's state. The caller decides whether the
// manifest can be read: the API refuses a server that is installing, whose
// manifest may be half-written, while the start path calls this on a server
// it has itself put in `installing` and stopped.
func (c *Checker) CheckServer(ctx context.Context, serverID string) (Result, error) {
	sv, err := c.store.GetServer(ctx, serverID)
	if err != nil {
		return Result{}, err
	}
	sp, err := c.store.GetSpec(ctx, sv.SpecID)
	if err != nil {
		return Result{}, fmt.Errorf("load spec: %w", err)
	}
	uc := sp.UpdateCheckFor(sv.Kind)
	if !uc.Steam() {
		return Evaluate(sv, sp), nil
	}
	if sv.NodeID == "" {
		return Result{}, ErrNotPlaced
	}
	node, err := c.store.GetNode(ctx, sv.NodeID)
	if err != nil {
		return Result{}, fmt.Errorf("load node: %w", err)
	}
	image, _ := sp.ImageFor(sv.Kind)
	res := c.checkGroup(ctx, group{image: image, kind: sv.Kind, members: []member{{sv: sv, uc: uc, node: node}}}, newLiveness(c.live))
	return res[sv.ID], nil
}

// Running reports whether a fleet pass is in progress.
func (c *Checker) Running() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.flight != nil
}

// CheckAll checks every server that can be checked, and records each
// outcome on its row. A pass already running is joined rather than doubled:
// the second caller waits for it and gets its error, so the daily timer and
// an operator's "check everything" never run two passes over one fleet.
//
// Servers are grouped by (image, platform) and each group's builds come from
// ONE GetAppBuilds call — the available build is per app and branch, not per
// server — on a node that already hosts one of them, so the image is there.
// Then each server's manifest is read from its own node. Skipped: a server
// that is installing (its manifest may be mid-write), restoring (a restore
// can rewrite the tree), retiring or retired (it is leaving or has left its
// node), and one whose spec has no build to check.
//
// The returned error is about the pass itself (the server list could not be
// read); what happened to each server is on its row.
func (c *Checker) CheckAll(ctx context.Context) error {
	c.mu.Lock()
	if f := c.flight; f != nil {
		c.mu.Unlock()
		select {
		case <-f.done:
			return f.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f := &fleetFlight{done: make(chan struct{})}
	c.flight = f
	c.mu.Unlock()

	f.err = c.checkAll(ctx)

	c.mu.Lock()
	c.flight = nil
	c.mu.Unlock()
	close(f.done)
	return f.err
}

func (c *Checker) checkAll(ctx context.Context) error {
	began := c.now()
	servers, err := c.store.ListServers(ctx)
	if err != nil {
		return fmt.Errorf("list servers: %w", err)
	}
	specs := map[string]*spec.Spec{}
	nodes := map[string]*cluster.Node{}
	groups := map[string]*group{}
	var order []string // group keys in first-seen order, so passes are repeatable
	checked := 0
	for _, sv := range servers {
		switch sv.State {
		case store.StateInstalling, store.StateRestoring, store.StateRetiring, store.StateRetired:
			continue
		}
		if sv.NodeID == "" {
			continue
		}
		sp, ok := specs[sv.SpecID]
		if !ok {
			sp, err = c.store.GetSpec(ctx, sv.SpecID)
			if err != nil {
				c.logger.Warn("build check: skipping a server whose spec cannot be loaded", "server", sv.ID, "spec", sv.SpecID, "err", err)
				sp = nil
			}
			specs[sv.SpecID] = sp
		}
		if sp == nil {
			continue
		}
		uc := sp.UpdateCheckFor(sv.Kind)
		if !uc.Steam() {
			continue
		}
		node, ok := nodes[sv.NodeID]
		if !ok {
			node, err = c.store.GetNode(ctx, sv.NodeID)
			if err != nil {
				c.logger.Warn("build check: skipping a server whose node cannot be loaded", "server", sv.ID, "node", sv.NodeID, "err", err)
				node = nil
			}
			nodes[sv.NodeID] = node
		}
		if node == nil {
			continue
		}
		image, _ := sp.ImageFor(sv.Kind)
		key := image + "\x00" + string(sv.Kind)
		g, ok := groups[key]
		if !ok {
			g = &group{image: image, kind: sv.Kind}
			groups[key] = g
			order = append(order, key)
		}
		g.members = append(g.members, member{sv: sv, uc: uc, node: node})
		checked++
	}

	live := newLiveness(c.live)
	var (
		wg      sync.WaitGroup
		sem     = make(chan struct{}, fleetParallelism)
		resMu   sync.Mutex
		results = map[string]int{}
	)
	for _, key := range order {
		g := groups[key]
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			for _, r := range c.checkGroup(ctx, *g, live) {
				resMu.Lock()
				results[r.Status]++
				resMu.Unlock()
			}
		}()
	}
	wg.Wait()
	c.logger.Info("build check: fleet pass done", "servers", checked, "groups", len(order),
		StatusCurrent, results[StatusCurrent], StatusAvailable, results[StatusAvailable],
		StatusUnknown, results[StatusUnknown], "took", c.now().Sub(began).Round(time.Millisecond).String())
	return ctx.Err()
}

// liveness memoizes the liveness probe for one pass, so a node that is down
// is probed once rather than once per server on it. Safe for the pass's
// concurrent groups; a probe of one node never waits on another's.
type liveness struct {
	probe LiveFunc
	mu    sync.Mutex
	seen  map[string]*liveProbe
}

type liveProbe struct {
	mu   sync.Mutex
	done bool
	err  error
}

func newLiveness(probe LiveFunc) *liveness {
	return &liveness{probe: probe, seen: map[string]*liveProbe{}}
}

// check probes n once per pass. The probe runs on a context detached from the
// caller's cancellation and bounded on its own: a pass whose context ends
// partway must not record "context canceled" as the node's answer — that
// would read as "node offline" for every later server on it, and the probe's
// own write would store the node offline. A caller whose context has already
// ended is told so, and nothing is cached for it.
func (l *liveness) check(ctx context.Context, n *cluster.Node) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	l.mu.Lock()
	p, ok := l.seen[n.ID]
	if !ok {
		p = &liveProbe{}
		l.seen[n.ID] = p
	}
	l.mu.Unlock()

	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.done {
		pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), livenessTimeout)
		p.err = l.probe(pctx, n)
		cancel()
		p.done = true
	}
	return p.err
}

// timedOut reports whether err is a deadline or a cancellation, from the
// context or from the gRPC status that carries one.
func timedOut(err error) bool {
	switch status.Code(err) {
	case codes.DeadlineExceeded, codes.Canceled:
		return true
	}
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}

// errOutOfTime is what a check records when its caller's time ran out before
// it could finish. The Agent's SteamCMD session is not stopped by that, and a
// check asked again shortly finds its answer.
var errOutOfTime = errors.New("the check ran out of time before it finished; SteamCMD may still be running on the node — check again shortly")

// checkGroup asks for the group's available builds once, reads each member's
// manifest, writes each member's row, and returns the results by server id.
func (c *Checker) checkGroup(ctx context.Context, g group, live *liveness) map[string]Result {
	builds, buildsErr := c.availableBuilds(ctx, g, live)
	out := make(map[string]Result, len(g.members))
	for _, m := range g.members {
		b := m.sv.Build // what is not learned now keeps its last value
		now := c.now().UTC()
		b.CheckedAt = &now
		var problems []string

		switch {
		case ctx.Err() != nil:
			// Out of time before the manifest could be read. Said once: when
			// the builds call is what ran out, its own error already says so.
			if buildsErr == nil {
				problems = append(problems, errOutOfTime.Error())
			}
		default:
			if installed, err := c.installedBuild(ctx, m, live); err != nil {
				problems = append(problems, err.Error())
			} else {
				b.InstalledBuild = installed
			}
		}
		if buildsErr != nil {
			problems = append(problems, buildsErr.Error())
		} else if ab, ok := builds[appKey(m.uc)]; !ok {
			problems = append(problems, "steam did not report a build for app "+appIDString(m.uc)+" on branch "+m.uc.Branch)
		} else if ab.GetError() != "" {
			problems = append(problems, "steam: "+ab.GetError())
		} else if ab.GetBuildId() == "" {
			problems = append(problems, "steam reported no build id for app "+appIDString(m.uc)+" on branch "+m.uc.Branch)
		} else {
			b.AvailableBuild = ab.GetBuildId()
			b.AvailableBuildAt = nil
			if t := ab.GetTimeUpdated(); t > 0 {
				at := time.Unix(t, 0).UTC()
				b.AvailableBuildAt = &at
			}
		}
		b.CheckError = strings.Join(problems, "; ")

		// Recorded on a context of its own: a check cut short by its caller's
		// deadline is still a check, and "it timed out" is the result to keep.
		wctx, wcancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		err := c.store.UpdateServerBuild(wctx, m.sv.ID, b)
		wcancel()
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			// A server deleted mid-check is nothing to report; anything else
			// leaves the row on its previous check, which the result says.
			c.logger.Warn("build check: could not record the result", "server", m.sv.ID, "err", err)
		}
		r := fromBuild(b)
		var old *agentPredatesError
		r.AgentPredates = errors.As(buildsErr, &old)
		out[m.sv.ID] = r
		if r.Status == StatusUnknown {
			c.logger.Info("build check: could not compare builds", "server", m.sv.ID, "name", m.sv.Name, "reason", b.CheckError)
		}
	}
	return out
}

// availableBuilds asks one of the group's nodes for the current build of every
// app and branch in the group, in one call. It tries the group's nodes in
// turn, so one node that is down — or whose Agent predates the RPC, partway
// through a fleet upgrade — does not leave the whole group unknown.
func (c *Checker) availableBuilds(ctx context.Context, g group, live *liveness) (map[string]*agentpb.AppBuild, error) {
	req := &agentpb.GetAppBuildsRequest{Image: g.image, PlatformType: platformType(g.kind)}
	seenApp := map[string]bool{}
	var candidates []*cluster.Node
	seenNode := map[string]bool{}
	for _, m := range g.members {
		if k := appKey(m.uc); !seenApp[k] {
			seenApp[k] = true
			req.Apps = append(req.Apps, &agentpb.AppBuildQuery{AppId: appIDString(m.uc), Branch: m.uc.Branch})
		}
		if !seenNode[m.node.ID] {
			seenNode[m.node.ID] = true
			candidates = append(candidates, m.node)
		}
	}

	var last error
	for _, n := range candidates {
		if ctx.Err() != nil {
			return nil, errOutOfTime
		}
		if err := live.check(ctx, n); err != nil {
			last = fmt.Errorf("node %s is offline: %v", nodeLabel(n), err)
			continue
		}
		client, err := c.clients.Client(n.DialTarget())
		if err != nil {
			last = fmt.Errorf("connect to node %s: %v", nodeLabel(n), err)
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, appBuildsTimeout)
		resp, err := client.GetAppBuilds(cctx, req)
		cancel()
		if status.Code(err) == codes.Unimplemented {
			last = &agentPredatesError{node: nodeLabel(n)}
			continue
		}
		if err != nil && timedOut(err) {
			// The Agent's session runs on regardless, and answers the next
			// caller from the same SteamCMD run; the raw context error would
			// tell the operator nothing of that.
			last = fmt.Errorf("node %s did not answer the build check in time; SteamCMD may still be running there — check again shortly", nodeLabel(n))
			if ctx.Err() != nil {
				return nil, last
			}
			continue
		}
		if err != nil {
			last = fmt.Errorf("ask node %s for the current builds: %v", nodeLabel(n), status.Convert(err).Message())
			continue
		}
		out := make(map[string]*agentpb.AppBuild, len(resp.GetBuilds()))
		for _, b := range resp.GetBuilds() {
			out[b.GetAppId()+"@"+branchOrPublic(b.GetBranch())] = b
		}
		return out, nil
	}
	if last == nil {
		last = errors.New("no node to ask for the current builds")
	}
	return nil, last
}

// installedBuild reads the build out of the member's appmanifest, on its own
// node.
func (c *Checker) installedBuild(ctx context.Context, m member, live *liveness) (string, error) {
	if err := live.check(ctx, m.node); err != nil {
		return "", fmt.Errorf("node %s is offline, so the installed build could not be read: %v", nodeLabel(m.node), err)
	}
	client, err := c.clients.Client(m.node.DialTarget())
	if err != nil {
		return "", fmt.Errorf("connect to node %s: %v", nodeLabel(m.node), err)
	}
	build, err := ReadInstalledBuild(ctx, client, m.sv.ID, m.uc)
	if errors.Is(err, errManifestTimeout) {
		return "", fmt.Errorf("node %s did not return the manifest in time — check again shortly", nodeLabel(m.node))
	}
	return build, err
}

// errManifestTimeout marks a manifest read that ran out of time, so the check
// records "try again" rather than a raw context error.
var errManifestTimeout = errors.New("manifest read timed out")

// ManifestPath is where SteamCMD keeps an app's manifest, relative to the
// server's data dir: the install scripts force_install_dir the data dir
// itself, so steamapps/ sits at its top.
func ManifestPath(appID int) string {
	return fmt.Sprintf("steamapps/appmanifest_%d.acf", appID)
}

// ReadInstalledBuild reads the build id out of a server's appmanifest through
// the Agent's ReadFile. It is the half of a check that needs no SteamCMD, and
// what the Panel uses after an install pass to record the build it just
// installed.
func ReadInstalledBuild(ctx context.Context, client agentpb.NodeServiceClient, serverID string, uc spec.UpdateCheck) (string, error) {
	path := ManifestPath(uc.AppID)
	rctx, cancel := context.WithTimeout(ctx, manifestTimeout)
	defer cancel()
	resp, err := client.ReadFile(rctx, &agentpb.ReadFileRequest{ServerId: serverID, Path: path, MaxBytes: maxManifestBytes})
	if status.Code(err) == codes.NotFound {
		return "", fmt.Errorf("no %s in the data dir — the install has not written one", path)
	}
	if err != nil && timedOut(err) {
		return "", fmt.Errorf("read %s: %w", path, errManifestTimeout)
	}
	if err != nil {
		return "", fmt.Errorf("read %s: %v", path, status.Convert(err).Message())
	}
	if resp.GetTruncated() {
		return "", fmt.Errorf("%s is larger than %d KB, which no manifest is", path, maxManifestBytes>>10)
	}
	m, err := steam.ParseAppManifest(resp.GetContent())
	if err != nil {
		return "", fmt.Errorf("%s: %v", path, err)
	}
	if m.AppID != "" && m.AppID != appIDString(uc) {
		return "", fmt.Errorf("%s is for app %s", path, m.AppID)
	}
	return m.BuildID, nil
}

// RecordInstalled records the build an install pass just left on disk as both
// the installed and the available build: the pass ran app_update, which pulled
// the branch's current build, so the check comes free with it. A spec with no
// build to check is left alone. The error is for the caller to log; the row is
// untouched when the manifest cannot be read.
func (c *Checker) RecordInstalled(ctx context.Context, sv *store.Server, sp *spec.Spec, client agentpb.NodeServiceClient) (string, error) {
	uc := sp.UpdateCheckFor(sv.Kind)
	if !uc.Steam() {
		return "", nil
	}
	build, err := ReadInstalledBuild(ctx, client, sv.ID, uc)
	if err != nil {
		return "", err
	}
	// The row's own copy, not the caller's: the caller's was read before the
	// pass, and Steam's timestamp for the build is only worth keeping if it is
	// still for this build.
	prev := sv.Build
	if fresh, ferr := c.store.GetServer(ctx, sv.ID); ferr == nil {
		prev = fresh.Build
	}
	now := c.now().UTC()
	b := store.ServerBuild{InstalledBuild: build, AvailableBuild: build, CheckedAt: &now}
	if prev.AvailableBuild == build {
		b.AvailableBuildAt = prev.AvailableBuildAt
	}
	if err := c.store.UpdateServerBuild(ctx, sv.ID, b); err != nil {
		return "", err
	}
	return build, nil
}

// Run is the timer: one fleet pass after first, then one every interval, each
// wait varied by up to ±10% so a fleet of Panels — or one Panel restarted on
// the hour — does not ask Steam at the same instant every day. It returns when
// ctx ends.
func (c *Checker) Run(ctx context.Context, first, interval time.Duration) {
	wait := jitter(first)
	for {
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		if err := c.CheckAll(ctx); err != nil && ctx.Err() == nil {
			c.logger.Warn("build check: fleet pass failed", "err", err)
		}
		wait = jitter(interval)
	}
}

// jitter returns d varied uniformly by up to ±10%.
func jitter(d time.Duration) time.Duration {
	spread := int64(d) / 10
	if spread <= 0 {
		return d
	}
	return d + time.Duration(rand.Int64N(2*spread+1)-spread)
}

func platformType(kind spec.PlatformKind) string {
	if kind == spec.WindowsNative {
		return "windows"
	}
	return "linux" // linux-native, and linux-wine: the wine image is a Linux container
}

func appIDString(uc spec.UpdateCheck) string { return fmt.Sprint(uc.AppID) }

func appKey(uc spec.UpdateCheck) string { return appIDString(uc) + "@" + branchOrPublic(uc.Branch) }

func branchOrPublic(b string) string {
	if b == "" {
		return "public"
	}
	return b
}

func nodeLabel(n *cluster.Node) string {
	if n.Name != "" {
		return n.Name
	}
	return n.ID
}
