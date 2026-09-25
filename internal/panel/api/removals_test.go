package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"

	"github.com/briggleman/kraken/internal/agent"
	"github.com/briggleman/kraken/internal/panel"
	"github.com/briggleman/kraken/internal/panel/api"
	"github.com/briggleman/kraken/internal/panel/cluster"
	"github.com/briggleman/kraken/internal/panel/config"
	"github.com/briggleman/kraken/internal/panel/rbac"
	"github.com/briggleman/kraken/internal/panel/store"
	"github.com/briggleman/kraken/internal/panel/store/memory"
	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// #354: a server deleted in the Panel kept running on its node, because the
// delete sent RemoveServer best-effort, threw the result away and deleted the
// row regardless. These pin the replacement: a removal that does not land is
// remembered on the node — holding the server's memory and ports — and
// finished by the node reconciler with backoff; a live node leaves nothing
// owed; a delete that cannot be remembered does not happen; and an orphan the
// node already has can be retired without touching its data.
//
// Since #360 the removal of a live server's containers and world is the
// retire's (the row stays, retired), and the delete is the permanent delete of
// a retired server; the removal mechanics these pin are the same for both.

// flakyStore is the memory store with switches for the reads and writes a
// delete and a replay depend on, so "the store could not answer" is testable.
type flakyStore struct {
	*memory.Store
	failGetNode, failUpdateNode, failGetServer, failDeleteServer atomic.Bool
	// onUpdateServer, when set, runs after every successful UpdateServer with
	// the row as written — the moment a test changes something else "while" a
	// background job moves the row along.
	onUpdateServer atomic.Pointer[func(*store.Server)]
}

func (f *flakyStore) UpdateServer(ctx context.Context, sv *store.Server) error {
	if err := f.Store.UpdateServer(ctx, sv); err != nil {
		return err
	}
	if hook := f.onUpdateServer.Load(); hook != nil {
		(*hook)(sv)
	}
	return nil
}

var errStoreDown = errors.New("store: connection reset")

func (f *flakyStore) DeleteServer(ctx context.Context, id string) error {
	if f.failDeleteServer.Load() {
		return errStoreDown
	}
	return f.Store.DeleteServer(ctx, id)
}

// lockedBuffer is a log sink the replay goroutines can write while a test
// reads it.
type lockedBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func (f *flakyStore) GetNode(ctx context.Context, id string) (*cluster.Node, error) {
	if f.failGetNode.Load() {
		return nil, errStoreDown
	}
	return f.Store.GetNode(ctx, id)
}

func (f *flakyStore) UpdateNode(ctx context.Context, n *cluster.Node) error {
	if f.failUpdateNode.Load() {
		return errStoreDown
	}
	return f.Store.UpdateNode(ctx, n)
}

func (f *flakyStore) GetServer(ctx context.Context, id string) (*store.Server, error) {
	if f.failGetServer.Load() {
		return nil, errStoreDown
	}
	return f.Store.GetServer(ctx, id)
}

// newRemovalAPI is newTestAPI over a flakyStore.
func newRemovalAPI(t *testing.T) (*api.Server, *flakyStore) {
	t.Helper()
	return newRemovalAPILogging(t, io.Discard)
}

// newRemovalAPILogging is newRemovalAPI with the Panel's log, debug and up,
// written to w.
func newRemovalAPILogging(t *testing.T, w io.Writer) (*api.Server, *flakyStore) {
	t.Helper()
	st := &flakyStore{Store: memory.New()}
	cfg := &config.Config{
		Env: "test", SessionTTL: time.Hour,
		BootstrapAdminUser: testAdmin, BootstrapAdminPassword: testPass,
		SetupAllowedCIDRs: []string{"192.0.2.0/24"},
	}
	logger := slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelDebug}))
	if err := panel.Seed(context.Background(), st, cfg, logger); err != nil {
		t.Fatalf("seed: %v", err)
	}
	clearMustChangePassword(t, st.Store, testAdmin)
	return api.New(cfg, st, logger), st
}

// startStoppableAgent is startFakeAgentRuntime with the off switch handed back,
// for a node that goes away mid-test.
func startStoppableAgent(t *testing.T, nodeID string) (string, *agent.FakeRuntime, func()) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	rt := agent.NewFakeRuntime(nodeID, "linux", true, "test")
	srv := grpc.NewServer()
	agentpb.RegisterNodeServiceServer(srv, agent.NewService(rt))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return lis.Addr().String(), rt, srv.Stop
}

