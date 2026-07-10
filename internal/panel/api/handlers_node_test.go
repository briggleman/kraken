package api_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
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
