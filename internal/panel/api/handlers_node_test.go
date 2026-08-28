package api_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"

	"github.com/briggleman/kraken/internal/agent"
	"github.com/briggleman/kraken/internal/panel/store"
	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// startFakeAgent starts a real Agent gRPC server on an ephemeral localhost port
// and returns its address. It is registered for cleanup.
func startFakeAgent(t *testing.T, nodeID string) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	agentpb.RegisterNodeServiceServer(srv, agent.NewService(agent.NewFakeRuntime(nodeID, "linux", true, "test")))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return lis.Addr().String()
}

func registerNode(t *testing.T, h http.Handler, token, addr string) string {
	t.Helper()
	rec := do(t, h, http.MethodPost, "/api/v1/nodes", token, map[string]any{
		"name": "abyss-node-01", "os": "linux", "wine_enabled": true,
		"address": addr, "total_memory_mb": 16384, "port_start": 27000, "port_end": 27100,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("register node: status %d, body %s", rec.Code, rec.Body.String())
	}
	var n struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &n); err != nil {
		t.Fatalf("register node decode: %v", err)
	}
	if n.ID == "" {
		t.Fatal("register node: empty id")
	}
	return n.ID
}

// Registering without a port range must fall back to the default pool — an
// empty pool would make the node permanently unschedulable (every spec needs
// at least one port).
func TestRegisterNode_DefaultsPortRange(t *testing.T) {
	h, _ := newTestServerStore(t)
	token := login(t, h)
	addr := startFakeAgent(t, "node-noports")

	rec := do(t, h, http.MethodPost, "/api/v1/nodes", token, map[string]any{
		"name": "no-ports-node", "os": "linux", "address": addr, "total_memory_mb": 8192,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("register node: status %d, body %s", rec.Code, rec.Body.String())
	}
	var n struct {
		Ports struct {
			Ranges []struct{ Start, End int } `json:"ranges"`
		} `json:"ports"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &n); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(n.Ports.Ranges) != 1 || n.Ports.Ranges[0].Start != 28000 || n.Ports.Ranges[0].End != 28999 {
		t.Fatalf("expected default port range 28000-28999, got %+v", n.Ports.Ranges)
	}
}

// PATCH /nodes/{id} edits capacity: memory and port range, independently or
// together, with validation that keeps the scheduler's math sane.
func TestUpdateNode_Capacity(t *testing.T) {
	h, _ := newTestServerStore(t)
	token := login(t, h)
	addr := startFakeAgent(t, "node-cap")
	nodeID := registerNode(t, h, token, addr) // 16384MB, ports 27000-27100

	type nodeView struct {
		TotalMemoryMB int `json:"total_memory_mb"`
		Ports         struct {
			Ranges []struct{ Start, End int } `json:"ranges"`
		} `json:"ports"`
	}

	// Memory + port range together.
	rec := do(t, h, http.MethodPatch, "/api/v1/nodes/"+nodeID, token, map[string]any{
		"total_memory_mb": 32768, "port_start": 29000, "port_end": 29999,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("update node: status %d, body %s", rec.Code, rec.Body.String())
	}
	var n nodeView
	if err := json.Unmarshal(rec.Body.Bytes(), &n); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if n.TotalMemoryMB != 32768 {
		t.Fatalf("memory not updated: %d", n.TotalMemoryMB)
	}
	if len(n.Ports.Ranges) != 1 || n.Ports.Ranges[0].Start != 29000 || n.Ports.Ranges[0].End != 29999 {
		t.Fatalf("port range not updated: %+v", n.Ports.Ranges)
	}

	// Omitted fields stay unchanged.
	rec = do(t, h, http.MethodPatch, "/api/v1/nodes/"+nodeID, token, map[string]any{"total_memory_mb": 8192})
	if rec.Code != http.StatusOK {
		t.Fatalf("memory-only update: status %d, body %s", rec.Code, rec.Body.String())
	}
	n = nodeView{}
	if err := json.Unmarshal(rec.Body.Bytes(), &n); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if n.TotalMemoryMB != 8192 || len(n.Ports.Ranges) != 1 || n.Ports.Ranges[0].Start != 29000 {
		t.Fatalf("memory-only update disturbed ports: %+v", n)
	}

	// Validation failures.
	for name, body := range map[string]map[string]any{
		"half port range":   {"port_start": 30000},
		"inverted range":    {"port_start": 300, "port_end": 200},
		"zero memory":       {"total_memory_mb": 0},
		"port out of range": {"port_start": 60000, "port_end": 70000},
	} {
		if rec := do(t, h, http.MethodPatch, "/api/v1/nodes/"+nodeID, token, body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: expected 400, got %d (%s)", name, rec.Code, rec.Body.String())
		}
	}

	if rec := do(t, h, http.MethodPatch, "/api/v1/nodes/missing", token, map[string]any{"total_memory_mb": 1024}); rec.Code != http.StatusNotFound {
		t.Errorf("missing node: expected 404, got %d", rec.Code)
	}
}

func TestPanelToAgent_NodeInfoAndPower(t *testing.T) {
	h, st := newTestServerStore(t)
	token := login(t, h)
	addr := startFakeAgent(t, "node-x")
	nodeID := registerNode(t, h, token, addr)

	// The power endpoint is now object-level authorized, so the server must exist
	// in the store. Seed it on the node (admin token has server.any → access ok).
	if err := st.CreateServer(context.Background(), &store.Server{ID: "s1", Name: "s1", NodeID: nodeID, State: store.StateRunning, CreatedAt: time.Now()}); err != nil {
		t.Fatalf("seed server: %v", err)
	}

	// Panel dials the Agent over gRPC and returns live node info.
	rec := do(t, h, http.MethodGet, "/api/v1/nodes/"+nodeID+"/info", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("node info: status %d, body %s", rec.Code, rec.Body.String())
	}
	var info struct {
		NodeID string `json:"node_id"`
		OS     string `json:"os"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
		t.Fatalf("node info decode: %v", err)
	}
	if info.NodeID != "node-x" || info.OS != "linux" {
		t.Fatalf("unexpected agent node info: %+v", info)
	}

	// The node should now be marked online in the registry.
	rec = do(t, h, http.MethodGet, "/api/v1/nodes/"+nodeID, token, nil)
	var node struct {
		Status string `json:"status"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &node)
	if node.Status != "online" {
		t.Fatalf("expected node status online after info, got %q", node.Status)
	}

	// Forward a power action through the Panel to the Agent.
	rec = do(t, h, http.MethodPost, "/api/v1/nodes/"+nodeID+"/servers/s1/power", token,
		map[string]string{"action": "start"})
	if rec.Code != http.StatusOK {
		t.Fatalf("power: status %d, body %s", rec.Code, rec.Body.String())
	}
	var pr struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &pr); err != nil {
		t.Fatalf("power decode: %v", err)
	}
	if pr.State != "SERVER_STATE_RUNNING" {
		t.Fatalf("expected RUNNING after start, got %q", pr.State)
	}
}

func TestPanelToAgent_UnreachableAgent(t *testing.T) {
	h := newTestServer(t)
	token := login(t, h)
	// Register a node pointing at a port with nothing listening.
	nodeID := registerNode(t, h, token, "127.0.0.1:1")

	rec := do(t, h, http.MethodGet, "/api/v1/nodes/"+nodeID+"/info", token, nil)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 for unreachable agent, got %d (body %s)", rec.Code, rec.Body.String())
	}
}

func TestNodes_RequirePermission(t *testing.T) {
	h := newTestServer(t)
	// No token → must be unauthorized, not a 500.
	rec := do(t, h, http.MethodGet, "/api/v1/nodes", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 listing nodes without token, got %d", rec.Code)
	}
}

// A node's name is display-only — servers reference it by UUID — so a rename
// must be accepted at any time and leave placement untouched.
func TestUpdateNodeRenames(t *testing.T) {
	h, _ := newTestServerStore(t)
	token := login(t, h)
	addr, _ := startAgentWithRuntimeStatus(t, "rename-node", agentpb.RuntimeStatus_RUNTIME_STATUS_OK, "")
	id := registerNode(t, h, token, addr)

	rec := do(t, h, http.MethodPatch, "/api/v1/nodes/"+id, token, map[string]any{"name": "abyss-prime"})
	if rec.Code != http.StatusOK {
		t.Fatalf("rename: status %d, body %s", rec.Code, rec.Body.String())
	}
	var n struct {
		Name          string `json:"name"`
		TotalMemoryMB int    `json:"total_memory_mb"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &n); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if n.Name != "abyss-prime" {
		t.Errorf("name = %q, want abyss-prime", n.Name)
	}
	// Capacity must be untouched by a name-only patch.
	if n.TotalMemoryMB != 16384 {
		t.Errorf("total_memory_mb = %d, want the registered 16384 — a rename must not disturb capacity", n.TotalMemoryMB)
	}

	// And it persists.
	rec = do(t, h, http.MethodGet, "/api/v1/nodes/"+id, token, nil)
	if err := json.Unmarshal(rec.Body.Bytes(), &n); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if n.Name != "abyss-prime" {
		t.Errorf("after reload name = %q, want abyss-prime", n.Name)
	}
}

func TestUpdateNodeRejectsBlankName(t *testing.T) {
	h, _ := newTestServerStore(t)
	token := login(t, h)
	addr, _ := startAgentWithRuntimeStatus(t, "blank-name", agentpb.RuntimeStatus_RUNTIME_STATUS_OK, "")
	id := registerNode(t, h, token, addr)

	for _, blank := range []string{"", "   "} {
		rec := do(t, h, http.MethodPatch, "/api/v1/nodes/"+id, token, map[string]any{"name": blank})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("name %q: status %d, want 400", blank, rec.Code)
		}
	}
}

