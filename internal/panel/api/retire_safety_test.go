package api_test

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/briggleman/kraken/internal/agent"
	"github.com/briggleman/kraken/internal/panel/api"
	"github.com/briggleman/kraken/internal/panel/cluster"
	"github.com/briggleman/kraken/internal/panel/store"
	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// The rule the retire keeps above all (#360 review): while the node answers,
// nothing is removed unless the final backup that was asked for is READY.

// fastFinalBackup shortens the final backup's poll and deadline for a test.
func fastFinalBackup(t *testing.T, timeout time.Duration) {
	t.Helper()
	t.Cleanup(api.SetFinalBackupTimingForTest(5*time.Millisecond, timeout))
}

// removalWatch records, at the moment the node is asked to remove the server,
// what the final backup's state was then.
type removalWatch struct {
	mu        sync.Mutex
	removals  int
	sawStates []agentpb.BackupState
}

func watchRemovals(rt *agent.FakeRuntime, serverID string) *removalWatch {
	w := &removalWatch{}
	rt.SetRemoveHook(func(id string, deleteData bool) {
		if id != serverID || !deleteData {
			return
		}
		w.mu.Lock()
		defer w.mu.Unlock()
		w.removals++
		for _, b := range rt.Backups(id) {
			w.sawStates = append(w.sawStates, b.State)
		}
	})
	return w
}

func (w *removalWatch) readyAtRemoval(t *testing.T) {
	t.Helper()
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.removals != 1 {
		t.Fatalf("the node was asked to remove the world %d times, want once", w.removals)
	}
	if len(w.sawStates) != 1 || w.sawStates[0] != agentpb.BackupState_BACKUP_STATE_UNSPECIFIED {
		t.Fatalf("archive states when the removal went out = %v, want the final backup, ready", w.sawStates)
	}
}

func (w *removalWatch) none(t *testing.T) {
	t.Helper()
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.removals != 0 {
		t.Fatalf("the node was asked to remove the world %d times; it must not be without the final backup", w.removals)
	}
}

// removalReached arms the fake so the returned func blocks until a
// RemoveServer has reached the node — the moment a held removal is in flight.
// (It replaces the fake's remove hook; watchRemovals sets its own.)
func removalReached(t *testing.T, rt *agent.FakeRuntime) func() {
	t.Helper()
	ch := make(chan struct{}, 1)
	rt.SetRemoveHook(func(string, bool) {
		select {
		case ch <- struct{}{}:
		default:
		}
	})
	return func() {
		t.Helper()
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatal("no removal reached the node")
		}
	}
}

// The archive is PENDING for a few polls; the removal waits for READY.
func TestRetire_RemovalWaitsForTheFinalBackupToBeReady(t *testing.T) {
	fastFinalBackup(t, 5*time.Second)
	srv, st := newRemovalAPI(t)
	h := srv.Handler()
	token := login(t, h)
	addr, rt := startFakeAgentRuntime(t, "node-order")
	nodeID := liveNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "retire-order", map[string]any{"script": "install.sh"})
	sv := placedServer(t, st, "sv-order", nodeID, specID)
	rt.SetBackupPending(3)
	w := watchRemovals(rt, sv.ID)

	v := retireAndWait(t, srv, token, sv.ID, true)
	if v.State != "retired" {
		t.Fatalf("row = %+v, want retired", v)
	}
	w.readyAtRemoval(t)
}

// PENDING, then FAILED: abandoned, nothing removed.
func TestRetire_PendingThenFailedAbandons(t *testing.T) {
	fastFinalBackup(t, 5*time.Second)
	srv, st := newRemovalAPI(t)
	h := srv.Handler()
	token := login(t, h)
	addr, rt := startFakeAgentRuntime(t, "node-pfail")
	nodeID := liveNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "retire-pfail", map[string]any{"script": "install.sh"})
	sv := placedServer(t, st, "sv-pfail", nodeID, specID)
	rt.SetBackupPending(2)
	rt.SetBackupFailure("archive/tar: write too long")
	w := watchRemovals(rt, sv.ID)

	v := retireAndWait(t, srv, token, sv.ID, true)
	if v.State != "offline" || !strings.Contains(v.LastError, "write too long") {
		t.Fatalf("row = %+v, want offline with the backup's failure in last_error", v)
	}
	w.none(t)
}

