package api_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/briggleman/kraken/internal/agent"
	"github.com/briggleman/kraken/internal/panel/api"
	"github.com/briggleman/kraken/internal/panel/store"
	"github.com/briggleman/kraken/internal/panel/store/memory"
	"github.com/briggleman/kraken/internal/shared/agentpb"
)

const restoreBackupID = "1700000000000__snap"

// restoreServerView is the part of GET /servers/{id} a restore changes.
type restoreServerView struct {
	State     string `json:"state"`
	LastError string `json:"last_error"`
	Restore   *struct {
		BackupID   string `json:"backup_id"`
		Phase      string `json:"phase"`
		BytesDone  int64  `json:"bytes_done"`
		BytesTotal int64  `json:"bytes_total"`
		StartedAt  string `json:"started_at"`
	} `json:"restore"`
}

func getRestoreView(t *testing.T, h http.Handler, token, id string) restoreServerView {
	t.Helper()
	rec := do(t, h, http.MethodGet, "/api/v1/servers/"+id, token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get server: status %d, body %s", rec.Code, rec.Body.String())
	}
	var v restoreServerView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode server: %v", err)
	}
	return v
}

// waitForRestoreView polls until ok accepts the view.
func waitForRestoreView(t *testing.T, h http.Handler, token, id string, ok func(restoreServerView) bool) restoreServerView {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		v := getRestoreView(t, h, token, id)
		if ok(v) {
			return v
		}
		if time.Now().After(deadline) {
			t.Fatalf("server %s never reached the expected restore view; last: %+v (restore %+v)", id, v, v.Restore)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// oldAgent is an Agent from before RestoreBackupStream: gRPC answers a method
// the server never registered with Unimplemented, which is what this returns.
type oldAgent struct {
	agentpb.NodeServiceServer
}

func (oldAgent) RestoreBackupStream(*agentpb.RestoreBackupRequest, agentpb.NodeService_RestoreBackupStreamServer) error {
	return status.Error(codes.Unimplemented, "unknown method RestoreBackupStream for service kraken.agent.v1.NodeService")
}

type restoreHarness struct {
	srv   *api.Server
	h     http.Handler
	st    *memory.Store
	rt    *agent.FakeRuntime
	token string
	id    string
}

// newRestoreHarness wires a Panel to a fake Agent (optionally one too old for
// the stream) and seeds one stopped server on it.
func newRestoreHarness(t *testing.T, old bool, state store.ServerState, opts ...agent.FakeOption) *restoreHarness {
	t.Helper()
	srv, st := newTestAPI(t)
	h := srv.Handler()
	token := login(t, h)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	rt := agent.NewFakeRuntime("node-restore", "linux", true, "test", opts...)
	var svc agentpb.NodeServiceServer = agent.NewService(rt)
	if old {
		svc = oldAgent{NodeServiceServer: svc}
	}
	gs := grpc.NewServer()
	agentpb.RegisterNodeServiceServer(gs, svc)
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)

	nodeID := registerNode(t, h, token, lis.Addr().String())
	sv := &store.Server{ID: "sv-restore", Name: "valheim-01", NodeID: nodeID, State: state, CreatedAt: time.Now()}
	if err := st.CreateServer(context.Background(), sv); err != nil {
		t.Fatalf("seed server: %v", err)
	}
	return &restoreHarness{srv: srv, h: h, st: st, rt: rt, token: token, id: sv.ID}
}

func (r *restoreHarness) restore(t *testing.T) *restoreServerView {
	t.Helper()
	rec := do(t, r.h, http.MethodPost, "/api/v1/servers/"+r.id+"/backups/"+restoreBackupID+"/restore", r.token, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("restore: status %d, want 202; body %s", rec.Code, rec.Body.String())
	}
	var v restoreServerView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode restore response: %v", err)
	}
	return &v
}

func (r *restoreHarness) power(t *testing.T, action string) (int, string) {
	t.Helper()
	rec := do(t, r.h, http.MethodPost, "/api/v1/servers/"+r.id+"/power", r.token, map[string]string{"action": action})
	return rec.Code, rec.Body.String()
}

