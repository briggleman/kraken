package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
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
		PrevState  string `json:"prev_state"`
		Phase      string `json:"phase"`
		BytesDone  int64  `json:"bytes_done"`
		BytesTotal int64  `json:"bytes_total"`
		StartedAt  string `json:"started_at"`
	} `json:"restore"`
	RestoreResult *struct {
		BackupID   string `json:"backup_id"`
		OK         bool   `json:"ok"`
		Error      string `json:"error"`
		FinishedAt string `json:"finished_at"`
	} `json:"restore_result"`
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

func settled(v restoreServerView) bool { return v.State != string(store.StateRestoring) }

// oldAgent is an Agent from before RestoreBackupStream: gRPC answers a method
// the server never registered with Unimplemented, which is what this returns.
type oldAgent struct{ agentpb.NodeServiceServer }

func (oldAgent) RestoreBackupStream(*agentpb.RestoreBackupRequest, agentpb.NodeService_RestoreBackupStreamServer) error {
	return status.Error(codes.Unimplemented, "unknown method RestoreBackupStream for service kraken.agent.v1.NodeService")
}

// unimplementedMidStream reports progress and THEN answers Unimplemented — not
// an old Agent, whatever the code says, and so never a reason to replay the
// restore through the unary call.
type unimplementedMidStream struct{ agentpb.NodeServiceServer }

func (unimplementedMidStream) RestoreBackupStream(_ *agentpb.RestoreBackupRequest, s agentpb.NodeService_RestoreBackupStreamServer) error {
	if err := s.Send(&agentpb.RestoreEvent{Phase: "extracting", BytesDone: 10, BytesTotal: 100}); err != nil {
		return err
	}
	return status.Error(codes.Unimplemented, "a proxy that lost the method mid-call")
}

// unavailableAgent refuses the stream outright with a code other than
// Unimplemented: nothing ran, and nothing may fall back.
type unavailableAgent struct{ agentpb.NodeServiceServer }

func (unavailableAgent) RestoreBackupStream(*agentpb.RestoreBackupRequest, agentpb.NodeService_RestoreBackupStreamServer) error {
	return status.Error(codes.Unavailable, "the agent is shutting down")
}

type restoreHarness struct {
	srv   *api.Server
	h     http.Handler
	st    *memory.Store
	rt    *agent.FakeRuntime
	token string
	id    string
}

