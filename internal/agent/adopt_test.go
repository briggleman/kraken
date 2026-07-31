package agent

import (
	"context"
	"testing"
	"time"

	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// newAdoptRuntime builds a Docker-backed runtime over the given dirs, skipping
// the test when there is no usable Linux daemon.
func newAdoptRuntime(t *testing.T, dataDir, backupDir, stateDir string) *DockerRuntime {
	t.Helper()
	t.Setenv("KRAKEN_DATA_DIR", dataDir)
	t.Setenv("KRAKEN_BACKUP_DIR", backupDir)
	t.Setenv("KRAKEN_STATE_DIR", stateDir)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	rt, err := NewDockerRuntime(ctx, "adopt-node", "linux", true, "test")
	if err != nil {
		t.Skipf("docker client unavailable: %v", err)
	}
	if ok, why := rt.RuntimeHealth(); !ok {
		_ = rt.Close()
		t.Skipf("docker daemon unreachable: %s", why)
	}
	if rt.OSType() != "linux" {
		_ = rt.Close()
		t.Skipf("daemon is in %q-container mode; this test uses a Linux image", rt.OSType())
	}
	t.Cleanup(func() { _ = rt.Close() })
	return rt
}

// TestAdoptRunningRestoresCrashRestart is the end-to-end proof for the watchdog
// gap: monitors were only ever armed by Power(START|RESTART), so an Agent restart
// silently dropped restart_on_crash for every server that was already up. It was
// invisible — Status falls back to inspecting the container, so the Panel kept
// reporting the right state while nothing was watching for a crash.
//
// The test leaves a container running with no monitor (exactly the state a
// restarted Agent inherits), brings up a second runtime over the same state dir,
// and kills the container to see whether anything notices.
func TestAdoptRunningRestoresCrashRestart(t *testing.T) {
	dataDir, backupDir, stateDir := t.TempDir(), t.TempDir(), t.TempDir()
	first := newAdoptRuntime(t, dataDir, backupDir, stateDir)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	const serverID = "adopt-1"
	spec := &agentpb.ServerSpec{
		ServerId:       serverID,
		Image:          "busybox:latest",
		StartupCommand: "sleep 600",
		RestartOnCrash: true,
		MaxRestarts:    2,
	}
	if err := first.pullImage(ctx, spec.Image, func(string) {}); err != nil {
		t.Skipf("could not pull %s: %v", spec.Image, err)
	}
	if err := first.Create(ctx, spec); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Start it *without* arming a monitor — this is what a running server looks
	// like to an Agent that has just restarted.
	if err := first.ensureAndStart(ctx, serverID); err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, watched := first.monitorState(serverID); watched {
		t.Fatal("precondition failed: a monitor exists, so this isn't the post-restart state")
	}

	// The restarted Agent: same state dir, so it reloads the spec and adopts.
	second := newAdoptRuntime(t, dataDir, backupDir, stateDir)
	// Tear down through the adopting runtime, and only after its watchdog is
	// stopped — otherwise the cleanup's own container removal reads as a crash and
	// the watchdog dutifully restarts what the test is trying to delete.
	t.Cleanup(func() {
		second.stopMonitor(serverID)
		_ = second.Remove(context.Background(), serverID, true)
	})
	restored, ok := second.getSpec(serverID)
	if !ok {
		t.Fatal("restarted runtime did not reload the persisted spec")
	}
	if !restored.GetRestartOnCrash() {
		t.Error("restored spec lost restart_on_crash")
	}
	if _, watched := second.monitorState(serverID); !watched {
		t.Fatal("restarted runtime did not adopt the running server; restart_on_crash would stay dead until the next power action")
	}

	// An adopted server with no ready_regex reports RUNNING, not STARTING — a
	// server that has been serving players must never regress to starting.
	if st, err := second.Status(ctx, serverID); err != nil {
		t.Fatalf("Status: %v", err)
	} else if st.GetState() != agentpb.ServerState_SERVER_STATE_RUNNING {
		t.Errorf("adopted state = %v, want RUNNING", st.GetState())
	}

	// Now the actual crash. Before this fix nothing was listening and the server
	// stayed down.
	startedBefore := containerStartTime(t, second, serverID)
	if err := second.cli.ContainerKill(ctx, containerName(serverID), "SIGKILL"); err != nil {
		t.Fatalf("kill: %v", err)
	}

	deadline := time.Now().Add(90 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatal("the adopted watchdog did not auto-restart the server after a crash")
		}
		insp, err := second.cli.ContainerInspect(ctx, containerName(serverID))
		if err == nil && insp.State != nil && insp.State.Running && insp.State.StartedAt != startedBefore {
			return // restarted by the adopted watchdog
		}
		time.Sleep(time.Second)
	}
}

func containerStartTime(t *testing.T, d *DockerRuntime, serverID string) string {
	t.Helper()
	insp, err := d.cli.ContainerInspect(context.Background(), containerName(serverID))
	if err != nil || insp.State == nil {
		t.Fatalf("inspect: %v", err)
	}
	return insp.State.StartedAt
}
