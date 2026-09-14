package agent

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// FakeRuntime is an in-memory Runtime that simulates installs, power actions,
// console output, and stats. It lets the Agent run end-to-end without Docker —
// used for local development and tests. It is concurrency-safe.
type FakeRuntime struct {
	nodeID      string
	os          string
	wineEnabled bool
	version     string
	binarySHA   string

	mu      sync.Mutex
	states  map[string]agentpb.ServerState
	configs map[string]map[string]string     // serverID → path → content
	backups map[string][]*agentpb.BackupInfo // serverID → backups
	files   map[string]map[string]*fakeFile  // serverID → logical path → entry (see tree)
}

// FakeOption customizes a FakeRuntime at construction time. It exists so the
// fixture can grow without breaking the call sites that don't care.
type FakeOption func(*FakeRuntime)

// WithFakeBinarySHA makes the fake report a self-reported agent binary hash in
// NodeInfo, the way an agent with a real self-updater does.
//
// The fake has no SelfUpdater, so Service.GetNodeInfo leaves BinarySha256 alone
// (it only fills it in when an updater is wired) — which is what makes reporting
// it from the runtime work, and what keeps a real updater authoritative if both
// are ever present. Without this, the Panel's artifact-identity branches (#93,
// #178, #186) are unreachable through the HTTP handler in a test.
func WithFakeBinarySHA(sha string) FakeOption {
	return func(f *FakeRuntime) { f.binarySHA = sha }
}

// NewFakeRuntime returns a fake runtime identifying as the given node.
func NewFakeRuntime(nodeID, os string, wineEnabled bool, version string, opts ...FakeOption) *FakeRuntime {
	f := &FakeRuntime{
		nodeID: nodeID, os: os, wineEnabled: wineEnabled, version: version,
		states: make(map[string]agentpb.ServerState),
	}
	for _, o := range opts {
		o(f)
	}
	return f
}

var _ Runtime = (*FakeRuntime)(nil)

func (f *FakeRuntime) setState(serverID string, st agentpb.ServerState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.states[serverID] = st
}

func (f *FakeRuntime) getState(serverID string) agentpb.ServerState {
	f.mu.Lock()
	defer f.mu.Unlock()
	if st, ok := f.states[serverID]; ok {
		return st
	}
	return agentpb.ServerState_SERVER_STATE_OFFLINE
}

func (f *FakeRuntime) NodeInfo(_ context.Context) (*agentpb.NodeInfo, error) {
	f.mu.Lock()
	running := 0
	for _, st := range f.states {
		if st == agentpb.ServerState_SERVER_STATE_RUNNING {
			running++
		}
	}
	f.mu.Unlock()
	return &agentpb.NodeInfo{
		NodeId:         f.nodeID,
		Os:             f.os,
		WineEnabled:    f.wineEnabled,
		AgentVersion:   f.version,
		BinarySha256:   f.binarySHA, // empty unless WithFakeBinarySHA was used
		TotalMemoryMb:  16384,
		RunningServers: int32(running),
		Host:           PrimaryIP(),
		HostAddresses:  CandidateIPs(),
		ExternalIp:     "203.0.113.10", // documentation IP; lets tests exercise external-IP adoption
		RuntimeStatus:  agentpb.RuntimeStatus_RUNTIME_STATUS_OK,
	}, nil
}

func (f *FakeRuntime) Create(_ context.Context, spec *agentpb.ServerSpec) error {
	f.setState(spec.ServerId, agentpb.ServerState_SERVER_STATE_OFFLINE)
	return nil
}

func (f *FakeRuntime) Remove(_ context.Context, serverID string, _ bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.states, serverID)
	delete(f.files, serverID)
	return nil
}

// ApplyConfig records the rendered files in memory (no real volume in the fake)
// and writes them into the fake data dir so they show up in the file listing.
func (f *FakeRuntime) ApplyConfig(_ context.Context, serverID string, files map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.configs == nil {
		f.configs = make(map[string]map[string]string)
	}
	f.configs[serverID] = files
	t := f.tree(serverID)
	for p, content := range files {
		if fp := fakePath(p); fp != fakeDataRoot {
			f.putFile(t, fp, []byte(content))
		}
	}
	return nil
}