// liveNode registers a node at addr and contacts it once, so the Panel
// believes it is up.
func liveNode(t *testing.T, h http.Handler, token, addr string) string {
	t.Helper()
	id := registerNode(t, h, token, addr)
	if rec := do(t, h, http.MethodGet, "/api/v1/nodes/"+id+"/info", token, nil); rec.Code != http.StatusOK {
		t.Fatalf("node info: status %d, body %s", rec.Code, rec.Body.String())
	}
	return id
}

// placedServer seeds a server row and reserves its memory and port on the
// node, the way the scheduler would have.
func placedServer(t *testing.T, st *flakyStore, id, nodeID, specID string) *store.Server {
	t.Helper()
	ctx := context.Background()
	n, err := st.Store.GetNode(ctx, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	ports, err := n.Reserve(1024, []cluster.PortRequest{{Name: "game", Preferred: 27015}})
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if err := st.Store.UpdateNode(ctx, n); err != nil {
		t.Fatal(err)
	}
	return seedOfflineServer(t, st.Store, id, nodeID, specID, func(s *store.Server) {
		s.Ports = ports
		s.MemoryMB = 1024
	})
}

// held reports whether the node still holds a placedServer's allocation.
func held(t *testing.T, st *flakyStore, nodeID string) bool {
	t.Helper()
	n, err := st.Store.GetNode(context.Background(), nodeID)
	if err != nil {
		t.Fatal(err)
	}
	memHeld, portHeld := n.AllocatedMemoryMB == 1024, !n.Ports.IsFree(27015)
	if memHeld != portHeld {
		t.Fatalf("memory held=%v but port held=%v — they must move together", memHeld, portHeld)
	}
	return memHeld
}

func pendingRemovals(t *testing.T, st *flakyStore, nodeID string) []cluster.PendingRemoval {
	t.Helper()
	n, err := st.Store.GetNode(context.Background(), nodeID)
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	return n.PendingRemovals
}

// dueNow clears the backoff on every removal a node owes, standing in for the
// clock having moved past NextAttempt.
func dueNow(t *testing.T, st *flakyStore, nodeID string) {
	t.Helper()
	ctx := context.Background()
	n, err := st.Store.GetNode(ctx, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	for i := range n.PendingRemovals {
		n.PendingRemovals[i].NextAttempt = time.Time{}
	}
	if err := st.Store.UpdateNode(ctx, n); err != nil {
		t.Fatal(err)
	}
}

func seedSchedule(t *testing.T, st *flakyStore, id, serverID string) {
	t.Helper()
	if err := st.CreateSchedule(context.Background(), &store.ScheduledTask{
		ID: id, ServerID: serverID, Name: "nightly", Action: store.ScheduleRestart,
		Cron: "0 4 * * *", Enabled: true, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("seed schedule: %v", err)
	}
}

// deleteServer deletes a retired server permanently.
func deleteServer(t *testing.T, h http.Handler, token, id string) {
	t.Helper()
	if rec := do(t, h, http.MethodDelete, "/api/v1/servers/"+id, token, nil); rec.Code != http.StatusOK {
		t.Fatalf("delete server: status %d, body %s", rec.Code, rec.Body.String())
	}
}

func codedBody(t *testing.T, raw []byte) struct{ Error, Code string } {
	t.Helper()
	var b struct{ Error, Code string }
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return b
}

// asUser creates a user holding the given role and returns its session token.
func asUser(t *testing.T, st *flakyStore, id string, role *rbac.Role) string {
	t.Helper()
	ctx := context.Background()
	if role != nil {
		if err := st.UpsertRole(ctx, role); err != nil {
			t.Fatal(err)
		}
	}
	roleID := rbac.RoleOperator
	if role != nil {
		roleID = role.ID
	}
	if err := st.CreateUser(ctx, &store.User{ID: id, Username: id, RoleID: roleID, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSession(ctx, &store.Session{Token: id + "-token", UserID: id, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	return id + "-token"
}

// The incident itself: the node is gone when the operator retires. The retire
// still goes through — the row goes retired, its schedules switched off — and
// the removal is owed to the node with delete_data, holding the server's
// memory and ports. It is not retried before its backoff; once due and the
// node answers, the next pass delivers it — the retired row does not claim the
// id — forgets the debt and releases the allocation.
func TestRetireServer_UnreachableNodeIsRememberedAndFinishedLater(t *testing.T) {
	ctx := context.Background()
	srv, st := newRemovalAPI(t)
	h := srv.Handler()
	token := login(t, h)
	addr, _, stop := startStoppableAgent(t, "node-away")
	nodeID := liveNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "delete-away", map[string]any{"script": "install.sh"})
	sv := placedServer(t, st, "sv-away", nodeID, specID)
	seedSchedule(t, st, "sched-away", sv.ID)

	stop() // the node goes away; the Panel still believes it is up
	retireServer(t, srv, token, sv.ID)

	if row, err := st.GetServer(ctx, sv.ID); err != nil || row.State != store.StateRetired || row.RetiredFromNodeID != nodeID {
		t.Fatalf("server row after retire: %+v %v, want it retired from %s", row, err, nodeID)
	}
	if left, _ := st.ListSchedulesByServer(ctx, sv.ID); len(left) != 1 || left[0].Enabled || !left[0].DisabledByRetire {
		t.Fatalf("schedules after retire = %+v, want the one kept, switched off and flagged", left)
	}
	owed := pendingRemovals(t, st, nodeID)
	if len(owed) != 1 {
		t.Fatalf("pending removals = %+v, want the one the node never heard about", owed)
	}
	p := owed[0]
	if p.ServerID != sv.ID || !p.DeleteData || p.Attempts != 1 || p.LastError == "" || p.RequestedAt.IsZero() {
		t.Fatalf("pending removal = %+v, want %s with delete_data, one attempt and its reason", p, sv.ID)
	}
	if p.MemoryMB != 1024 || len(p.Ports) != 1 || p.Ports[0] != 27015 || !p.NextAttempt.After(time.Now()) {
		t.Fatalf("pending removal = %+v, want it holding 1024 MB and port 27015, with a retry scheduled", p)
	}
	// The container may still be running and bound: nothing is released yet.
	if !held(t, st, nodeID) {
		t.Fatal("the deleted server's memory and ports were released while its removal is still owed")
	}

	// The fleet list the UI reads carries the debt.
	rec := do(t, h, http.MethodGet, "/api/v1/nodes", token, nil)
	var list struct {
		Nodes []struct {
			PendingRemovals []struct {
				ServerID   string `json:"server_id"`
				DeleteData bool   `json:"delete_data"`
			} `json:"pending_removals"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode nodes: %v", err)
	}
	if len(list.Nodes) != 1 || len(list.Nodes[0].PendingRemovals) != 1 || list.Nodes[0].PendingRemovals[0].ServerID != sv.ID {
		t.Fatalf("GET /nodes pending_removals = %+v, want the owed removal", list.Nodes)
	}

	// A pass while the node is still away keeps the debt and the allocation.
	dueNow(t, st, nodeID)
	srv.ReconcileNodesOnceForTest(ctx)
	if owed := pendingRemovals(t, st, nodeID); len(owed) != 1 || !held(t, st, nodeID) {
		t.Fatalf("a pass against a node still away changed the debt: %+v", owed)
	}

	// The node comes back (its Agent now answers at a new address).
	addr2, rt2, _ := startStoppableAgent(t, "node-away")
	n, err := st.Store.GetNode(ctx, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	n.Address = addr2
	n.PendingRemovals[0].NextAttempt = time.Now().Add(time.Hour)
	if err := st.Store.UpdateNode(ctx, n); err != nil {
		t.Fatal(err)
	}
	// Not due yet: the backoff holds even for a node that answers.
	srv.ReconcileNodesOnceForTest(ctx)
	if got := rt2.Removals(); len(got) != 0 {
		t.Fatalf("a removal not yet due was retried: %+v", got)
	}

	dueNow(t, st, nodeID)
	srv.ReconcileNodesOnceForTest(ctx)
	got := rt2.Removals()
	if len(got) != 1 || got[0].ServerID != sv.ID || !got[0].DeleteData {
		t.Fatalf("removals the returning node received = %+v, want %s with delete_data", got, sv.ID)
	}
	if owed := pendingRemovals(t, st, nodeID); len(owed) != 0 {
		t.Fatalf("pending removals after the node confirmed = %+v, want none", owed)
	}
	if held(t, st, nodeID) {
		t.Fatal("the allocation was not released when the removal landed")
	}
}

// An Agent that answers with a failure is owed the removal too. Each failed
// retry counts, carries the Agent's own words (no gRPC framing) and pushes the
// next try back; the pass after the node recovers clears it and releases.
func TestRetireServer_AgentFailureIsRetriedUntilItLands(t *testing.T) {
	ctx := context.Background()
	srv, st := newRemovalAPI(t)
	h := srv.Handler()
	token := login(t, h)
	addr, rt := startFakeAgentRuntime(t, "node-flaky")
	nodeID := liveNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "delete-flaky", map[string]any{"script": "install.sh"})
	sv := placedServer(t, st, "sv-flaky", nodeID, specID)

	rt.SetRemoveFailure("docker daemon is restarting")
	retireServer(t, srv, token, sv.ID)
	owed := pendingRemovals(t, st, nodeID)
	if len(owed) != 1 || owed[0].LastError != "docker daemon is restarting" {
		t.Fatalf("pending removals = %+v, want one carrying the Agent's reason verbatim", owed)
	}
	if !held(t, st, nodeID) {
		t.Fatal("allocation released while the removal is owed")
	}

	dueNow(t, st, nodeID)
	before := time.Now()
	srv.ReconcileNodesOnceForTest(ctx)
	owed = pendingRemovals(t, st, nodeID)
	if len(owed) != 1 || owed[0].Attempts != 2 {
		t.Fatalf("after a failed retry: %+v, want the debt kept with two attempts", owed)
	}
	if wait := owed[0].NextAttempt.Sub(before); wait < 35*time.Second || wait > 45*time.Second {
		t.Fatalf("next attempt %v after the second failure, want ~40s", wait)
	}

	rt.SetRemoveFailure("")
	dueNow(t, st, nodeID)
	srv.ReconcileNodesOnceForTest(ctx)
	if owed := pendingRemovals(t, st, nodeID); len(owed) != 0 {
		t.Fatalf("after the node recovered: %+v, want none", owed)
	}
	if held(t, st, nodeID) {
		t.Fatal("allocation not released once the removal landed")
	}
	if got := rt.Removals(); len(got) != 3 {
		t.Fatalf("removals = %+v, want the retire's and two retries", got)
	}
}

// A retire the node confirms leaves nothing owed and releases at once; the
// permanent delete that follows takes the row and its schedules.
func TestRetireServer_LiveNodeOwesNothing(t *testing.T) {
	ctx := context.Background()
	srv, st := newRemovalAPI(t)
	h := srv.Handler()
	token := login(t, h)
	addr, rt := startFakeAgentRuntime(t, "node-live")
	nodeID := liveNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "delete-live", map[string]any{"script": "install.sh"})
	sv := placedServer(t, st, "sv-live", nodeID, specID)
	seedSchedule(t, st, "sched-live", sv.ID)

	retireServer(t, srv, token, sv.ID)

	if got := rt.Removals(); len(got) != 1 || got[0].ServerID != sv.ID || !got[0].DeleteData {
		t.Fatalf("removals = %+v, want %s with delete_data", got, sv.ID)
	}
	if owed := pendingRemovals(t, st, nodeID); len(owed) != 0 {
		t.Fatalf("pending removals = %+v, want none", owed)
	}
	if held(t, st, nodeID) {
		t.Fatal("allocation not released after a confirmed removal")
	}
	deleteServer(t, h, token, sv.ID)
	if left, _ := st.ListSchedulesByServer(ctx, sv.ID); len(left) != 0 {
		t.Fatalf("%d schedules outlived their server", len(left))
	}
}

// A permanent delete the Panel cannot remember does not happen: when the node
// record cannot be read, or the outcome cannot be written to it, the answer is
// 500 and the server row is still there.
func TestDeleteServer_RefusedWhenTheRemovalCannotBeRecorded(t *testing.T) {
	ctx := context.Background()
	srv, st := newRemovalAPI(t)
	h := srv.Handler()
	token := login(t, h)
	addr, _, stop := startStoppableAgent(t, "node-unrecorded")
	nodeID := liveNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "delete-unrecorded", map[string]any{"script": "install.sh"})
	sv := placedServer(t, st, "sv-unrecorded", nodeID, specID)
	retireServer(t, srv, token, sv.ID)
	stop()

	for name, flag := range map[string]*atomic.Bool{"node unreadable": &st.failGetNode, "node unwritable": &st.failUpdateNode} {
		flag.Store(true)
		rec := do(t, h, http.MethodDelete, "/api/v1/servers/"+sv.ID, token, nil)
		flag.Store(false)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("%s: delete = %d %s, want 500", name, rec.Code, rec.Body.String())
		}
		if _, err := st.GetServer(ctx, sv.ID); err != nil {
			t.Fatalf("%s: the row was deleted although its removal was not recorded: %v", name, err)
		}
		if owed := pendingRemovals(t, st, nodeID); len(owed) != 0 {
			t.Fatalf("%s: a pending removal was recorded for a delete that did not happen: %+v", name, owed)
		}
	}
}

// A replay that cannot tell whether the server still exists does not go
// ahead: only not-found means gone, and a removal can delete data.
func TestPendingRemoval_SkippedWhenTheServerLookupFails(t *testing.T) {
	ctx := context.Background()
	srv, st := newRemovalAPI(t)
	h := srv.Handler()
	token := login(t, h)
	addr, rt := startFakeAgentRuntime(t, "node-lookup")
	nodeID := liveNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "delete-lookup", map[string]any{"script": "install.sh"})
	sv := placedServer(t, st, "sv-lookup", nodeID, specID)

	rt.SetRemoveFailure("docker daemon is restarting")
	retireServer(t, srv, token, sv.ID)
	rt.SetRemoveFailure("")
	dueNow(t, st, nodeID)

	st.failGetServer.Store(true)
	srv.ReconcileNodesOnceForTest(ctx)
	st.failGetServer.Store(false)
	if got := rt.Removals(); len(got) != 1 {
		t.Fatalf("removals = %+v, want only the retire's — the replay must wait when it cannot look", got)
	}
	if owed := pendingRemovals(t, st, nodeID); len(owed) != 1 || owed[0].Attempts != 1 {
		t.Fatalf("pending = %+v, want it untouched", owed)
	}

	srv.ReconcileNodesOnceForTest(ctx)
	if owed := pendingRemovals(t, st, nodeID); len(owed) != 0 {
		t.Fatalf("once the store answers the replay should land: %+v", owed)
	}
}

// A permanent delete retried after an earlier attempt failed to delete the
// row, with a removal already queued (the retire's, holding the allocation):
// the retry's removal lands, and its success finishes the queued record — one
// release — rather than releasing beside it and leaving the record to release
// the same ports again later, out from under whatever was placed on them in
// between.
func TestDeleteServer_RetryAfterAFailedRowDeleteReleasesOnce(t *testing.T) {
	ctx := context.Background()
	srv, st := newRemovalAPI(t)
	h := srv.Handler()
	token := login(t, h)
	addr, rt := startFakeAgentRuntime(t, "node-retry")
	nodeID := liveNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "delete-retry", map[string]any{"script": "install.sh"})
	sv := placedServer(t, st, "sv-retry", nodeID, specID)

	// The retire's removal fails (queued, holding the allocation); then the
	// first delete's removal fails too (folded into it) and the row delete fails.
	rt.SetRemoveFailure("docker daemon is restarting")
	retireServer(t, srv, token, sv.ID)
	st.failDeleteServer.Store(true)
	if rec := do(t, h, http.MethodDelete, "/api/v1/servers/"+sv.ID, token, nil); rec.Code != http.StatusInternalServerError {
		t.Fatalf("first delete: %d %s, want 500", rec.Code, rec.Body.String())
	}
	st.failDeleteServer.Store(false)
	rt.SetRemoveFailure("")
	if owed := pendingRemovals(t, st, nodeID); len(owed) != 1 || !owed[0].DeleteBackups || !held(t, st, nodeID) {
		t.Fatalf("setup: pending %+v, want one holding the allocation, now with delete_backups", owed)
	}

	// The retry succeeds end to end.
	deleteServer(t, h, token, sv.ID)
	if owed := pendingRemovals(t, st, nodeID); len(owed) != 0 {
		t.Fatalf("after the successful retry: pending %+v, want the queued record finished", owed)
	}
	if held(t, st, nodeID) {
		t.Fatal("allocation not released by the successful retry")
	}

	// Someone else is placed on the freed port; nothing may free it again.
	n, err := st.Store.GetNode(ctx, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := n.Reserve(1024, []cluster.PortRequest{{Name: "game", Preferred: 27015}}); err != nil {
		t.Fatal(err)
	}
	if err := st.Store.UpdateNode(ctx, n); err != nil {
		t.Fatal(err)
	}
	srv.ReconcileNodesOnceForTest(ctx)
	if !held(t, st, nodeID) {
		t.Fatal("the new server's port and memory were released by a stale pending record")
	}
}

// When the node's removal landed but the outcome could not be written, the 500
// says so truthfully: the data is gone on the node, and a retry settles it.
func TestDeleteServer_UnrecordedSuccessSaysTheDataIsGone(t *testing.T) {
	ctx := context.Background()
	srv, st := newRemovalAPI(t)
	h := srv.Handler()
	token := login(t, h)
	addr, rt := startFakeAgentRuntime(t, "node-unwritten")
	nodeID := liveNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "delete-unwritten", map[string]any{"script": "install.sh"})
	sv := placedServer(t, st, "sv-unwritten", nodeID, specID)
	retireServer(t, srv, token, sv.ID)

	st.failUpdateNode.Store(true)
	rec := do(t, h, http.MethodDelete, "/api/v1/servers/"+sv.ID, token, nil)
	st.failUpdateNode.Store(false)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("delete: %d %s, want 500", rec.Code, rec.Body.String())
	}
	if msg := codedBody(t, rec.Body.Bytes()).Error; !strings.Contains(msg, "were removed on the node") || !strings.Contains(msg, "retry the delete") {
		t.Fatalf("500 says %q; it must not claim nothing happened when the node removed the data", msg)
	}
	if got := rt.Removals(); len(got) != 2 {
		t.Fatalf("removals = %+v, want the retire's and the delete's, which landed", got)
	}
	if _, err := st.GetServer(ctx, sv.ID); err != nil {
		t.Fatalf("the row went although the delete was not recorded: %v", err)
	}
	deleteServer(t, h, token, sv.ID) // and the retry settles it
}

// A store that keeps failing the lookup is warned about once, and again only
// when its reason changes — not every pass.
func TestPendingRemoval_LookupFailureWarnsOncePerReason(t *testing.T) {
	ctx := context.Background()
	logs := &lockedBuffer{}
	srv, st := newRemovalAPILogging(t, logs)
	h := srv.Handler()
	token := login(t, h)
	addr, rt := startFakeAgentRuntime(t, "node-noisy")
	nodeID := liveNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "delete-noisy", map[string]any{"script": "install.sh"})
	sv := placedServer(t, st, "sv-noisy", nodeID, specID)
	rt.SetRemoveFailure("docker daemon is restarting")
	retireServer(t, srv, token, sv.ID)
	rt.SetRemoveFailure("")

	warns := func() int {
		n := 0
		for _, line := range strings.Split(logs.String(), "\n") {
			if strings.Contains(line, "level=WARN") && strings.Contains(line, "could not check for a server") {
				n++
			}
		}
		return n
	}
	st.failGetServer.Store(true)
	for range 3 {
		dueNow(t, st, nodeID)
		srv.ReconcileNodesOnceForTest(ctx)
	}
	st.failGetServer.Store(false)
	if got := warns(); got != 1 {
		t.Fatalf("%d warnings over three passes with the same lookup failure, want 1", got)
	}
	if !strings.Contains(logs.String(), "level=DEBUG") {
		t.Fatal("the repeats were not logged at debug")
	}
	dueNow(t, st, nodeID)
	srv.ReconcileNodesOnceForTest(ctx)
	if owed := pendingRemovals(t, st, nodeID); len(owed) != 0 {
		t.Fatalf("once the store answers the replay should land: %+v", owed)
	}
}

// The health pass never waits on a replay: a removal hanging on its node
// leaves the pass free to finish, and a second pass does not start a second
// replay of the same node while the first is still running.
func TestPendingRemoval_ReplayNeverBlocksThePass(t *testing.T) {
	ctx := context.Background()
	srv, st := newRemovalAPI(t)
	h := srv.Handler()
	token := login(t, h)
	addr, rt := startFakeAgentRuntime(t, "node-hang")
	nodeID := liveNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "delete-hang", map[string]any{"script": "install.sh"})
	sv := placedServer(t, st, "sv-hang", nodeID, specID)

	rt.SetRemoveFailure("docker daemon is restarting")
	retireServer(t, srv, token, sv.ID)
	rt.SetRemoveFailure("")
	dueNow(t, st, nodeID)

	release := rt.HoldRemovals()
	t.Cleanup(release)
	done := make(chan struct{})
	go func() {
		srv.ReconcileNodesPassForTest(ctx)
		srv.ReconcileNodesPassForTest(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the node pass waited on a hanging removal")
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(rt.Removals()) < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := rt.Removals(); len(got) != 2 {
		t.Fatalf("removals = %+v, want the retire's and ONE replay — passes must not overlap on a node", got)
	}
	release()
	srv.WaitRemovalReplaysForTest()
	if owed := pendingRemovals(t, st, nodeID); len(owed) != 0 {
		t.Fatalf("after the hang cleared: %+v, want none", owed)
	}
}

// The untracked badge's action: a container the node runs for an id the Panel
// has no row for is stopped and removed with its data left alone, and it drops
// off the node's roll call at once.
func TestRetireNodeContainer_RemovesTheOrphanAndKeepsItsData(t *testing.T) {
	ctx := context.Background()
	srv, st := newRemovalAPI(t)
	h := srv.Handler()
	token := login(t, h)
	addr, rt := startFakeAgentRuntime(t, "node-orphan")
	const orphan = "f4030778-08c8-47b7-abba-59bbd9cebd08"
	if err := rt.Create(ctx, &agentpb.ServerSpec{ServerId: orphan}); err != nil {
		t.Fatal(err)
	}
	if err := rt.ApplyConfig(ctx, orphan, map[string]string{"/data/world/save.cfg": "keep me"}); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Power(ctx, orphan, agentpb.PowerAction_POWER_ACTION_START); err != nil {
		t.Fatal(err)
	}
	nodeID := liveNode(t, h, token, addr)
	if n, _ := st.Store.GetNode(ctx, nodeID); len(n.ManagedContainers) != 1 {
		t.Fatalf("setup: node reports %+v, want the orphan", n.ManagedContainers)
	}

	rec := do(t, h, http.MethodDelete, "/api/v1/nodes/"+nodeID+"/containers/"+orphan, token, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("retire: status %d, body %s", rec.Code, rec.Body.String())
	}
	if got := rt.Removals(); len(got) != 1 || got[0].ServerID != orphan || got[0].DeleteData {
		t.Fatalf("removals = %+v, want %s WITHOUT delete_data", got, orphan)
	}
	files, err := rt.ListFiles(ctx, orphan, "/data/world")
	if err != nil || len(files) != 1 {
		t.Fatalf("the orphan's data after retire: %+v (err %v), want it untouched", files, err)
	}
	if n, _ := st.Store.GetNode(ctx, nodeID); len(n.ManagedContainers) != 0 || n.RunningServers != 0 {
		t.Fatalf("node still lists the retired container: %+v (running %d)", n.ManagedContainers, n.RunningServers)
	}
}

// Retire is for what the Panel does not own. A server with a row on this node
// is refused (409 server_tracked) and the Agent is never asked.
func TestRetireNodeContainer_RefusesATrackedServer(t *testing.T) {
	srv, st := newRemovalAPI(t)
	h := srv.Handler()
	token := login(t, h)
	addr, rt := startFakeAgentRuntime(t, "node-tracked")
	nodeID := liveNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "retire-tracked", map[string]any{"script": "install.sh"})
	sv := placedServer(t, st, "sv-tracked", nodeID, specID)

	rec := do(t, h, http.MethodDelete, "/api/v1/nodes/"+nodeID+"/containers/"+sv.ID, token, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("retire a tracked server: status %d, body %s — want 409", rec.Code, rec.Body.String())
	}
	if b := codedBody(t, rec.Body.Bytes()); b.Code != "server_tracked" || b.Error == "" {
		t.Fatalf("body = %+v, want code server_tracked with a message", b)
	}
	if got := rt.Removals(); len(got) != 0 {
		t.Fatalf("the Agent was asked to remove a tracked server: %+v", got)
	}
}

// A container whose server was deleted but whose removal is still owed shows
// as untracked — and retiring it would promise the data stays while the next
// replay deletes it. Refused with 409 removal_pending.
func TestRetireNodeContainer_RefusesAnIDWithARemovalPending(t *testing.T) {
	srv, st := newRemovalAPI(t)
	h := srv.Handler()
	token := login(t, h)
	addr, rt := startFakeAgentRuntime(t, "node-owed")
	nodeID := liveNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "retire-owed", map[string]any{"script": "install.sh"})
	sv := placedServer(t, st, "sv-owed", nodeID, specID)
	rt.SetRemoveFailure("docker daemon is restarting")
	retireServer(t, srv, token, sv.ID)
	rt.SetRemoveFailure("")

	rec := do(t, h, http.MethodDelete, "/api/v1/nodes/"+nodeID+"/containers/"+sv.ID, token, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("retire with a removal pending: status %d, body %s — want 409", rec.Code, rec.Body.String())
	}
	if b := codedBody(t, rec.Body.Bytes()); b.Code != "removal_pending" {
		t.Fatalf("body = %+v, want code removal_pending", b)
	}
	if got := rt.Removals(); len(got) != 1 || !got[0].DeleteData {
		t.Fatalf("removals = %+v, want only the retire's own", got)
	}
}

// A node that is not there says so with a status a client can branch on; an
// Agent that answers with a failure is a 500 with its reason, never a 502.
func TestRetireNodeContainer_NodeFailures(t *testing.T) {
	srv, st := newRemovalAPI(t)
	_ = st
	h := srv.Handler()
	token := login(t, h)

	addr, rt := startFakeAgentRuntime(t, "node-refuses")
	nodeID := liveNode(t, h, token, addr)
	rt.SetRemoveFailure("docker daemon is restarting")
	rec := do(t, h, http.MethodDelete, "/api/v1/nodes/"+nodeID+"/containers/orphan-1", token, nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("retire refused by the agent: status %d, body %s — want 500", rec.Code, rec.Body.String())
	}
	if b := codedBody(t, rec.Body.Bytes()); b.Code != "node_error" || !strings.Contains(b.Error, "docker daemon is restarting") {
		t.Fatalf("body = %+v, want code node_error with the agent's reason", b)
	}

	addr2, _, stop := startStoppableAgent(t, "node-gone")
	goneID := liveNode(t, h, token, addr2)
	stop()
	rec = do(t, h, http.MethodDelete, "/api/v1/nodes/"+goneID+"/containers/orphan-1", token, nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("retire on a gone node: status %d, body %s — want 503", rec.Code, rec.Body.String())
	}
	if b := codedBody(t, rec.Body.Bytes()); b.Code != "node_unreachable" {
		t.Fatalf("body = %+v, want code node_unreachable", b)
	}
}

// The id is sent to the Agent, which names a container and a spec file after
// it, so anything but a plain id is refused before it leaves the Panel. Both
// permissions are needed, and either refusal carries the coded body: the
// operator lacks server.delete (the route refuses); a role with server.delete
// but without node.manage is refused by the handler.
func TestRetireNodeContainer_Guards(t *testing.T) {
	srv, st := newRemovalAPI(t)
	h := srv.Handler()
	token := login(t, h)
	addr, rt := startFakeAgentRuntime(t, "node-guard")
	nodeID := liveNode(t, h, token, addr)

	if rec := do(t, h, http.MethodDelete, "/api/v1/nodes/"+nodeID+"/containers/..bad.id", token, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("retire a malformed id: status %d, body %s — want 400", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodDelete, "/api/v1/nodes/no-such-node/containers/orphan-1", token, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("retire on an unknown node: status %d — want 404", rec.Code)
	}

	operator := asUser(t, st, "op", nil)
	deleter := asUser(t, st, "deleter", &rbac.Role{ID: "deleter", Name: "Deleter", Permissions: []rbac.Permission{
		rbac.PermServerView, rbac.PermServerDelete, rbac.PermNodeView,
	}})
	for who, tok := range map[string]string{"operator": operator, "server.delete without node.manage": deleter} {
		for _, path := range []string{"/containers/orphan-1", "/removals/orphan-1"} {
			rec := do(t, h, http.MethodDelete, "/api/v1/nodes/"+nodeID+path, tok, nil)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("%s DELETE %s: status %d — want 403", who, path, rec.Code)
			}
			if b := codedBody(t, rec.Body.Bytes()); b.Code != "forbidden" || b.Error == "" {
				t.Fatalf("%s DELETE %s: body %+v, want code forbidden with a message", who, path, b)
			}
		}
	}
	if got := rt.Removals(); len(got) != 0 {
		t.Fatalf("a refused retire reached the Agent: %+v", got)
	}
}

// A removal that will never land can be dismissed: the record goes and the
// allocation it was holding is released. Dismissing what is not owed is 404.
func TestDismissPendingRemoval(t *testing.T) {
	srv, st := newRemovalAPI(t)
	h := srv.Handler()
	token := login(t, h)
	addr, _, stop := startStoppableAgent(t, "node-dead")
	nodeID := liveNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "dismiss", map[string]any{"script": "install.sh"})
	sv := placedServer(t, st, "sv-dismiss", nodeID, specID)
	stop()
	retireServer(t, srv, token, sv.ID)

	rec := do(t, h, http.MethodDelete, "/api/v1/nodes/"+nodeID+"/removals/sv-other", token, nil)
	if rec.Code != http.StatusNotFound || codedBody(t, rec.Body.Bytes()).Code != "removal_not_found" {
		t.Fatalf("dismiss what is not owed: %d %s — want 404 removal_not_found", rec.Code, rec.Body.String())
	}
	if !held(t, st, nodeID) {
		t.Fatal("setup: allocation not held")
	}
	rec = do(t, h, http.MethodDelete, "/api/v1/nodes/"+nodeID+"/removals/"+sv.ID, token, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("dismiss: %d %s — want 204", rec.Code, rec.Body.String())
	}
	if owed := pendingRemovals(t, st, nodeID); len(owed) != 0 {
		t.Fatalf("pending after dismiss = %+v, want none", owed)
	}
	if held(t, st, nodeID) {
		t.Fatal("dismiss did not release the allocation the record held")
	}
}