// A backup still PENDING at the deadline — its archiver may still be writing
// — abandons the retire rather than delete the world under it.
func TestRetire_FinalBackupTimeoutAbandons(t *testing.T) {
	fastFinalBackup(t, 100*time.Millisecond)
	srv, st := newRemovalAPI(t)
	h := srv.Handler()
	token := login(t, h)
	addr, rt := startFakeAgentRuntime(t, "node-slow")
	nodeID := liveNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "retire-slow", map[string]any{"script": "install.sh"})
	sv := placedServer(t, st, "sv-slow", nodeID, specID)
	rt.SetBackupPending(-1)
	w := watchRemovals(rt, sv.ID)

	v := retireAndWait(t, srv, token, sv.ID, true)
	if v.State != "offline" || !strings.Contains(v.LastError, "did not finish in time") {
		t.Fatalf("row = %+v, want offline with the timeout in last_error", v)
	}
	w.none(t)
	if !held(t, st, nodeID) {
		t.Fatal("an abandoned retire released the allocation")
	}
}

// The stop does not reach the node (Unavailable) — the old code then skipped
// the backup and let the removal re-probe and delete the world. Now the node
// answers the probe at removal time, so the stop and the backup are tried
// again, and the removal goes out only once the backup is READY.
func TestRetire_AStopThatMissedIsRetriedBeforeAnyRemoval(t *testing.T) {
	fastFinalBackup(t, 5*time.Second)
	ctx := context.Background()
	srv, st := newRemovalAPI(t)
	h := srv.Handler()
	token := login(t, h)
	addr, rt := startFakeAgentRuntime(t, "node-blink")
	nodeID := liveNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "retire-blink", map[string]any{"script": "install.sh"})
	sv := placedServer(t, st, "sv-blink", nodeID, specID)
	sv.State = store.StateRunning
	if err := st.UpdateServer(ctx, sv); err != nil {
		t.Fatal(err)
	}
	rt.FailNextPower(agentpb.PowerAction_POWER_ACTION_STOP, 1)
	w := watchRemovals(rt, sv.ID)
	// The retried STOP must land before the retried backup is taken, or the
	// archive is of a world still being saved. The hook runs only for a STOP
	// that landed (the first one missed the node), and records how many
	// archives existed at that moment.
	var mu sync.Mutex
	var stopsLanded []int
	rt.SetPowerHook(func(id string, a agentpb.PowerAction) {
		if id != sv.ID || a != agentpb.PowerAction_POWER_ACTION_STOP {
			return
		}
		n := len(rt.Backups(id))
		mu.Lock()
		defer mu.Unlock()
		stopsLanded = append(stopsLanded, n)
	})

	v := retireAndWait(t, srv, token, sv.ID, true)
	if v.State != "retired" || v.RetireNote != "" {
		t.Fatalf("row = %+v, want retired with nothing to say (the retry took the backup)", v)
	}
	w.readyAtRemoval(t)
	mu.Lock()
	defer mu.Unlock()
	if len(stopsLanded) != 1 || stopsLanded[0] != 0 {
		t.Fatalf("archives when each landed STOP arrived = %v, want one STOP with no backup taken yet", stopsLanded)
	}
	if len(rt.Backups(sv.ID)) != 1 {
		t.Fatalf("archives = %+v, want the final backup, taken after the STOP", rt.Backups(sv.ID))
	}
}

// ...and when the retried backup fails, the retire is abandoned: the node
// answered, so nothing is removed without it.
func TestRetire_AStopThatMissedThenAFailedBackupAbandons(t *testing.T) {
	fastFinalBackup(t, 5*time.Second)
	ctx := context.Background()
	srv, st := newRemovalAPI(t)
	h := srv.Handler()
	token := login(t, h)
	addr, rt := startFakeAgentRuntime(t, "node-blink2")
	nodeID := liveNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "retire-blink2", map[string]any{"script": "install.sh"})
	sv := placedServer(t, st, "sv-blink2", nodeID, specID)
	sv.State = store.StateRunning
	if err := st.UpdateServer(ctx, sv); err != nil {
		t.Fatal(err)
	}
	rt.FailNextPower(agentpb.PowerAction_POWER_ACTION_STOP, 1)
	rt.SetBackupFailure("disk full")
	w := watchRemovals(rt, sv.ID)

	v := retireAndWait(t, srv, token, sv.ID, true)
	// The retry's stop landed, so the server is stopped: it goes back offline,
	// not to a `running` it no longer is.
	if v.State != "offline" || !strings.Contains(v.LastError, "disk full") {
		t.Fatalf("row = %+v, want offline with the backup's failure in last_error", v)
	}
	w.none(t)
}

