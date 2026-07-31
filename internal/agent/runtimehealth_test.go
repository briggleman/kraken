package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// deadDockerRuntime builds a runtime pointed at a port nothing is listening on,
// so every daemon call fails fast. That is the whole point of these tests: the
// degraded path must be exercisable without Docker.
func deadDockerRuntime(t *testing.T) *DockerRuntime {
	t.Helper()
	t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:1")
	t.Setenv("KRAKEN_DATA_DIR", t.TempDir())
	t.Setenv("KRAKEN_BACKUP_DIR", t.TempDir())
	t.Setenv("KRAKEN_STATE_DIR", t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rt, err := NewDockerRuntime(ctx, "degraded-node", "linux", true, "test")
	if err != nil {
		t.Fatalf("NewDockerRuntime with an unreachable daemon must succeed, got: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	return rt
}

// An unreachable Docker daemon must not stop the Agent from coming up, and must
// not be reported as a transport error either. The Panel has to be able to tell
// "agent up, Docker down" (partial) from "agent gone" (offline), which it can
// only do if NodeInfo still answers.
func TestNodeInfoReportsRuntimeUnavailable(t *testing.T) {
	rt := deadDockerRuntime(t)

	ok, why := rt.RuntimeHealth()
	if ok {
		t.Fatal("RuntimeHealth reported healthy against a dead daemon")
	}
	if why == "" {
		t.Error("RuntimeHealth gave no reason; the Panel surfaces this verbatim as the operator's diagnosis")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	info, err := rt.NodeInfo(ctx)
	if err != nil {
		t.Fatalf("NodeInfo must answer when Docker is down, got error: %v", err)
	}
	if info.GetRuntimeStatus() != agentpb.RuntimeStatus_RUNTIME_STATUS_UNAVAILABLE {
		t.Errorf("runtime_status = %v, want UNAVAILABLE", info.GetRuntimeStatus())
	}
	if info.GetRuntimeError() == "" {
		t.Error("runtime_error is empty; nothing tells the operator what to fix")
	}
	// Identity still has to be right — this is how the node stays recognizable
	// while degraded.
	if info.GetNodeId() != "degraded-node" {
		t.Errorf("node_id = %q, want %q", info.GetNodeId(), "degraded-node")
	}
	if info.GetOs() != "linux" {
		t.Errorf("os = %q, want the configured %q (the daemon can't be asked)", info.GetOs(), "linux")
	}
}

// A node's specs must outlive the Agent process: on restart they are what tells
// the watchdog which servers auto-restart on crash, and what lets a start
// recreate a container before the Panel re-pushes anything.
func TestSpecsSurviveRestart(t *testing.T) {
	rt := deadDockerRuntime(t)
	spec := &agentpb.ServerSpec{
		ServerId:       "srv-1",
		Image:          "kraken/steam-base:latest",
		StartupCommand: "./start.sh",
		ReadyRegex:     "Session created",
		RestartOnCrash: true,
		MaxRestarts:    5,
		Env:            map[string]string{"ADMIN_PASSWORD": "hunter2"},
	}
	if err := rt.Create(context.Background(), spec); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// A second runtime over the same state dir stands in for the restarted Agent.
	restarted := deadDockerRuntimeAt(t, rt.specDir)
	got, ok := restarted.getSpec("srv-1")
	if !ok {
		t.Fatal("restarted Agent did not restore the persisted spec; restart_on_crash would be silently lost")
	}
	if !got.GetRestartOnCrash() || got.GetMaxRestarts() != 5 {
		t.Errorf("restart policy lost: restart_on_crash=%v max_restarts=%d", got.GetRestartOnCrash(), got.GetMaxRestarts())
	}
	if got.GetReadyRegex() != "Session created" {
		t.Errorf("ready_regex = %q, want %q", got.GetReadyRegex(), "Session created")
	}
	if got.GetEnv()["ADMIN_PASSWORD"] != "hunter2" {
		t.Error("env did not round-trip")
	}

	// A spec carries game admin/RCON passwords, so the file must not be
	// world-readable. (No-op on Windows, which doesn't model POSIX bits.)
	if fi, err := os.Stat(rt.specFile("srv-1")); err == nil && filepath.Separator == '/' {
		if perm := fi.Mode().Perm(); perm != 0o600 {
			t.Errorf("persisted spec mode = %04o, want 0600 (it holds server secrets)", perm)
		}
	}

	// Removing the server drops the persisted copy, so a stale spec can't be
	// adopted after the server is gone.
	if err := rt.Remove(context.Background(), "srv-1", false); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(rt.specFile("srv-1")); !os.IsNotExist(err) {
		t.Errorf("persisted spec still present after Remove (stat err = %v)", err)
	}
}

// deadDockerRuntimeAt builds a second degraded runtime sharing an existing spec
// directory — a restarted Agent on the same host.
func deadDockerRuntimeAt(t *testing.T, specDir string) *DockerRuntime {
	t.Helper()
	t.Setenv("KRAKEN_STATE_DIR", filepath.Dir(specDir))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rt, err := NewDockerRuntime(ctx, "degraded-node", "linux", true, "test")
	if err != nil {
		t.Fatalf("NewDockerRuntime: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	return rt
}

// A corrupt spec file must not take the Agent down with it: the Panel re-pushes
// specs on the next power action, so skipping one is recoverable where failing
// to start is not.
func TestLoadSpecsSkipsCorruptFiles(t *testing.T) {
	rt := deadDockerRuntime(t)
	if err := os.MkdirAll(rt.specDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rt.specDir, "broken.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	good := &agentpb.ServerSpec{ServerId: "srv-ok", Image: "busybox"}
	rt.persistSpec(good)

	fresh := deadDockerRuntimeAt(t, rt.specDir)
	if _, ok := fresh.getSpec("srv-ok"); !ok {
		t.Error("a corrupt sibling file prevented the valid spec from loading")
	}
}