// newRestoreHarness wires a Panel to a fake Agent (wrapped, to stand in for an
// older or misbehaving one) and seeds one server built from a real spec, in
// the given state and freshly provisioned — so a start skips the update pass
// unless a test says otherwise.
func newRestoreHarness(t *testing.T, wrap func(agentpb.NodeServiceServer) agentpb.NodeServiceServer, state store.ServerState, opts ...agent.FakeOption) *restoreHarness {
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
	if wrap != nil {
		svc = wrap(svc)
	}
	gs := grpc.NewServer()
	agentpb.RegisterNodeServiceServer(gs, svc)
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)

	nodeID := registerNode(t, h, token, lis.Addr().String())
	specID := createSpecWithBackup(t, h, token, "restorable-"+strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-")), nil)
	now := time.Now()
	sv := seedOfflineServer(t, st, "sv-restore", nodeID, specID, func(s *store.Server) {
		s.State = state
		s.ProvisionedAt = &now
	})
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

func (r *restoreHarness) setRow(t *testing.T, mutate func(*store.Server)) {
	t.Helper()
	sv, err := r.st.GetServer(context.Background(), r.id)
	if err != nil {
		t.Fatalf("load server: %v", err)
	}
	mutate(sv)
	if err := r.st.UpdateServer(context.Background(), sv); err != nil {
		t.Fatalf("update server: %v", err)
	}
}

// gate is a closable held-open restore for WithFakeRestoreGate.
type gate struct {
	ch   chan struct{}
	once sync.Once
}

func newGate(t *testing.T) *gate {
	g := &gate{ch: make(chan struct{})}
	t.Cleanup(g.release)
	return g
}

func (g *gate) release() { g.once.Do(func() { close(g.ch) }) }

// The whole of #361's Panel half in one pass: the restore answers at once with
// the server already `restoring` and the prior state recorded on the row, the
// meter in GET /servers/{id} is the Agent's own figure, nothing can start the
// server or begin a second restore while it runs, stop still reaches the Agent
// without lifting the gate, and the row goes back — with a restore_result and
// no restore block — when it lands.
func TestRestoreRunsAsAJobAndHoldsTheServer(t *testing.T) {
	g := newGate(t)
	r := newRestoreHarness(t, nil, store.StateOffline,
		agent.WithFakeRestoreGate(g.ch),
		agent.WithFakeRestoreProgress(
			&agentpb.RestoreEvent{Phase: "opening"},
			&agentpb.RestoreEvent{Phase: "extracting", BytesTotal: 1000},
			&agentpb.RestoreEvent{Phase: "extracting", BytesDone: 500, BytesTotal: 1000, Entry: "worlds/Dedicated.db"},
		),
	)

	v := r.restore(t)
	if v.State != string(store.StateRestoring) {
		t.Errorf("the 202 carries state %q, want restoring", v.State)
	}
	if v.Restore == nil || v.Restore.BackupID != restoreBackupID || v.Restore.StartedAt == "" || v.Restore.PrevState != "offline" {
		t.Errorf("the 202 should carry the job and the state to return to; got %+v", v.Restore)
	}
	row, _ := r.st.GetServer(context.Background(), r.id)
	if row.Restore == nil || row.Restore.PrevState != store.StateOffline || row.Restore.BackupID != restoreBackupID {
		t.Errorf("the row itself must record the restore (durable across a Panel restart); got %+v", row.Restore)
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

	g.release()
	done := waitForRestoreView(t, r.h, r.token, r.id, settled)
	if done.State != string(store.StateOffline) {
		t.Errorf("state after the restore = %q, want offline", done.State)
	}
	if done.Restore != nil {
		t.Errorf("a finished restore still reports a job: %+v", done.Restore)
	}
	if done.RestoreResult == nil || !done.RestoreResult.OK || done.RestoreResult.BackupID != restoreBackupID || done.RestoreResult.FinishedAt == "" {
		t.Errorf("restore_result = %+v; want ok for %s with a finish time", done.RestoreResult, restoreBackupID)
	}
	if got := r.rt.Restores(r.id); !slices.Equal(got, []string{"stream:" + restoreBackupID}) {
		t.Errorf("agent restores = %v, want exactly one streamed restore", got)
	}
	if code, body := r.power(t, "start"); strings.Contains(body, "server_restoring") {
		t.Errorf("start after the restore is still refused as restoring: %d %s", code, body)
	}
}

// A failure lands where the server came from, with the Agent's reason — which
// says whether the tree was rolled back — in restore_result. last_error is not
// the restore's to write.
func TestRestoreFailureRecordsTheReasonInRestoreResult(t *testing.T) {
	const reason = `docker: restore stopped at "savegame" installing the restored copy: sharing violation; the live tree was rolled back to how it was before the restore`
	r := newRestoreHarness(t, nil, store.StateCrashed, agent.WithFakeRestoreFailure(reason))
	r.restore(t)
	v := waitForRestoreView(t, r.h, r.token, r.id, settled)
	if v.State != string(store.StateCrashed) {
		t.Errorf("state after a failed restore = %q, want crashed (where it came from)", v.State)
	}
	if v.RestoreResult == nil || v.RestoreResult.OK || v.RestoreResult.Error != reason {
		t.Errorf("restore_result = %+v; want not ok with the agent's reason", v.RestoreResult)
	}
	if v.LastError != "" {
		t.Errorf("a failed restore wrote last_error %q", v.LastError)
	}
	if v.Restore != nil {
		t.Errorf("a failed restore still reports a job: %+v", v.Restore)
	}
}

// A restore puts save files back; it does not repair an install. An
// install_failed server goes back there with its install's reason intact —
// whether the restore landed or failed — and a later good restore reads as
// good, not as the earlier failure.
func TestRestoreFromInstallFailedKeepsTheInstallGateAndReason(t *testing.T) {
	const install = "install failed: steamcmd exited 8"
	for _, tc := range []struct {
		name    string
		failure string
	}{{"landed", ""}, {"failed", "gzip: invalid header; the live tree was not touched"}} {
		t.Run(tc.name, func(t *testing.T) {
			var opts []agent.FakeOption
			if tc.failure != "" {
				opts = append(opts, agent.WithFakeRestoreFailure(tc.failure))
			}
			r := newRestoreHarness(t, nil, store.StateInstallFailed, opts...)
			r.setRow(t, func(s *store.Server) { s.LastError = install })
			r.restore(t)
			v := waitForRestoreView(t, r.h, r.token, r.id, settled)
			if v.State != string(store.StateInstallFailed) || v.LastError != install {
				t.Errorf("after the restore: state %q, last_error %q; want install_failed with the install's reason", v.State, v.LastError)
			}
			if v.RestoreResult == nil || v.RestoreResult.OK != (tc.failure == "") {
				t.Errorf("restore_result = %+v", v.RestoreResult)
			}
		})
	}
}

// A Panel restart mid-restore loses the job but not the row: the orphan settle
// returns the server to the state the row recorded — an install_failed server
// keeps its reinstall gate — and records the unknown outcome in
// restore_result, not last_error.
func TestReconcilerSettlesAnOrphanedRestoreToItsPriorState(t *testing.T) {
	r := newRestoreHarness(t, nil, store.StateRestoring)
	r.setRow(t, func(s *store.Server) {
		s.LastError = "install failed: steamcmd exited 8"
		s.Restore = &store.ServerRestore{BackupID: restoreBackupID, PrevState: store.StateInstallFailed, StartedAt: time.Now()}
	})
	r.srv.ReconcileOnceForTest(context.Background())
	v := getRestoreView(t, r.h, r.token, r.id)
	if v.State != string(store.StateInstallFailed) {
		t.Errorf("an orphaned restore of an install_failed server settled to %q; the reinstall gate was lifted", v.State)
	}
	if v.LastError != "install failed: steamcmd exited 8" {
		t.Errorf("last_error = %q; the orphan settle must leave the install's reason", v.LastError)
	}
	if v.RestoreResult == nil || v.RestoreResult.OK || !strings.Contains(v.RestoreResult.Error, "panel restarted") ||
		!strings.Contains(v.RestoreResult.Error, ".kraken-aside-") {
		t.Errorf("restore_result = %+v; want the unknown outcome and where the originals would be", v.RestoreResult)
	}
	if v.Restore != nil {
		t.Errorf("the settled row still carries a restore: %+v", v.Restore)
	}
}

// A row that someone else moved while the restore ran keeps what they wrote;
// the job records its result and leaves the state alone.
func TestFinishRestoreLeavesAStateSomeoneElseWrote(t *testing.T) {
	g := newGate(t)
	r := newRestoreHarness(t, nil, store.StateOffline, agent.WithFakeRestoreGate(g.ch))
	r.restore(t)
	r.setRow(t, func(s *store.Server) { s.State = store.StateCrashed })
	g.release()
	v := waitForRestoreView(t, r.h, r.token, r.id, func(v restoreServerView) bool { return v.RestoreResult != nil })
	if v.State != string(store.StateCrashed) {
		t.Errorf("the job overwrote a state someone else wrote: %q", v.State)
	}
}

// An Agent older than the stream answers Unimplemented; the Panel falls back to
// the unary call and the meter stays indeterminate (bytes_total 0) rather than
// inventing a figure — and the restore still completes.
func TestRestoreFallsBackToUnaryOnAnOldAgent(t *testing.T) {
	g := newGate(t)
	r := newRestoreHarness(t, func(s agentpb.NodeServiceServer) agentpb.NodeServiceServer { return oldAgent{s} },
		store.StateOffline, agent.WithFakeRestoreGate(g.ch))
	r.restore(t)
	mid := waitForRestoreView(t, r.h, r.token, r.id, func(v restoreServerView) bool {
		return v.Restore != nil && v.Restore.Phase == "restoring"
	})
	if mid.State != string(store.StateRestoring) || mid.Restore.BytesTotal != 0 {
		t.Errorf("an old agent's restore reads state %q, total %d; want restoring with bytes_total 0", mid.State, mid.Restore.BytesTotal)
	}
	g.release()
	v := waitForRestoreView(t, r.h, r.token, r.id, settled)
	if v.RestoreResult == nil || !v.RestoreResult.OK {
		t.Errorf("restore_result = %+v, want ok", v.RestoreResult)
	}
	if got := r.rt.Restores(r.id); !slices.Equal(got, []string{"unary:" + restoreBackupID}) {
		t.Errorf("agent restores = %v, want the unary fallback exactly once", got)
	}
}

// The fallback is for an old Agent only: an Unimplemented after progress, or
// any other code at all, never replays the restore through the unary call.
func TestRestoreNeverFallsBackExceptForAnOldAgent(t *testing.T) {
	for _, tc := range []struct {
		name string
		wrap func(agentpb.NodeServiceServer) agentpb.NodeServiceServer
		want string
	}{
		{"unimplemented after progress", func(s agentpb.NodeServiceServer) agentpb.NodeServiceServer { return unimplementedMidStream{s} },
			"the restore stream was interrupted"},
		{"unavailable before anything ran", func(s agentpb.NodeServiceServer) agentpb.NodeServiceServer { return unavailableAgent{s} },
			"the restore did not start"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRestoreHarness(t, tc.wrap, store.StateOffline)
			r.restore(t)
			v := waitForRestoreView(t, r.h, r.token, r.id, settled)
			if v.RestoreResult == nil || v.RestoreResult.OK || !strings.Contains(v.RestoreResult.Error, tc.want) {
				t.Errorf("restore_result = %+v; want a failure saying %q", v.RestoreResult, tc.want)
			}
			for _, call := range r.rt.Restores(r.id) {
				if strings.HasPrefix(call, "unary:") {
					t.Errorf("the restore was replayed through the unary call (%v)", r.rt.Restores(r.id))
				}
			}
		})
	}
}

// The reconciler adopts a running container for a stopped row (#328). A
// restoring row is not stopped in that sense — the job owns it — so a container
// the Agent reports running must not flip it, which would lift the start gate.
func TestReconcilerLeavesARestoringServerAlone(t *testing.T) {
	g := newGate(t)
	r := newRestoreHarness(t, nil, store.StateOffline, agent.WithFakeRestoreGate(g.ch))
	if _, err := r.rt.Power(context.Background(), r.id, agentpb.PowerAction_POWER_ACTION_START); err != nil {
		t.Fatalf("fake start: %v", err)
	}
	r.restore(t)
	r.srv.ReconcileOnceForTest(context.Background())
	if got := getRestoreView(t, r.h, r.token, r.id).State; got != string(store.StateRestoring) {
		t.Errorf("after a reconcile pass the restoring server reads %q", got)
	}
}

// Every writer of the server's tree is refused while a restore swaps it; reads
// and downloads are not.
func TestWritersAreRefusedWhileRestoring(t *testing.T) {
	g := newGate(t)
	r := newRestoreHarness(t, nil, store.StateOffline, agent.WithFakeRestoreGate(g.ch))
	r.restore(t)

	base := "/api/v1/servers/" + r.id
	gated := []struct {
		name, method, path string
		body               any
	}{
		{"backup create", http.MethodPost, base + "/backups", map[string]string{"name": "snap"}},
		{"backup delete", http.MethodDelete, base + "/backups/" + restoreBackupID, nil},
		{"server delete", http.MethodDelete, base, nil},
		{"reinstall", http.MethodPost, base + "/reinstall", nil},
		{"settings save", http.MethodPut, base + "/settings", map[string]any{"values": map[string]string{}}},
		{"file write", http.MethodPost, base + "/files/write", map[string]string{"path": "a.txt", "content": "x"}},
		{"file delete", http.MethodPost, base + "/files/delete", map[string]any{"paths": []string{"a.txt"}}},
		{"mkdir", http.MethodPost, base + "/files/mkdir", map[string]string{"path": "d"}},
		{"move", http.MethodPost, base + "/files/move", map[string]string{"src": "a.txt", "dst": "b.txt"}},
		{"copy", http.MethodPost, base + "/files/copy", map[string]string{"src": "a.txt", "dst": "b.txt"}},
	}
	for _, tc := range gated {
		rec := do(t, r.h, tc.method, tc.path, r.token, tc.body)
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"code":"server_restoring"`) {
			t.Errorf("%s while restoring: %d %s; want 409 server_restoring", tc.name, rec.Code, rec.Body.String())
		}
	}
	if rec := doUpload(t, r.h, base+"/files/upload", r.token); rec.Code != http.StatusConflict ||
		!strings.Contains(rec.Body.String(), `"code":"server_restoring"`) {
		t.Errorf("upload while restoring: %d %s; want 409 server_restoring", rec.Code, rec.Body.String())
	}
	settingsRec := do(t, r.h, http.MethodPut, base+"/settings", r.token, map[string]any{"values": map[string]string{}})
	if !strings.Contains(settingsRec.Body.String(), "wait for the restore to finish") {
		t.Errorf("the settings refusal should say to wait for the restore: %s", settingsRec.Body.String())
	}
	for _, path := range []string{base + "/files?path=.", base + "/backups"} {
		if rec := do(t, r.h, http.MethodGet, path, r.token, nil); rec.Code == http.StatusConflict {
			t.Errorf("GET %s is a read and must not be refused: %s", path, rec.Body.String())
		}
	}
	if row, err := r.st.GetServer(context.Background(), r.id); err != nil || row.State != store.StateRestoring {
		t.Errorf("a refused writer changed the row: %v %+v", err, row)
	}
}

// doUpload posts a one-file multipart upload.
func doUpload(t *testing.T, h http.Handler, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("files", "a.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fw.Write([]byte("x"))
	_ = mw.WriteField("path", ".")
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, path, &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// Scheduled backups, commands and replication skip while a restore runs, with
// the reason in the schedule's last_error — a 04:00 backup would otherwise
// archive a half-swapped tree and could evict the archive being read.
func TestScheduledActionsSkipWhileRestoring(t *testing.T) {
	g := newGate(t)
	r := newRestoreHarness(t, nil, store.StateOffline, agent.WithFakeRestoreGate(g.ch))
	r.restore(t)
	due := time.Now().Add(-time.Minute)
	for _, a := range []store.ScheduleAction{store.ScheduleBackup, store.ScheduleCommand, store.ScheduleReplicate} {
		if err := r.st.CreateSchedule(context.Background(), &store.ScheduledTask{
			ID: "sch-" + string(a), ServerID: r.id, Name: string(a), Action: a, Command: "save",
			Cron: "0 4 * * *", Enabled: true, NextRunAt: &due, CreatedAt: time.Now(),
		}); err != nil {
			t.Fatalf("seed schedule: %v", err)
		}
	}
	r.srv.RunDueSchedulesForTest(context.Background())
	for _, a := range []store.ScheduleAction{store.ScheduleBackup, store.ScheduleCommand, store.ScheduleReplicate} {
		task, err := r.st.GetSchedule(context.Background(), "sch-"+string(a))
		if err != nil {
			t.Fatalf("get schedule: %v", err)
		}
		if !strings.Contains(task.LastError, "backup restore is in progress") {
			t.Errorf("scheduled %s: last_error %q, want the restore named as the reason it was skipped", a, task.LastError)
		}
	}
	list := do(t, r.h, http.MethodGet, "/api/v1/servers/"+r.id+"/backups", r.token, nil)
	if strings.Contains(list.Body.String(), "scheduled-") {
		t.Errorf("a scheduled backup ran during the restore: %s", list.Body.String())
	}
}

// A scheduled restart that is in flight when a restore takes the row must not
// write the Agent's state over `restoring` afterwards.
func TestScheduledRestartDoesNotWriteOverARestore(t *testing.T) {
	r := newRestoreHarness(t, nil, store.StateRunning)
	seedDueRestart(t, r.st, "sch-restart", r.id)
	r.rt.SetPowerHook(func(serverID string, action agentpb.PowerAction) {
		if action == agentpb.PowerAction_POWER_ACTION_RESTART {
			r.setRow(t, func(s *store.Server) { s.State = store.StateRestoring })
		}
	})
	r.srv.RunDueSchedulesForTest(context.Background())
	if row, _ := r.st.GetServer(context.Background(), r.id); row.State != store.StateRestoring {
		t.Errorf("the scheduler wrote %q over a restore that took the row mid-restart", row.State)
	}
}

// A restore registered after a start's gate but before its state write or
// Agent call must still win: the start re-checks right before acting.
func TestStartThatRacesARestoreLoses(t *testing.T) {
	for _, tc := range []struct {
		name   string
		update bool // take the update-on-start path (writes `installing`)
		call   func(t *testing.T, r *restoreHarness) (int, string)
	}{
		{"start", false, func(t *testing.T, r *restoreHarness) (int, string) { return r.power(t, "start") }},
		{"start with update pass", true, func(t *testing.T, r *restoreHarness) (int, string) { return r.power(t, "start") }},
		{"reinstall", false, func(t *testing.T, r *restoreHarness) (int, string) {
			rec := do(t, r.h, http.MethodPost, "/api/v1/servers/"+r.id+"/reinstall", r.token, nil)
			return rec.Code, rec.Body.String()
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newGate(t)
			r := newRestoreHarness(t, nil, store.StateOffline, agent.WithFakeRestoreGate(g.ch))
			if tc.update {
				r.setRow(t, func(s *store.Server) { s.ProvisionedAt = nil })
			}
			var once sync.Once
			clear := api.SetRestoreClaimHookForTest(func(serverID string) {
				once.Do(func() {
					rec := do(t, r.h, http.MethodPost, "/api/v1/servers/"+r.id+"/backups/"+restoreBackupID+"/restore", r.token, nil)
					if rec.Code != http.StatusAccepted {
						t.Errorf("the racing restore: %d %s", rec.Code, rec.Body.String())
					}
				})
			})
			defer clear()
			code, body := tc.call(t, r)
			if code != http.StatusConflict || !strings.Contains(body, `"code":"server_restoring"`) {
				t.Errorf("%s racing a restore: %d %s; want 409 server_restoring", tc.name, code, body)
			}
			if row, _ := r.st.GetServer(context.Background(), r.id); row.State != store.StateRestoring {
				t.Errorf("the row reads %q; the racing %s wrote over the restore", row.State, tc.name)
			}
			if got := agentState(t, r.rt, r.id); got == agentpb.ServerState_SERVER_STATE_RUNNING {
				t.Errorf("the Agent was told to start the server over the restore")
			}
		})
	}
}