func TestUpdateNodeRejectsOverlongName(t *testing.T) {
	h, _ := newTestServerStore(t)
	token := login(t, h)
	addr, _ := startAgentWithRuntimeStatus(t, "long-name", agentpb.RuntimeStatus_RUNTIME_STATUS_OK, "")
	id := registerNode(t, h, token, addr)

	rec := do(t, h, http.MethodPatch, "/api/v1/nodes/"+id, token, map[string]any{"name": strings.Repeat("x", 65)})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status %d, want 400 for a 65-character name", rec.Code)
	}
}

// A node registered before its agent answered carries a placeholder name and
// adopts the agent's id on first contact. Renaming it must win over that
// adoption — otherwise the operator's chosen name is silently overwritten the
// moment the agent shows up.
func TestUpdateNodeRenameSurvivesFirstContact(t *testing.T) {
	h, _ := newTestServerStore(t)
	token := login(t, h)
	addr, _ := startAgentWithRuntimeStatus(t, "self-reported-id", agentpb.RuntimeStatus_RUNTIME_STATUS_OK, "")

	// Register with no name: the record is identity-pending.
	rec := do(t, h, http.MethodPost, "/api/v1/nodes", token, map[string]any{
		"address": addr, "os": "linux", "total_memory_mb": 8192,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("register: status %d, body %s", rec.Code, rec.Body.String())
	}
	var created struct {
		ID              string `json:"id"`
		IdentityPending bool   `json:"identity_pending"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !created.IdentityPending {
		t.Fatal("expected the node to be identity-pending before first contact")
	}

	if rec := do(t, h, http.MethodPatch, "/api/v1/nodes/"+created.ID, token,
		map[string]any{"name": "chosen-by-operator"}); rec.Code != http.StatusOK {
		t.Fatalf("rename: status %d, body %s", rec.Code, rec.Body.String())
	}

	// First contact would otherwise adopt the agent's self-reported node id.
	pollNode(t, h, token, created.ID)

	rec = do(t, h, http.MethodGet, "/api/v1/nodes/"+created.ID, token, nil)
	var n struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &n); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if n.Name != "chosen-by-operator" {
		t.Errorf("name = %q, want the operator's rename to survive first contact", n.Name)
	}
}