// While the job runs the server reads `retiring`, and everything that would
// change it is refused with server_busy.
func TestRetire_TheServerReadsRetiringWhileTheJobRuns(t *testing.T) {
	srv, st := newRemovalAPI(t)
	h := srv.Handler()
	token := login(t, h)
	addr, rt := startFakeAgentRuntime(t, "node-busy")
	nodeID := liveNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "retire-busy", map[string]any{"script": "install.sh"})
	sv := placedServer(t, st, "sv-busy", nodeID, specID)
	release := rt.HoldRemovals()
	t.Cleanup(release)
	reached := removalReached(t, rt)

	rec := do(t, h, http.MethodPost, "/api/v1/servers/"+sv.ID+"/retire", token, map[string]bool{"final_backup": false})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("retire: %d %s", rec.Code, rec.Body.String())
	}
	reached()
	if got := getRetireView(t, h, token, sv.ID); got.State != "retiring" || got.Retire == nil || got.Retire.PrevState != "offline" {
		t.Fatalf("mid-retire row = %+v, want retiring from offline", got)
	}
	base := "/api/v1/servers/" + sv.ID
	for _, tc := range []struct {
		name, method, path string
		body               any
	}{
		{"start", http.MethodPost, base + "/power", map[string]string{"action": "start"}},
		{"settings save", http.MethodPut, base + "/settings", map[string]any{"values": map[string]string{}}},
		{"restore", http.MethodPost, base + "/backups/x/restore", nil},
		{"backup create", http.MethodPost, base + "/backups", nil},
		{"reinstall", http.MethodPost, base + "/reinstall", nil},
		{"delete", http.MethodDelete, base, nil},
		{"revive", http.MethodPost, base + "/revive", nil},
	} {
		rec := do(t, h, tc.method, tc.path, token, tc.body)
		want := "server_busy"
		if tc.name == "revive" || tc.name == "delete" {
			// Not retired yet, which is what those two say first.
			if rec.Code == http.StatusConflict && codedBody(t, rec.Body.Bytes()).Code == "server_not_retired" {
				continue
			}
		}
		if rec.Code != http.StatusConflict || codedBody(t, rec.Body.Bytes()).Code != want {
			t.Errorf("%s mid-retire: %d %s; want 409 %s", tc.name, rec.Code, rec.Body.String(), want)
		}
	}
	release()
	waitOpClear(t, srv, sv.ID)
	if got := getRetireView(t, h, token, sv.ID).State; got != "retired" {
		t.Fatalf("after the removal was let through: %q", got)
	}
}

// DELETE holds the row for its whole run: a revive asked for while it is
// deleting is refused, and cannot place the server out from under it.
func TestPermanentDelete_HoldsTheRowAgainstARevive(t *testing.T) {
	srv, st := newRemovalAPI(t)
	h := srv.Handler()
	token := login(t, h)
	addr, rt := startFakeAgentRuntime(t, "node-race")
	nodeID := liveNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "delete-race", map[string]any{"script": "install.sh"})
	sv := placedServer(t, st, "sv-race", nodeID, specID)
	retireServer(t, srv, token, sv.ID)
	release := rt.HoldRemovals()
	t.Cleanup(release)
	reached := removalReached(t, rt)

	done := make(chan int, 1)
	go func() { done <- do(t, h, http.MethodDelete, "/api/v1/servers/"+sv.ID, token, nil).Code }()
	reached() // the delete's RemoveServer is at the node, and the delete holds the row
	if held := srv.OperationHeldForTest(sv.ID); held != "delete" {
		t.Fatalf("mid-delete the row is held by %q", held)
	}
	rec := do(t, h, http.MethodPost, "/api/v1/servers/"+sv.ID+"/revive", token, nil)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "being deleted") {
		t.Fatalf("revive mid-delete: %d %s, want 409 naming the delete", rec.Code, rec.Body.String())
	}
	release()
	if code := <-done; code != http.StatusOK {
		t.Fatalf("delete: %d", code)
	}
}