// The whole of #361's Panel half in one pass: the restore answers at once with
// the server already `restoring`, the meter in GET /servers/{id} is the
// Agent's own figure, nothing can start the server or begin a second restore
// while it runs, stop still reaches the Agent without lifting the gate, and the
// row goes back to offline — with no restore block — when it lands.
func TestRestoreRunsAsAJobAndHoldsTheServer(t *testing.T) {
	gate := make(chan struct{})
	r := newRestoreHarness(t, false, store.StateOffline,
		agent.WithFakeRestoreGate(gate),
		agent.WithFakeRestoreProgress(
			&agentpb.RestoreEvent{Phase: "opening"},
			&agentpb.RestoreEvent{Phase: "extracting", BytesTotal: 1000},
			&agentpb.RestoreEvent{Phase: "extracting", BytesDone: 500, BytesTotal: 1000, Entry: "worlds/Dedicated.db"},
		),
	)
	released := false
	release := func() {
		if !released {
			released = true
			close(gate)
		}
	}
	t.Cleanup(release)

	v := r.restore(t)
	if v.State != string(store.StateRestoring) {
		t.Errorf("the 202 carries state %q, want restoring", v.State)
	}
	if v.Restore == nil || v.Restore.BackupID != restoreBackupID || v.Restore.StartedAt == "" {
		t.Errorf("the 202 should carry the job; got %+v", v.Restore)
	}

	mid := waitForRestoreView(t, r.h, r.token, r.id, func(v restoreServerView) bool {
		return v.Restore != nil && v.Restore.BytesDone == 500
	})
	if mid.State != string(store.StateRestoring) || mid.Restore.Phase != "extracting" || mid.Restore.BytesTotal != 1000 {
		t.Errorf("mid-restore view = state %q, restore %+v; want restoring, extracting 500/1000", mid.State, mid.Restore)
	}

	for _, action := range []string{"start", "restart"} {
		code, body := r.power(t, action)
		if code != http.StatusConflict || !strings.Contains(body, `"code":"server_restoring"`) {
			t.Errorf("%s while restoring: %d %s; want 409 server_restoring", action, code, body)
		}
	}
	again := do(t, r.h, http.MethodPost, "/api/v1/servers/"+r.id+"/backups/"+restoreBackupID+"/restore", r.token, nil)
	if again.Code != http.StatusConflict || !strings.Contains(again.Body.String(), `"code":"restore_in_progress"`) {
		t.Errorf("a second restore: %d %s; want 409 restore_in_progress", again.Code, again.Body.String())
	}
	if code, body := r.power(t, "stop"); code != http.StatusOK {
		t.Errorf("stop while restoring: %d %s; want 200", code, body)
	}
	if got := getRestoreView(t, r.h, r.token, r.id).State; got != string(store.StateRestoring) {
		t.Errorf("after a stop mid-restore the state is %q; the gate must hold until the restore ends", got)
	}
	if rec := do(t, r.h, http.MethodPost, "/api/v1/servers/"+r.id+"/reinstall", r.token, nil); rec.Code != http.StatusConflict {
		t.Errorf("reinstall while restoring: %d, want 409", rec.Code)
	}

	release()
	done := waitForRestoreView(t, r.h, r.token, r.id, func(v restoreServerView) bool { return v.State == string(store.StateOffline) })
	if done.Restore != nil {
		t.Errorf("a finished restore still reports a job: %+v", done.Restore)
	}
	if done.LastError != "" {
		t.Errorf("a restore that landed left last_error %q", done.LastError)
	}
	if got := r.rt.Restores(r.id); !slices.Equal(got, []string{"stream:" + restoreBackupID}) {
		t.Errorf("agent restores = %v, want exactly one streamed restore", got)
	}
	if code, body := r.power(t, "start"); code == http.StatusConflict {
		t.Errorf("start after the restore is still refused: %s", body)
	}
}

// A failure lands offline with the Agent's reason — which says whether the tree
// was rolled back — on the record, where the drill-in reads it.
func TestRestoreFailureLandsOfflineWithTheReason(t *testing.T) {
	const reason = `docker: restore stopped at "savegame" installing the restored copy: sharing violation; the live tree was rolled back to how it was before the restore`
	r := newRestoreHarness(t, false, store.StateCrashed, agent.WithFakeRestoreFailure(reason))
	r.restore(t)
	v := waitForRestoreView(t, r.h, r.token, r.id, func(v restoreServerView) bool { return v.State != string(store.StateRestoring) })
	if v.State != string(store.StateOffline) {
		t.Errorf("state after a failed restore = %q, want offline", v.State)
	}
	if !strings.HasPrefix(v.LastError, "restore failed: ") || !strings.Contains(v.LastError, "rolled back") {
		t.Errorf("last_error = %q; want the agent's reason behind \"restore failed: \"", v.LastError)
	}
	if v.Restore != nil {
		t.Errorf("a failed restore still reports a job: %+v", v.Restore)
	}
}

