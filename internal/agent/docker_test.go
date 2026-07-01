package agent_test

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/briggleman/kraken/internal/agent"
	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// newDockerRuntime connects to Docker or skips the test when the daemon is
// unavailable (e.g. CI without Docker). These are real integration tests: they
// pull busybox and create/run actual containers.
func newDockerRuntime(t *testing.T) *agent.DockerRuntime {
	t.Helper()
	// Keep test data/backups in temp dirs so integration runs don't litter the
	// source tree with server-data/ and backups/.
	t.Setenv("KRAKEN_DATA_DIR", t.TempDir())
	t.Setenv("KRAKEN_BACKUP_DIR", t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rt, err := agent.NewDockerRuntime(ctx, "itest-node", true, "test")
	if err != nil {
		t.Skipf("docker unavailable, skipping integration test: %v", err)
	}
	if rt.OSType() != "linux" {
		_ = rt.Close()
		t.Skipf("daemon is in %q-container mode; these tests use Linux images", rt.OSType())
	}
	t.Cleanup(func() { _ = rt.Close() })
	return rt
}

const itestImage = "busybox:latest"

func TestDockerLifecycle(t *testing.T) {
	rt := newDockerRuntime(t)
	ctx := context.Background()
	const id = "itest-lifecycle"

	// Clean any leftovers from a prior run, and ensure cleanup afterward.
	_ = rt.Remove(ctx, id, true)
	t.Cleanup(func() { _ = rt.Remove(context.Background(), id, true) })

	spec := &agentpb.ServerSpec{
		ServerId:       id,
		Image:          itestImage,
		StartupCommand: `echo "server started"; i=0; while true; do echo "tick-$i"; i=$((i+1)); sleep 1; done`,
		DataPath:       "/data",
		StopSignal:     "SIGKILL", // busybox sh ignores SIGTERM in this loop; force fast stop
	}
	if err := rt.Create(ctx, spec); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Install phase: a one-shot container that writes to the data volume.
	install := &agentpb.InstallServerRequest{
		ServerId:      id,
		Image:         itestImage,
		InstallScript: `echo "installing"; echo ok > /data/installed.txt; echo "install done"`,
	}
	var completed bool
	var logs strings.Builder
	ictx, icancel := context.WithTimeout(ctx, 90*time.Second)
	defer icancel()
	err := rt.Install(ictx, install, func(ev *agentpb.InstallEvent) error {
		switch e := ev.Event.(type) {
		case *agentpb.InstallEvent_LogLine:
			logs.WriteString(e.LogLine + "\n")
		case *agentpb.InstallEvent_Completed:
			completed = true
		case *agentpb.InstallEvent_Failed:
			t.Fatalf("install failed: %s", e.Failed)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if !completed {
		t.Fatalf("install did not complete; logs:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), "install done") {
		t.Fatalf("expected install output, got:\n%s", logs.String())
	}

	// Start the runtime container. Readiness is async (the watchdog flips
	// starting→running), so START reports STARTING; with no ready_regex the
	// watchdog marks it running almost immediately.
	state, err := rt.Power(ctx, id, agentpb.PowerAction_POWER_ACTION_START)
	if err != nil {
		t.Fatalf("Power start: %v", err)
	}
	if state != agentpb.ServerState_SERVER_STATE_STARTING {
		t.Fatalf("expected STARTING, got %v", state)
	}
	if !waitForState(t, rt, id, agentpb.ServerState_SERVER_STATE_RUNNING, 5*time.Second) {
		t.Fatal("server did not reach RUNNING after start")
	}

	// Console: we should see streamed output within a few seconds.
	if line := firstConsoleLine(t, rt, id); !strings.Contains(line, "tick") && !strings.Contains(line, "started") {
		t.Fatalf("unexpected console output: %q", line)
	}

	// Stats: one sample should arrive.
	if !gotOneStat(t, rt, id) {
		t.Fatal("did not receive a resource stats sample")
	}

	// SendCommand should not error (busybox ignores it, but the attach+write works).
	if err := rt.SendCommand(ctx, id, "noop"); err != nil {
		t.Fatalf("SendCommand: %v", err)
	}

	// Stop → offline.
	state, err = rt.Power(ctx, id, agentpb.PowerAction_POWER_ACTION_STOP)
	if err != nil {
		t.Fatalf("Power stop: %v", err)
	}
	if state != agentpb.ServerState_SERVER_STATE_OFFLINE {
		t.Fatalf("expected OFFLINE after stop, got %v", state)
	}
}

// TestDockerReadyRegex verifies the watchdog holds a server in STARTING until a
// console line matches ready_regex, then flips it to RUNNING.
func TestDockerReadyRegex(t *testing.T) {
	rt := newDockerRuntime(t)
	ctx := context.Background()
	const id = "itest-ready"

	_ = rt.Remove(ctx, id, true)
	t.Cleanup(func() { _ = rt.Remove(context.Background(), id, true) })

	spec := &agentpb.ServerSpec{
		ServerId:       id,
		Image:          itestImage,
		StartupCommand: `sleep 2; echo "SERVER READY"; while true; do echo tick; sleep 1; done`,
		ReadyRegex:     "SERVER READY",
		DataPath:       "/data",
		StopSignal:     "SIGKILL",
	}
	if err := rt.Create(ctx, spec); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := rt.Power(ctx, id, agentpb.PowerAction_POWER_ACTION_START); err != nil {
		t.Fatalf("Power start: %v", err)
	}
	// Before the ready line prints (~2s), the server must report STARTING.
	if st, err := rt.Status(ctx, id); err != nil || st.State != agentpb.ServerState_SERVER_STATE_STARTING {
		t.Fatalf("expected STARTING before ready line, got %+v err=%v", st, err)
	}
	// After the ready line, it must flip to RUNNING.
	if !waitForState(t, rt, id, agentpb.ServerState_SERVER_STATE_RUNNING, 8*time.Second) {
		t.Fatal("server did not reach RUNNING after ready_regex matched")
	}

	if _, err := rt.Power(ctx, id, agentpb.PowerAction_POWER_ACTION_STOP); err != nil {
		t.Fatalf("Power stop: %v", err)
	}
}

// TestDockerCrashAutoRestart verifies that an unexpected (non-operator) exit is
// auto-restarted up to max_restarts, after which the server lands in CRASHED.
func TestDockerCrashAutoRestart(t *testing.T) {
	rt := newDockerRuntime(t)
	ctx := context.Background()
	const id = "itest-crash"

	_ = rt.Remove(ctx, id, true)
	t.Cleanup(func() { _ = rt.Remove(context.Background(), id, true) })

	// Each run lives ~1s then exits non-zero, simulating repeated crashes.
	spec := &agentpb.ServerSpec{
		ServerId:       id,
		Image:          itestImage,
		StartupCommand: `echo up; sleep 1; exit 7`,
		RestartOnCrash: true,
		MaxRestarts:    2,
		DataPath:       "/data",
		StopSignal:     "SIGKILL",
	}
	if err := rt.Create(ctx, spec); err != nil {
		t.Fatalf("Create: %v", err)
	}

	start := time.Now()
	if _, err := rt.Power(ctx, id, agentpb.PowerAction_POWER_ACTION_START); err != nil {
		t.Fatalf("Power start: %v", err)
	}
	// With 2 auto-restarts of ~1s runs, CRASHED should only arrive after several
	// seconds — proving the restarts happened rather than an immediate give-up.
	if !waitForState(t, rt, id, agentpb.ServerState_SERVER_STATE_CRASHED, 20*time.Second) {
		t.Fatal("server did not reach CRASHED after exhausting restarts")
	}
	if elapsed := time.Since(start); elapsed < 2*time.Second {
		t.Fatalf("reached CRASHED too fast (%s) — auto-restart did not run", elapsed)
	}
}

// waitForState polls Status until the server reports want or the timeout elapses.
func waitForState(t *testing.T, rt *agent.DockerRuntime, id string, want agentpb.ServerState, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if st, err := rt.Status(context.Background(), id); err == nil && st.State == want {
			return true
		}
		time.Sleep(150 * time.Millisecond)
	}
	return false
}

func firstConsoleLine(t *testing.T, rt *agent.DockerRuntime, id string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	lines := make(chan string, 64)
	go func() {
		_ = rt.StreamConsole(ctx, id, 20, func(cl *agentpb.ConsoleLine) error {
			select {
			case lines <- cl.Text:
			default:
			}
			return nil
		})
	}()
	select {
	case l := <-lines:
		return l
	case <-ctx.Done():
		return ""
	}
}

func gotOneStat(t *testing.T, rt *agent.DockerRuntime, id string) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	got := make(chan struct{}, 1)
	go func() {
		_ = rt.StreamStats(ctx, id, 1000, func(_ *agentpb.ResourceStats) error {
			select {
			case got <- struct{}{}:
			default:
			}
			return io.EOF // stop after first sample
		})
	}()
	select {
	case <-got:
		return true
	case <-ctx.Done():
		return false
	}
}