// The reconciler's write goes onto a fresh read: a retire that completes
// between the list and the write is not undone by the snapshot.
func TestReconciler_DoesNotWriteAStaleSnapshotOverARetire(t *testing.T) {
	ctx := context.Background()
	srv, st := newRemovalAPI(t)
	h := srv.Handler()
	token := login(t, h)
	addr, rt := startFakeAgentRuntime(t, "node-stale")
	nodeID := liveNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "reconcile-stale", map[string]any{"script": "install.sh"})
	sv := placedServer(t, st, "sv-stale", nodeID, specID)
	sv.State = store.StateStarting // the agent will say running: a write is due
	if err := st.UpdateServer(ctx, sv); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Power(ctx, sv.ID, agentpb.PowerAction_POWER_ACTION_START); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(api.SetReconcileWriteHookForTest(func(id string) {
		if id != sv.ID {
			return
		}
		row, err := st.GetServer(ctx, id)
		if err != nil {
			t.Error(err)
			return
		}
		row.State, row.NodeID, row.RetiredFromNodeID = store.StateRetired, "", nodeID
		if err := st.UpdateServer(ctx, row); err != nil {
			t.Error(err)
		}
	}))
	srv.ReconcileOnceForTest(ctx)
	if got, _ := st.GetServer(ctx, sv.ID); got.State != store.StateRetired || got.NodeID != "" {
		t.Fatalf("after the pass: state %q node %q — the stale snapshot was written over the retire", got.State, got.NodeID)
	}
}

