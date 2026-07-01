package agent

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
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

	mu      sync.Mutex
	states  map[string]agentpb.ServerState
	configs map[string]map[string]string     // serverID → path → content
	backups map[string][]*agentpb.BackupInfo // serverID → backups
}

// NewFakeRuntime returns a fake runtime identifying as the given node.
func NewFakeRuntime(nodeID, os string, wineEnabled bool, version string) *FakeRuntime {
	return &FakeRuntime{
		nodeID: nodeID, os: os, wineEnabled: wineEnabled, version: version,
		states: make(map[string]agentpb.ServerState),
	}
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
		TotalMemoryMb:  16384,
		RunningServers: int32(running),
		Host:           PrimaryIP(),
		ExternalIp:     "203.0.113.10", // documentation IP; lets tests exercise external-IP adoption
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
	return nil
}

// ApplyConfig records the rendered files in memory (no real volume in the fake).
func (f *FakeRuntime) ApplyConfig(_ context.Context, serverID string, files map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.configs == nil {
		f.configs = make(map[string]map[string]string)
	}
	f.configs[serverID] = files
	return nil
}

func (f *FakeRuntime) ListFiles(_ context.Context, _ string, path string) ([]*agentpb.FileEntry, error) {
	if path == "" {
		path = "/data"
	}
	return []*agentpb.FileEntry{
		{Name: "server.cfg", Path: path + "/server.cfg", IsDir: false, Size: 128, ModUnixMs: nowMs()},
		{Name: "saves", Path: path + "/saves", IsDir: true, Size: 0, ModUnixMs: nowMs()},
	}, nil
}

func (f *FakeRuntime) ReadFile(_ context.Context, _ string, p string, _ int64) ([]byte, int64, bool, bool, error) {
	content := []byte("# fake content for " + p + "\nkey=value\n")
	return content, int64(len(content)), false, false, nil
}

func (f *FakeRuntime) DownloadFile(_ context.Context, _ string, p string, w io.Writer) error {
	_, err := w.Write([]byte("fake content for " + p + "\n"))
	return err
}

func (f *FakeRuntime) MovePath(_ context.Context, _ string, _, _ string) error { return nil }
func (f *FakeRuntime) CopyPath(_ context.Context, _ string, _, _ string) error { return nil }

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

func (f *FakeRuntime) CreateBackup(_ context.Context, serverID, _, name string) (*agentpb.BackupInfo, error) {
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

func (f *FakeRuntime) ApplyNodeConfig(_ context.Context, cfg *agentpb.NodeConfig) (bool, string) {
	if cfg == nil {
		return true, "no config"
	}
	target := cfg.GetBackupTarget()
	if target == "" {
		target = "local"
	}
	return true, fmt.Sprintf("fake: primary=%s replication=%t", target, cfg.GetReplicateToSftp())
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

func (f *FakeRuntime) MakeDir(_ context.Context, _ string, _ string) error             { return nil }
func (f *FakeRuntime) WriteFile(_ context.Context, _ string, _ string, _ []byte) error { return nil }
func (f *FakeRuntime) DeletePaths(_ context.Context, _ string, _ []string) error       { return nil }

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
	return &agentpb.ServerStatus{ServerId: serverID, State: f.getState(serverID)}, nil
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