// --- files ---------------------------------------------------------------
//
// The fake keeps a real (in-memory) data dir per server, seeded with a config
// file and a saves folder, so the file API behaves the way it does against a
// node: an upload appears in the listing, a delete removes it, a folder delete
// takes its contents. Before this the file ops were no-ops that always listed
// the same two entries, which made the Files tab impossible to exercise against
// the fake stack (#287).

const fakeDataRoot = "/data"

type fakeFile struct {
	entry *agentpb.FileEntry
	data  []byte
}

// fakePath maps any client path onto the logical data root the way the docker
// runtime's safePath does: "" and "." are the root, relative paths hang off it,
// and Windows separators are normalized.
func fakePath(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	if p == "" || p == "." {
		return fakeDataRoot
	}
	if !strings.HasPrefix(p, "/") {
		p = fakeDataRoot + "/" + p
	}
	p = path.Clean(p)
	if p != fakeDataRoot && !strings.HasPrefix(p, fakeDataRoot+"/") {
		p = fakeDataRoot + p
	}
	return p
}

// tree returns the server's file map, seeding it on first touch. Callers hold f.mu.
func (f *FakeRuntime) tree(serverID string) map[string]*fakeFile {
	if f.files == nil {
		f.files = make(map[string]map[string]*fakeFile)
	}
	t, ok := f.files[serverID]
	if !ok {
		t = make(map[string]*fakeFile)
		f.files[serverID] = t
		f.putFile(t, fakeDataRoot+"/server.cfg", []byte("# fake content for "+fakeDataRoot+"/server.cfg\nkey=value\n"))
		f.putDir(t, fakeDataRoot+"/saves")
		f.putFile(t, fakeDataRoot+"/saves/world.sav", make([]byte, 2048))
	}
	return t
}

// putDir records a directory and every ancestor below the root. Callers hold f.mu.
func (f *FakeRuntime) putDir(t map[string]*fakeFile, p string) {
	for cur := p; cur != fakeDataRoot && cur != "/" && cur != "."; cur = path.Dir(cur) {
		if _, ok := t[cur]; ok {
			continue
		}
		t[cur] = &fakeFile{entry: &agentpb.FileEntry{Name: path.Base(cur), Path: cur, IsDir: true, ModUnixMs: nowMs()}}
	}
}

// putFile records a file (creating its parents) and its bytes. Callers hold f.mu.
func (f *FakeRuntime) putFile(t map[string]*fakeFile, p string, data []byte) {
	f.putDir(t, path.Dir(p))
	t[p] = &fakeFile{
		entry: &agentpb.FileEntry{Name: path.Base(p), Path: p, Size: int64(len(data)), ModUnixMs: nowMs()},
		data:  data,
	}
}

