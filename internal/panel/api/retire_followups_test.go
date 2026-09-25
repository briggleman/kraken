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
	// The schedules are switched back on just after the row reads offline, so
	// that is what is waited for, not the state.
	waitScheduleEnabled(t, st, sv.ID, "sched-retired")
	if n := len(rt.InstallScripts(sv.ID)); n != 2 {
		t.Fatalf("%d install passes, want 2 (the failed revive, the reinstall)", n)
	}
	if got := getServerState(t, h, token, sv.ID); got != "offline" {
		t.Fatalf("state after the reinstall = %q, want offline", got)
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

// waitScheduleEnabled waits (bounded) for a schedule to be switched on. The
// switch happens after the install's write of `offline`, so a test that only
// waited for the state could read the schedules in between.
func waitScheduleEnabled(t *testing.T, st *flakyStore, serverID, id string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		s := schedulesOf(t, st, serverID)[id]
		if s != nil && s.Enabled {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("schedule %s was never switched on: %+v", id, s)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A stop the node answers with a refusal abandons the retire (fail closed),
// and would refuse the next one the same way, so the reason says the way
// through: retire again without the final backup, which does not wait on the
// stop. A stop the node never got says nothing of the kind: that retire goes
// through without the backup on its own once the node is unreachable.
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
		`retire again with "take a final backup first" unchecked (final_backup: false in the API)`,
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

	// The retried stop does not reach the node (the first missed it too, and
	// the node answered the probe in between): abandoned, and no hint.
	addr2, rt2 := startFakeAgentRuntime(t, "node-stop-missed")
	rt2.FailNextPower(agentpb.PowerAction_POWER_ACTION_STOP, 2)
	nodeID2 := liveNode(t, h, token, addr2)
	sv2 := placedServer(t, st, "sv-stop-missed", nodeID2, specID)
	sv2.State = store.StateRunning
	if err := st.UpdateServer(ctx, sv2); err != nil {
		t.Fatal(err)
	}
	v2 := retireAndWait(t, srv, token, sv2.ID, true)
	if v2.State != "running" || !strings.Contains(v2.LastError, "the node stopped answering") {
		t.Fatalf("row = %+v, want it back running with the unreachable stop in last_error", v2)
	}
	if strings.Contains(v2.LastError, "final_backup: false") {
		t.Fatalf("last_error = %q: the hint is for a stop the node refused, not one it never got", v2.LastError)
	}
}

// Review of #377: a probe whose read of the node fails must not be taken for
// a node that does not answer — that queued the removal, world and all, with
// no final backup, on a node that may well be answering. It abandons the
// retire, as the retire's first read of the node does.
func TestRetire_AProbeThatCannotReadTheNodeAbandons(t *testing.T) {
	ctx := context.Background()
	srv, st := newRemovalAPI(t)
	h := srv.Handler()
	token := login(t, h)
	addr, rt := startFakeAgentRuntime(t, "node-unread")
	nodeID := liveNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "retire-unread", map[string]any{"script": "install.sh"})
	sv := placedServer(t, st, "sv-unread", nodeID, specID)
	sv.State = store.StateRunning
	if err := st.UpdateServer(ctx, sv); err != nil {
		t.Fatal(err)
	}
	// The stop misses, so the backup is skipped and the probe is due; the
	// store fails the probe's read of the node, once.
	rt.FailNextPower(agentpb.PowerAction_POWER_ACTION_STOP, 1)
	var once atomic.Bool
	hook := func(row *store.Server) {
		if row.ID == sv.ID && row.Retire != nil && row.Retire.Phase == store.RetirePhaseBackingUp && once.CompareAndSwap(false, true) {
			st.failNextGetNode.Store(true)
		}
	}
	st.onUpdateServer.Store(&hook)
	t.Cleanup(func() { st.onUpdateServer.Store(nil) })
	w := watchRemovals(rt, sv.ID)

	v := retireAndWait(t, srv, token, sv.ID, true)
	if !once.Load() || st.failNextGetNode.Load() {
		t.Fatal("the store's failure was never armed, or never reached")
	}
	if v.State != "running" || v.Retire != nil {
		t.Fatalf("row = %+v, want it back running (the stop missed) with no retire block", v)
	}
	if !strings.Contains(v.LastError, "retire abandoned: could not load the server's node (store: connection reset); nothing was removed") {
		t.Fatalf("last_error = %q, want the failed read", v.LastError)
	}
	w.none(t)
	if owed := pendingRemovals(t, st, nodeID); len(owed) != 0 {
		t.Fatalf("pending = %+v: a removal was queued on a node that was never found unreachable", owed)
	}
	if !held(t, st, nodeID) {
		t.Fatal("an abandoned retire released the allocation")
	}
}

// Review of #377: the removal can probe the node too (removeOnNode →
// ensureNodeLive), and that probe writes back the copy it works on. It only
// probes a copy that reads offline, so here the node reads offline when the
// retire begins and does not answer the retire's first probe, then answers the
// removal's. The operator cordons and renames it in between; the removal's
// probe must write that back, not the copy read when the retire began.
func TestRetire_TheRemovalKeepsANodeEditMadeDuringTheRetire(t *testing.T) {
	ctx := context.Background()
	srv, st := newRemovalAPI(t)
	h := srv.Handler()
	token := login(t, h)
	addr, rt := startFakeAgentRuntime(t, "node-edited-at-removal")
	nodeID := liveNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "retire-edited-removal", map[string]any{"script": "install.sh"})
	sv := placedServer(t, st, "sv-edited-removal", nodeID, specID)
	n, err := st.Store.GetNode(ctx, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	n.Status = cluster.NodeOffline // what the last reconcile pass found
	if err := st.Store.UpdateNode(ctx, n); err != nil {
		t.Fatal(err)
	}
	rt.FailNextNodeInfo(1) // the retire's first probe; the removal's answers
	var once atomic.Bool
	hook := func(row *store.Server) {
		if row.ID != sv.ID || row.Retire == nil || row.Retire.Phase != store.RetirePhaseRemoving || !once.CompareAndSwap(false, true) {
			return
		}
		n, err := st.Store.GetNode(ctx, nodeID)
		if err != nil {
			t.Error(err)
			return
		}
		n.Cordoned, n.Name = true, "renamed-before-removal"
		if err := st.Store.UpdateNode(ctx, n); err != nil {
			t.Error(err)
		}
	}
	st.onUpdateServer.Store(&hook)
	t.Cleanup(func() { st.onUpdateServer.Store(nil) })

	v := retireAndWait(t, srv, token, sv.ID, false)
	if !once.Load() {
		t.Fatal("the retire never reached removing, so the node was never edited")
	}
	after, err := st.Store.GetNode(ctx, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if !after.Cordoned || after.Name != "renamed-before-removal" {
		t.Fatalf("node after the retire: cordoned=%v name=%q — the removal's probe wrote back the copy read before the edit",
			after.Cordoned, after.Name)
	}
	if after.Status != cluster.NodeCordoned {
		t.Fatalf("node status = %q, want cordoned: the probe found it answering, and it is cordoned", after.Status)
	}
	if v.State != "retired" || len(rt.Removals()) != 1 || held(t, st, nodeID) {
		t.Fatalf("row = %+v, removals %+v: want retired with the removal landed and the allocation released", v, rt.Removals())
	}
}
