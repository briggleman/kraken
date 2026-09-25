package api_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/briggleman/kraken/internal/agent"
	"github.com/briggleman/kraken/internal/panel/cluster"
	"github.com/briggleman/kraken/internal/panel/store"
	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// Follow-ups from the #376 review (#377).

// The retire's reachability probe used the node read when the retire began,
// and a probe writes back the copy it works on — so a cordon or a rename made
// during a long stop was undone by it. The node is edited here once the stop
// is over (the phase moves to backing_up), and the edit must survive the probe
// that finds the node gone.
func TestRetire_TheProbeKeepsANodeEditMadeDuringTheRetire(t *testing.T) {
	ctx := context.Background()
	srv, st := newRemovalAPI(t)
	h := srv.Handler()
	token := login(t, h)
	addr, _, stop := startStoppableAgent(t, "node-edited")
	nodeID := liveNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "retire-edited", map[string]any{"script": "install.sh"})
	sv := placedServer(t, st, "sv-edited", nodeID, specID)
	sv.State = store.StateRunning
	if err := st.UpdateServer(ctx, sv); err != nil {
		t.Fatal(err)
	}
	stop()
	var once atomic.Bool
	hook := func(row *store.Server) {
		if row.ID != sv.ID || row.Retire == nil || row.Retire.Phase != store.RetirePhaseBackingUp || !once.CompareAndSwap(false, true) {
			return
		}
		n, err := st.Store.GetNode(ctx, nodeID)
		if err != nil {
			t.Error(err)
			return
		}
		n.Cordoned, n.Status, n.Name = true, cluster.NodeCordoned, "renamed-mid-retire"
		if err := st.Store.UpdateNode(ctx, n); err != nil {
			t.Error(err)
		}
	}
	st.onUpdateServer.Store(&hook)
	t.Cleanup(func() { st.onUpdateServer.Store(nil) })

	v := retireAndWait(t, srv, token, sv.ID, true)
	if !once.Load() {
		t.Fatal("the retire never reached backing_up, so the node was never edited")
	}
	n, err := st.Store.GetNode(ctx, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if !n.Cordoned || n.Name != "renamed-mid-retire" {
		t.Fatalf("node after the retire: cordoned=%v name=%q — the probe wrote back the copy read before the edit", n.Cordoned, n.Name)
	}
	if n.Status != cluster.NodeOffline {
		t.Fatalf("node status = %q, want offline: the probe still records what it found", n.Status)
	}
	if owed := n.PendingRemovals; len(owed) != 1 || owed[0].ServerID != sv.ID {
		t.Fatalf("pending = %+v, want the retire's removal", owed)
	}
	// And the note names the node as it is now, not as it was.
	if v.State != "retired" || !strings.Contains(v.RetireNote, "queued until node renamed-mid-retire answers") {
		t.Fatalf("row = %+v, want retired with the removal queued for the node under its new name", v)
	}
}

