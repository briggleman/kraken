package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"

	"github.com/briggleman/kraken/internal/agent"
	"github.com/briggleman/kraken/internal/panel/cluster"
	"github.com/briggleman/kraken/internal/panel/rbac"
	"github.com/briggleman/kraken/internal/panel/store"
	"github.com/briggleman/kraken/internal/panel/store/memory"
	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// #354: a server deleted in the Panel kept running on its node, because the
// delete sent RemoveServer best-effort, threw the result away and deleted the
// row regardless. These pin the replacement: a removal that does not land is
// remembered on the node and finished by the node reconciler; a live node
// leaves nothing owed; and an orphan the node already has can be retired from
// the node without touching its data.

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

func pendingRemovals(t *testing.T, st *memory.Store, nodeID string) []cluster.PendingRemoval {
	t.Helper()
	n, err := st.GetNode(context.Background(), nodeID)
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	return n.PendingRemovals
}

func seedSchedule(t *testing.T, st *memory.Store, id, serverID string) {
	t.Helper()
	if err := st.CreateSchedule(context.Background(), &store.ScheduledTask{
		ID: id, ServerID: serverID, Name: "nightly", Action: store.ScheduleRestart,
		Cron: "0 4 * * *", Enabled: true, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("seed schedule: %v", err)
	}
}

func deleteServer(t *testing.T, h http.Handler, token, id string) {
	t.Helper()
	if rec := do(t, h, http.MethodDelete, "/api/v1/servers/"+id, token, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete server: status %d, body %s", rec.Code, rec.Body.String())
	}
}

// The incident itself: the node is gone when the operator deletes. The delete
// still goes through — row, schedules — and the removal is owed to the node,
// with the operator's delete_data intent. When the node answers again, the next
// node pass delivers it and forgets the debt.
func TestDeleteServer_UnreachableNodeIsRememberedAndFinishedLater(t *testing.T) {
	ctx := context.Background()
	srv, st := newTestAPI(t)
	h := srv.Handler()
	token := login(t, h)
	addr, _, stop := startStoppableAgent(t, "node-away")
	nodeID := liveNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "delete-away", map[string]any{"script": "install.sh"})
	sv := seedOfflineServer(t, st, "sv-away", nodeID, specID, nil)
	seedSchedule(t, st, "sched-away", sv.ID)

	stop() // the node goes away; the Panel still believes it is up
	deleteServer(t, h, token, sv.ID)

	if _, err := st.GetServer(ctx, sv.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("server row after delete: err %v, want ErrNotFound", err)
	}
	if left, _ := st.ListSchedulesByServer(ctx, sv.ID); len(left) != 0 {
		t.Fatalf("%d schedules outlived their server; they fire forever with \"load server\"", len(left))
	}
	owed := pendingRemovals(t, st, nodeID)
	if len(owed) != 1 {
		t.Fatalf("pending removals = %+v, want the one the node never heard about", owed)
	}
	if p := owed[0]; p.ServerID != sv.ID || !p.DeleteData || p.Attempts != 1 || p.LastError == "" || p.RequestedAt.IsZero() {
		t.Fatalf("pending removal = %+v, want %s with delete_data, one attempt and its reason", p, sv.ID)
	}

	// The fleet list the UI reads carries the debt.
	rec := do(t, h, http.MethodGet, "/api/v1/nodes", token, nil)
	var list struct {
		Nodes []struct {
			ID              string `json:"id"`
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

	// A pass while the node is still away keeps the debt.
	srv.ReconcileNodesOnceForTest(ctx)
	if owed := pendingRemovals(t, st, nodeID); len(owed) != 1 {
		t.Fatalf("a pass against a node still away dropped the debt: %+v", owed)
	}

	// The node comes back (its Agent now answers at a new address), and the
	// next pass finishes the job with the intent the operator gave.
	addr2, rt2, _ := startStoppableAgent(t, "node-away")
	n, err := st.GetNode(ctx, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	n.Address = addr2
	if err := st.UpdateNode(ctx, n); err != nil {
		t.Fatal(err)
	}
	srv.ReconcileNodesOnceForTest(ctx)

	got := rt2.Removals()
	if len(got) != 1 || got[0].ServerID != sv.ID || !got[0].DeleteData {
		t.Fatalf("removals the returning node received = %+v, want %s with delete_data", got, sv.ID)
	}
	if owed := pendingRemovals(t, st, nodeID); len(owed) != 0 {
		t.Fatalf("pending removals after the node confirmed = %+v, want none", owed)
	}
}

// An Agent that answers with a failure is owed the removal too, and the retry
// counts: each failed pass is an attempt with its reason, and the pass after
// the node recovers clears it.
func TestDeleteServer_AgentFailureIsRetriedUntilItLands(t *testing.T) {
	ctx := context.Background()
	srv, st := newTestAPI(t)
	h := srv.Handler()
	token := login(t, h)
	addr, rt := startFakeAgentRuntime(t, "node-flaky")
	nodeID := liveNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "delete-flaky", map[string]any{"script": "install.sh"})
	sv := seedOfflineServer(t, st, "sv-flaky", nodeID, specID, nil)

	rt.SetRemoveFailure("docker daemon is restarting")
	deleteServer(t, h, token, sv.ID)
	owed := pendingRemovals(t, st, nodeID)
	if len(owed) != 1 || !strings.Contains(owed[0].LastError, "docker daemon is restarting") {
		t.Fatalf("pending removals = %+v, want one carrying the Agent's reason", owed)
	}

	srv.ReconcileNodesOnceForTest(ctx)
	if owed := pendingRemovals(t, st, nodeID); len(owed) != 1 || owed[0].Attempts != 2 {
		t.Fatalf("after a failed retry: %+v, want the debt kept with two attempts", owed)
	}

	rt.SetRemoveFailure("")
	srv.ReconcileNodesOnceForTest(ctx)
	if owed := pendingRemovals(t, st, nodeID); len(owed) != 0 {
		t.Fatalf("after the node recovered: %+v, want none", owed)
	}
	if got := rt.Removals(); len(got) != 3 {
		t.Fatalf("removals = %+v, want the delete and two retries", got)
	}
}

// A delete the node confirms leaves nothing owed.
func TestDeleteServer_LiveNodeOwesNothing(t *testing.T) {
	ctx := context.Background()
	srv, st := newTestAPI(t)
	h := srv.Handler()
	token := login(t, h)
	addr, rt := startFakeAgentRuntime(t, "node-live")
	nodeID := liveNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "delete-live", map[string]any{"script": "install.sh"})
	sv := seedOfflineServer(t, st, "sv-live", nodeID, specID, nil)
	seedSchedule(t, st, "sched-live", sv.ID)

	deleteServer(t, h, token, sv.ID)

	if got := rt.Removals(); len(got) != 1 || got[0].ServerID != sv.ID || !got[0].DeleteData {
		t.Fatalf("removals = %+v, want %s with delete_data", got, sv.ID)
	}
	if owed := pendingRemovals(t, st, nodeID); len(owed) != 0 {
		t.Fatalf("pending removals = %+v, want none", owed)
	}
	if left, _ := st.ListSchedulesByServer(ctx, sv.ID); len(left) != 0 {
		t.Fatalf("%d schedules outlived their server", len(left))
	}
}

// The untracked badge's action: a container the node runs for an id the Panel
// has no row for is stopped and removed with its data left alone, and it drops
// off the node's roll call at once.
func TestRetireNodeContainer_RemovesTheOrphanAndKeepsItsData(t *testing.T) {
	ctx := context.Background()
	srv, st := newTestAPI(t)
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
	if n, _ := st.GetNode(ctx, nodeID); len(n.ManagedContainers) != 1 {
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
	if n, _ := st.GetNode(ctx, nodeID); len(n.ManagedContainers) != 0 || n.RunningServers != 0 {
		t.Fatalf("node still lists the retired container: %+v (running %d)", n.ManagedContainers, n.RunningServers)
	}
}

// Retire is for what the Panel does not own. A server with a row on this node
// is refused (409) and the Agent is never asked; its delete is the way.
func TestRetireNodeContainer_RefusesATrackedServer(t *testing.T) {
	srv, st := newTestAPI(t)
	h := srv.Handler()
	token := login(t, h)
	addr, rt := startFakeAgentRuntime(t, "node-tracked")
	nodeID := liveNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "retire-tracked", map[string]any{"script": "install.sh"})
	sv := seedOfflineServer(t, st, "sv-tracked", nodeID, specID, nil)

	rec := do(t, h, http.MethodDelete, "/api/v1/nodes/"+nodeID+"/containers/"+sv.ID, token, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("retire a tracked server: status %d, body %s — want 409", rec.Code, rec.Body.String())
	}
	var body struct{ Error, Code string }
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Code != "server_tracked" || body.Error == "" {
		t.Fatalf("body = %+v, want code server_tracked with a message", body)
	}
	if got := rt.Removals(); len(got) != 0 {
		t.Fatalf("the Agent was asked to remove a tracked server: %+v", got)
	}
}

// A node that is not there says so with a status a client can branch on.
func TestRetireNodeContainer_UnreachableNodeIs503(t *testing.T) {
	srv, _ := newTestAPI(t)
	h := srv.Handler()
	token := login(t, h)
	addr, _, stop := startStoppableAgent(t, "node-gone")
	nodeID := liveNode(t, h, token, addr)
	stop()

	rec := do(t, h, http.MethodDelete, "/api/v1/nodes/"+nodeID+"/containers/orphan-1", token, nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("retire on a gone node: status %d, body %s — want 503", rec.Code, rec.Body.String())
	}
	var body struct{ Error, Code string }
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Code != "node_unreachable" {
		t.Fatalf("body = %+v, want code node_unreachable", body)
	}
}

// The id is sent to the Agent, which names a container and a spec file after
// it, so anything but a plain id is refused before it leaves the Panel; and an
// operator — who cannot delete servers — cannot retire containers either.
func TestRetireNodeContainer_Guards(t *testing.T) {
	ctx := context.Background()
	srv, st := newTestAPI(t)
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

	if err := st.CreateUser(ctx, &store.User{ID: "op", Username: "op", RoleID: rbac.RoleOperator, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSession(ctx, &store.Session{Token: "op-token", UserID: "op", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if rec := do(t, h, http.MethodDelete, "/api/v1/nodes/"+nodeID+"/containers/orphan-1", "op-token", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("operator retire: status %d — want 403", rec.Code)
	}
	if got := rt.Removals(); len(got) != 0 {
		t.Fatalf("a refused retire reached the Agent: %+v", got)
	}
}
