package agent

import (
	"context"
	"testing"
	"time"

	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// The fake's half of the watchdog signal (#348): a simulated watchdog restart
// shows in Status the way the Docker runtime's does, so the Panel's diff of the
// count can be tested without a crashing container. It counts up and stamps
// each restart, a stop leaves it standing, and an operator start or restart
// clears it, as a fresh watchdog does.
func TestFakeWatchdogRestarts(t *testing.T) {
	const serverID = "s1"
	f := NewFakeRuntime("n1", "linux", false, "test")
	ctx := context.Background()

	read := func() *agentpb.ServerStatus {
		t.Helper()
		st, err := f.Status(ctx, serverID)
		if err != nil {
			t.Fatal(err)
		}
		return st
	}

	if _, err := f.Power(ctx, serverID, agentpb.PowerAction_POWER_ACTION_START); err != nil {
		t.Fatal(err)
	}
	if st := read(); st.WatchdogRestarts != 0 || st.LastWatchdogRestartUnixMs != 0 {
		t.Fatalf("freshly started: %+v", st)
	}

	run := f.run(serverID)
	before := time.Now().UnixMilli()
	f.SimulateWatchdogRestart(serverID)
	st := read()
	if st.WatchdogRestarts != 1 || st.LastWatchdogRestartUnixMs < before || st.LastWatchdogRestartUnixMs > time.Now().UnixMilli() {
		t.Errorf("after one restart: %+v (crashed at or after %d)", st, before)
	}
	if st.State != agentpb.ServerState_SERVER_STATE_RUNNING {
		t.Errorf("a healed server is running again: %v", st.State)
	}
	if f.run(serverID) != run+1 {
		t.Errorf("a watchdog restart is a new container run: run %d → %d", run, f.run(serverID))
	}

	f.SimulateWatchdogRestart(serverID)
	if st := read(); st.WatchdogRestarts != 2 {
		t.Errorf("after two restarts: %+v", st)
	}

	// A stop is not a fresh watchdog: the count stands for the Panel to read.
	if _, err := f.Power(ctx, serverID, agentpb.PowerAction_POWER_ACTION_STOP); err != nil {
		t.Fatal(err)
	}
	if st := read(); st.WatchdogRestarts != 2 {
		t.Errorf("a stop must leave the count standing: %+v", st)
	}

	for _, action := range []agentpb.PowerAction{agentpb.PowerAction_POWER_ACTION_START, agentpb.PowerAction_POWER_ACTION_RESTART} {
		f.SimulateWatchdogRestart(serverID)
		if _, err := f.Power(ctx, serverID, action); err != nil {
			t.Fatal(err)
		}
		if st := read(); st.WatchdogRestarts != 0 || st.LastWatchdogRestartUnixMs != 0 {
			t.Errorf("%v must reset the count: %+v", action, st)
		}
	}

	// Removal takes the watchdog with it.
	f.SimulateWatchdogRestart(serverID)
	if err := f.Remove(ctx, serverID, false); err != nil {
		t.Fatal(err)
	}
	if st := read(); st.WatchdogRestarts != 0 || st.LastWatchdogRestartUnixMs != 0 {
		t.Errorf("a removed server has no restarts: %+v", st)
	}
}