// A restore puts save files back; it does not repair an install. A server that
// was install_failed goes back there, reinstall gate and reason intact.
func TestRestoreFromInstallFailedKeepsTheInstallGate(t *testing.T) {
	r := newRestoreHarness(t, false, store.StateInstallFailed)
	sv, _ := r.st.GetServer(context.Background(), r.id)
	sv.LastError = "install failed: steamcmd exited 8"
	if err := r.st.UpdateServer(context.Background(), sv); err != nil {
		t.Fatalf("seed last_error: %v", err)
	}
	r.restore(t)
	v := waitForRestoreView(t, r.h, r.token, r.id, func(v restoreServerView) bool { return v.State != string(store.StateRestoring) })
	if v.State != string(store.StateInstallFailed) || v.LastError != "install failed: steamcmd exited 8" {
		t.Errorf("after restoring an install_failed server: state %q, last_error %q; want install_failed with the install's reason", v.State, v.LastError)
	}
}

// An Agent older than the stream answers Unimplemented; the Panel falls back to
// the unary call and the meter stays indeterminate (bytes_total 0) rather than
// inventing a figure — and the restore still completes.
func TestRestoreFallsBackToUnaryOnAnOldAgent(t *testing.T) {
	gate := make(chan struct{})
	r := newRestoreHarness(t, true, store.StateOffline, agent.WithFakeRestoreGate(gate))
	released := false
	t.Cleanup(func() {
		if !released {
			close(gate)
		}
	})
	r.restore(t)
	mid := waitForRestoreView(t, r.h, r.token, r.id, func(v restoreServerView) bool {
		return v.Restore != nil && v.Restore.Phase == "restoring"
	})
	if mid.State != string(store.StateRestoring) || mid.Restore.BytesTotal != 0 {
		t.Errorf("an old agent's restore reads state %q, total %d; want restoring with bytes_total 0", mid.State, mid.Restore.BytesTotal)
	}
	released = true
	close(gate)
	waitForState(t, r.h, r.token, r.id, string(store.StateOffline))
	if got := r.rt.Restores(r.id); !slices.Equal(got, []string{"unary:" + restoreBackupID}) {
		t.Errorf("agent restores = %v, want the unary fallback exactly once", got)
	}
}

// The reconciler adopts a running container for a stopped row (#328). A
// restoring row is not stopped in that sense — the job owns it — so a container
// the Agent reports running must not flip it, which would lift the start gate.
func TestReconcilerLeavesARestoringServerAlone(t *testing.T) {
	gate := make(chan struct{})
	r := newRestoreHarness(t, false, store.StateOffline, agent.WithFakeRestoreGate(gate))
	defer close(gate)
	if _, err := r.rt.Power(context.Background(), r.id, agentpb.PowerAction_POWER_ACTION_START); err != nil {
		t.Fatalf("fake start: %v", err)
	}
	r.restore(t)
	r.srv.ReconcileOnceForTest(context.Background())
	if got := getRestoreView(t, r.h, r.token, r.id).State; got != string(store.StateRestoring) {
		t.Errorf("after a reconcile pass the restoring server reads %q", got)
	}
}

// A `restoring` row with no job in this process is what a Panel restart
// mid-restore leaves behind. The reconciler settles it instead of letting it
// hold the start gate forever, and says why.
func TestReconcilerSettlesAnOrphanedRestore(t *testing.T) {
	r := newRestoreHarness(t, false, store.StateRestoring)
	r.srv.ReconcileOnceForTest(context.Background())
	v := getRestoreView(t, r.h, r.token, r.id)
	if v.State != string(store.StateOffline) {
		t.Errorf("an orphaned restoring row reads %q after a reconcile, want offline", v.State)
	}
	if !strings.Contains(v.LastError, "panel restarted") {
		t.Errorf("last_error = %q; want the reason the restore's outcome is unknown", v.LastError)
	}
}