// A replay that delivered a removal without delete_backups, while a permanent
// delete folded delete_backups into the record, keeps the record and sends
// again, rather than finish it and drop the purge.
func TestPendingRemoval_APurgeFoldedInMidReplayIsSentAgain(t *testing.T) {
	ctx := context.Background()
	srv, st := newRemovalAPI(t)
	h := srv.Handler()
	token := login(t, h)
	addr, rt := startFakeAgentRuntime(t, "node-fold")
	nodeID := liveNode(t, h, token, addr)
	n, err := st.Store.GetNode(ctx, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	n.AddPendingRemoval(cluster.PendingRemoval{ServerID: "sv-fold", DeleteData: true, RequestedAt: time.Now()})
	if err := st.Store.UpdateNode(ctx, n); err != nil {
		t.Fatal(err)
	}
	release := rt.HoldRemovals()
	t.Cleanup(release)
	reached := removalReached(t, rt)
	srv.ReconcileNodesPassForTest(ctx)
	reached()
	// The permanent delete lands on the record while the RPC is out.
	n, err = st.Store.GetNode(ctx, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	n.AddPendingRemoval(cluster.PendingRemoval{ServerID: "sv-fold", DeleteData: true, DeleteBackups: true})
	if err := st.Store.UpdateNode(ctx, n); err != nil {
		t.Fatal(err)
	}
	release()
	srv.WaitRemovalReplaysForTest()

	owed := pendingRemovals(t, st, nodeID)
	if len(owed) != 1 || !owed[0].DeleteBackups || !owed[0].NextAttempt.IsZero() {
		t.Fatalf("pending = %+v, want the record kept with delete_backups, due again", owed)
	}
	if len(rt.Purges()) != 0 {
		t.Fatal("setup: the first delivery purged")
	}
	srv.ReconcileNodesOnceForTest(ctx)
	if got := rt.Purges(); len(got) != 1 || got[0] != "sv-fold" {
		t.Fatalf("purges = %v, want the folded-in purge delivered", got)
	}
	if len(pendingRemovals(t, st, nodeID)) != 0 {
		t.Fatal("the record outlived its delivery")
	}
}

// A revive's backup check can take a while; whatever was written to the node
// meanwhile survives the reservation.
func TestRevive_ReservesOnAFreshNodeAfterTheBackupCheck(t *testing.T) {
	ctx := context.Background()
	srv, st := newRemovalAPI(t)
	h := srv.Handler()
	token := login(t, h)
	addr, rt := startFakeAgentRuntime(t, "node-fresh")
	nodeID := liveNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "revive-fresh", map[string]any{"script": "install.sh"})
	sv := placedServer(t, st, "sv-fresh", nodeID, specID)
	retireAndWait(t, srv, token, sv.ID, true)
	backups := rt.Backups(sv.ID)
	var once atomic.Bool
	rt.SetListBackupsHook(func(string) {
		if !once.CompareAndSwap(false, true) {
			return
		}
		n, err := st.Store.GetNode(ctx, nodeID)
		if err != nil {
			t.Error(err)
			return
		}
		if _, err := n.Reserve(512, []cluster.PortRequest{{Name: "other", Preferred: 27090}}); err != nil {
			t.Error(err)
			return
		}
		if err := st.Store.UpdateNode(ctx, n); err != nil {
			t.Error(err)
		}
	})
	rec := do(t, h, http.MethodPost, "/api/v1/servers/"+sv.ID+"/revive", token, map[string]any{"restore_backup_id": backups[0].Id})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("revive: %d %s", rec.Code, rec.Body.String())
	}
	n, err := st.Store.GetNode(ctx, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if n.AllocatedMemoryMB != 1024+512 || n.Ports.IsFree(27090) || n.Ports.IsFree(27015) {
		t.Fatalf("node after revive: %d MB, 27090 free=%v, 27015 free=%v — a stale copy was written back",
			n.AllocatedMemoryMB, n.Ports.IsFree(27090), n.Ports.IsFree(27015))
	}
	waitForState(t, h, token, sv.ID, "offline")
}

// A revive whose install fails leaves the schedules off: they come back on
// only once the server is installed.
func TestRevive_AFailedInstallLeavesTheSchedulesOff(t *testing.T) {
	srv, st := newRemovalAPI(t)
	h := srv.Handler()
	token := login(t, h)
	addr, _ := startFakeAgentRuntime(t, "node-ifail", agent.WithFakeInstallFailure("depot unreachable"))
	nodeID := liveNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "revive-ifail", map[string]any{"script": "install.sh"})
	sv := placedServer(t, st, "sv-ifail", nodeID, specID)
	seedSchedule(t, st, "sched-ifail", sv.ID)
	retireServer(t, srv, token, sv.ID)
	if rec := do(t, h, http.MethodPost, "/api/v1/servers/"+sv.ID+"/revive", token, nil); rec.Code != http.StatusAccepted {
		t.Fatalf("revive: %d %s", rec.Code, rec.Body.String())
	}
	waitForState(t, h, token, sv.ID, "install_failed")
	if s := schedulesOf(t, st, sv.ID)["sched-ifail"]; s.Enabled || !s.DisabledByRetire {
		t.Fatalf("schedule after a failed revive install = %+v, want still off and flagged", s)
	}
}

// A spec a retired server was built from cannot be deleted, and says why.
func TestDeleteSpec_CountsRetiredServers(t *testing.T) {
	srv, st := newRemovalAPI(t)
	h := srv.Handler()
	token := login(t, h)
	specID := createSpecWithInstall(t, h, token, "spec-retired", map[string]any{"script": "install.sh"})
	seedRetiredServer(t, st, "sv-spec", "node-gone", specID)
	rec := do(t, h, http.MethodDelete, "/api/v1/specs/"+specID, token, nil)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "1 server uses this spec (1 retired) — revive or delete them for good first") {
		t.Fatalf("delete spec: %d %s", rec.Code, rec.Body.String())
	}
}

// A retired server has no console: the stream answers 409, not a 500 from an
// empty node id.
func TestStream_RefusesARetiredServer(t *testing.T) {
	srv, st := newRemovalAPI(t)
	h := srv.Handler()
	token := login(t, h)
	specID := createSpecWithInstall(t, h, token, "stream-retired", map[string]any{"script": "install.sh"})
	seedRetiredServer(t, st, "sv-stream", "node-gone", specID)
	rec := do(t, h, http.MethodGet, "/api/v1/servers/sv-stream/stream/ws?token="+token, "", nil)
	if rec.Code != http.StatusConflict || codedBody(t, rec.Body.Bytes()).Code != "server_retired" {
		t.Fatalf("stream of a retired server: %d %s, want 409 server_retired", rec.Code, rec.Body.String())
	}
}