func (f *FakeRuntime) ListFiles(_ context.Context, serverID string, p string) ([]*agentpb.FileEntry, error) {
	dir := fakePath(p)
	f.mu.Lock()
	defer f.mu.Unlock()
	t := f.tree(serverID)
	if dir != fakeDataRoot {
		d, ok := t[dir]
		if !ok || !d.entry.IsDir {
			return nil, fmt.Errorf("fake: %s: not a directory", p)
		}
	}
	var out []*agentpb.FileEntry
	for k, ff := range t {
		if path.Dir(k) == dir {
			out = append(out, ff.entry)
		}
	}
	// Folders first, then by name — the order an operator expects of a listing.
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func (f *FakeRuntime) ReadFile(_ context.Context, serverID string, p string, _ int64) ([]byte, int64, bool, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ff, ok := f.tree(serverID)[fakePath(p)]
	if !ok || ff.entry.IsDir {
		return nil, 0, false, false, fmt.Errorf("fake: %s: no such file", p)
	}
	return ff.data, int64(len(ff.data)), false, false, nil
}

func (f *FakeRuntime) DownloadFile(_ context.Context, serverID string, p string, w io.Writer) error {
	data, _, _, _, err := f.ReadFile(context.Background(), serverID, p, 0)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

// MovePath renames a file or a whole subtree; CopyPath duplicates one.
func (f *FakeRuntime) MovePath(_ context.Context, serverID string, src, dst string) error {
	return f.transplant(serverID, src, dst, true)
}

func (f *FakeRuntime) CopyPath(_ context.Context, serverID string, src, dst string) error {
	return f.transplant(serverID, src, dst, false)
}

func (f *FakeRuntime) transplant(serverID, src, dst string, move bool) error {
	s, d := fakePath(src), fakePath(dst)
	if s == fakeDataRoot || d == fakeDataRoot {
		return fmt.Errorf("fake: cannot move the data root")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	t := f.tree(serverID)
	if _, ok := t[s]; !ok {
		return fmt.Errorf("fake: %s: no such file or directory", src)
	}
	moved := make(map[string]*fakeFile)
	for k, ff := range t {
		if k != s && !strings.HasPrefix(k, s+"/") {
			continue
		}
		nk := d + strings.TrimPrefix(k, s)
		moved[nk] = &fakeFile{
			entry: &agentpb.FileEntry{Name: path.Base(nk), Path: nk, IsDir: ff.entry.IsDir, Size: ff.entry.Size, ModUnixMs: nowMs()},
			data:  append([]byte(nil), ff.data...),
		}
		if move {
			delete(t, k)
		}
	}
	f.putDir(t, path.Dir(d))
	for k, ff := range moved {
		t[k] = ff
	}
	return nil
}

func (f *FakeRuntime) ZipFiles(_ context.Context, _ string, paths []string, w io.Writer) error {
	zw := zip.NewWriter(w)
	for _, p := range paths {
		fw, err := zw.Create(strings.TrimPrefix(p, "/"))
		if err != nil {
			return err
		}
		_, _ = fw.Write([]byte("fake content for " + p + "\n"))
	}
	return zw.Close()
}

// CreateBackup ignores the backup globs: the fake stores no data dir, so there
// is nothing to filter. Tests that care about the resolved globs assert on the
// request the Panel sends.
func (f *FakeRuntime) CreateBackup(_ context.Context, serverID, _, name string, _, _ []string) (*agentpb.BackupInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.backups == nil {
		f.backups = make(map[string][]*agentpb.BackupInfo)
	}
	if name == "" {
		name = "backup"
	}
	b := &agentpb.BackupInfo{Id: fmt.Sprintf("%d__%s", nowMs(), name), Name: name, Size: 1024, CreatedUnixMs: nowMs()}
	f.backups[serverID] = append(f.backups[serverID], b)
	return b, nil
}

func (f *FakeRuntime) ListBackups(_ context.Context, serverID, _ string) ([]*agentpb.BackupInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.backups[serverID], nil
}

func (f *FakeRuntime) RestoreBackup(_ context.Context, _, _, _ string) error { return nil }

func (f *FakeRuntime) ApplyNodeConfig(_ context.Context, cfg *agentpb.NodeConfig, verify bool) (bool, string) {
	if cfg == nil {
		return true, "no config"
	}
	target := cfg.GetBackupTarget()
	if target == "" {
		target = "local"
	}
	// The verify flag is echoed so handler tests can assert which path set it.
	return true, fmt.Sprintf("fake: primary=%s replication=%t verify=%t", target,
		cfg.GetReplicateToSftp() || cfg.GetReplicateToSmb(), verify)
}

func (f *FakeRuntime) ReplicateBackups(_ context.Context, serverID, _ string) (int32, int32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return int32(len(f.backups[serverID])), 0, nil
}

func (f *FakeRuntime) DeleteBackup(_ context.Context, serverID, _, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	kept := f.backups[serverID][:0]
	for _, b := range f.backups[serverID] {
		if b.Id != id {
			kept = append(kept, b)
		}
	}
	f.backups[serverID] = kept
	return nil
}

func (f *FakeRuntime) MakeDir(_ context.Context, serverID string, p string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.putDir(f.tree(serverID), fakePath(p))
	return nil
}

func (f *FakeRuntime) WriteFile(_ context.Context, serverID string, p string, content []byte) error {
	fp := fakePath(p)
	if fp == fakeDataRoot {
		return fmt.Errorf("fake: cannot write the data root")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.putFile(f.tree(serverID), fp, append([]byte(nil), content...))
	return nil
}

// DeletePaths removes each path and, for a folder, everything under it. The
// data root itself is skipped, the way the docker runtime skips it.
func (f *FakeRuntime) DeletePaths(_ context.Context, serverID string, paths []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := f.tree(serverID)
	for _, p := range paths {
		fp := fakePath(p)
		if fp == fakeDataRoot {
			continue
		}
		if _, ok := t[fp]; !ok {
			return fmt.Errorf("fake: delete %s: no such file or directory", p)
		}
		for k := range t {
			if k == fp || strings.HasPrefix(k, fp+"/") {
				delete(t, k)
			}
		}
	}
	return nil
}

func (f *FakeRuntime) Install(ctx context.Context, req *agentpb.InstallServerRequest, emit func(*agentpb.InstallEvent) error) error {
	f.setState(req.ServerId, agentpb.ServerState_SERVER_STATE_INSTALLING)
	steps := []string{
		"Redirecting stderr to console",
		"[  0%] Connecting anonymously to Steam Public...",
		"[ 50%] Downloading update (depot)...",
		"[100%] Install of " + req.ServerId + " complete",
	}
	for i, line := range steps {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := emit(&agentpb.InstallEvent{Event: &agentpb.InstallEvent_LogLine{LogLine: line}}); err != nil {
			return err
		}
		if err := emit(&agentpb.InstallEvent{Event: &agentpb.InstallEvent_Progress{Progress: int32((i + 1) * 100 / len(steps))}}); err != nil {
			return err
		}
	}
	f.setState(req.ServerId, agentpb.ServerState_SERVER_STATE_OFFLINE)
	return emit(&agentpb.InstallEvent{Event: &agentpb.InstallEvent_Completed{Completed: true}})
}

func (f *FakeRuntime) Power(_ context.Context, serverID string, action agentpb.PowerAction) (agentpb.ServerState, error) {
	var st agentpb.ServerState
	switch action {
	case agentpb.PowerAction_POWER_ACTION_START, agentpb.PowerAction_POWER_ACTION_RESTART:
		st = agentpb.ServerState_SERVER_STATE_RUNNING
	case agentpb.PowerAction_POWER_ACTION_STOP, agentpb.PowerAction_POWER_ACTION_KILL:
		st = agentpb.ServerState_SERVER_STATE_OFFLINE
	default:
		return agentpb.ServerState_SERVER_STATE_UNSPECIFIED, fmt.Errorf("agent: unknown power action %v", action)
	}
	f.setState(serverID, st)
	return st, nil
}

func (f *FakeRuntime) Status(_ context.Context, serverID string) (*agentpb.ServerStatus, error) {
	st := f.getState(serverID)
	status := &agentpb.ServerStatus{ServerId: serverID, State: st}
	// A crashed fake server reports the exit code a real one would, so the
	// Panel's crash notice can be exercised without a Windows node: 0xC0000135
	// is STATUS_DLL_NOT_FOUND, the case that made #280 expensive.
	if st == agentpb.ServerState_SERVER_STATE_CRASHED {
		status.LastExitCode, status.ExitCodeKnown = 3221225781, true
	}
	return status, nil
}

func (f *FakeRuntime) StreamConsole(ctx context.Context, serverID string, tail int32, emit func(*agentpb.ConsoleLine) error) error {
	for i := int32(0); i < tail; i++ {
		if err := emit(&agentpb.ConsoleLine{
			ServerId: serverID, TsUnixMs: nowMs(), Stream: "stdout",
			Text: fmt.Sprintf("[replay %d] historical console line", i+1),
		}); err != nil {
			return err
		}
	}
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	n := 0
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			n++
			if err := emit(&agentpb.ConsoleLine{
				ServerId: serverID, TsUnixMs: nowMs(), Stream: "stdout",
				Text: fmt.Sprintf("INFO  world tick stabilized (sample %d)", n),
			}); err != nil {
				return err
			}
		}
	}
}

func (f *FakeRuntime) SendCommand(_ context.Context, _ string, _ string) error {
	return nil
}

func (f *FakeRuntime) StreamStats(ctx context.Context, serverID string, intervalMs int32, emit func(*agentpb.ResourceStats) error) error {
	if intervalMs < 100 {
		intervalMs = 500
	}
	ticker := time.NewTicker(time.Duration(intervalMs) * time.Millisecond)
	defer ticker.Stop()
	start := time.Now()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := emit(&agentpb.ResourceStats{
				ServerId: serverID, TsUnixMs: nowMs(),
				CpuPercent: 34.0, MemoryUsedMb: 6200, MemoryLimitMb: 16384,
				NetRxBytes: 1024, NetTxBytes: 2048,
				UptimeSeconds: int64(time.Since(start).Seconds()), DiskUsedMb: 512,
			}); err != nil {
				return err
			}
		}
	}
}

func nowMs() int64 { return time.Now().UnixMilli() }