// A revive whose install failed left the schedules off (right), and the
// reinstall that then succeeded never switched them back on (wrong). They come
// back on with the first install that lands, and only the ones the retire
// switched off.
func TestRevive_AReinstallAfterAFailedReviveInstallSwitchesTheSchedulesBackOn(t *testing.T) {
	ctx := context.Background()
	srv, st := newRemovalAPI(t)
	h := srv.Handler()
	token := login(t, h)
	// The revive's pass fails; the reinstall's (the fake's second for this
	// server) succeeds.
	addr, rt := startFakeAgentRuntime(t, "node-ifail-then-ok",
		agent.WithFakeInstallOutcomes("ERROR! Failed to install app '740' (No subscription)"))
	nodeID := liveNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "revive-ifail-ok", map[string]any{"script": "install.sh"})
	sv := placedServer(t, st, "sv-ifail-ok", nodeID, specID)
	seedSchedule(t, st, "sched-retired", sv.ID)
	seedSchedule(t, st, "sched-operator-off", sv.ID)
	off := schedulesOf(t, st, sv.ID)["sched-operator-off"]
	off.Enabled = false
	if err := st.UpdateSchedule(ctx, off); err != nil {
		t.Fatal(err)
	}
	retireServer(t, srv, token, sv.ID)

	if rec := do(t, h, http.MethodPost, "/api/v1/servers/"+sv.ID+"/revive", token, nil); rec.Code != http.StatusAccepted {
		t.Fatalf("revive: %d %s", rec.Code, rec.Body.String())
	}
	waitForState(t, h, token, sv.ID, "install_failed")
	waitOpClear(t, srv, sv.ID)
	if s := schedulesOf(t, st, sv.ID)["sched-retired"]; s.Enabled || !s.DisabledByRetire {
		t.Fatalf("schedule after the failed revive install = %+v, want still off and flagged", s)
	}

	if rec := do(t, h, http.MethodPost, "/api/v1/servers/"+sv.ID+"/reinstall", token, nil); rec.Code != http.StatusAccepted {
		t.Fatalf("reinstall: %d %s", rec.Code, rec.Body.String())
	}
	waitForState(t, h, token, sv.ID, "offline")
	if n := len(rt.InstallScripts(sv.ID)); n != 2 {
		t.Fatalf("%d install passes, want 2 (the failed revive, the reinstall)", n)
	}
	scheds := schedulesOf(t, st, sv.ID)
	on := scheds["sched-retired"]
	if !on.Enabled || on.DisabledByRetire || on.NextRunAt != nil {
		t.Fatalf("schedule the retire switched off, after the reinstall = %+v, want on, unflagged, and its next run left to the scheduler", on)
	}
	if scheds["sched-operator-off"].Enabled {
		t.Fatal("a schedule the operator had switched off was switched on by the reinstall")
	}
	// The scheduler arms its next run the way it arms any schedule switched on.
	srv.RunDueSchedulesForTest(ctx)
	if armed := schedulesOf(t, st, sv.ID)["sched-retired"]; armed.NextRunAt == nil || !armed.NextRunAt.After(time.Now()) {
		t.Fatalf("next run after a scheduler pass = %v, want armed in the future", armed.NextRunAt)
	}
}

// A stop the node answers with a refusal abandons the retire (fail closed),
// and would refuse the next one the same way, so the reason says the way
// through: retire again without the final backup, which does not need the
// stop.
func TestRetire_AStopTheNodeRefusesSaysHowToRetireWithoutTheBackup(t *testing.T) {
	ctx := context.Background()
	srv, st := newRemovalAPI(t)
	h := srv.Handler()
	token := login(t, h)
	addr, rt := startFakeAgentRuntime(t, "node-stop-refused",
		agent.WithFakePowerError(agentpb.PowerAction_POWER_ACTION_STOP, errors.New("container is paused")))
	nodeID := liveNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "retire-stop-refused", map[string]any{"script": "install.sh"})
	sv := placedServer(t, st, "sv-stop-refused", nodeID, specID)
	sv.State = store.StateRunning
	if err := st.UpdateServer(ctx, sv); err != nil {
		t.Fatal(err)
	}
	w := watchRemovals(rt, sv.ID)

	v := retireAndWait(t, srv, token, sv.ID, true)
	if v.State != "running" || v.Retire != nil {
		t.Fatalf("row = %+v, want it back running (never stopped) with no retire block", v)
	}
	for _, want := range []string{
		"retire abandoned: the server could not be stopped for its final backup: container is paused; nothing was removed",
		"retire again with the final backup unchecked (final_backup: false)",
	} {
		if !strings.Contains(v.LastError, want) {
			t.Errorf("last_error = %q, want it to contain %q", v.LastError, want)
		}
	}
	if v.RetireNote != v.LastError {
		t.Errorf("retire_note = %q, want the same sentence as last_error", v.RetireNote)
	}
	w.none(t)
	if !held(t, st, nodeID) {
		t.Fatal("an abandoned retire released the allocation")
	}
}
