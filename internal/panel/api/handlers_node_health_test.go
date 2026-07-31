package api_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"

	"google.golang.org/grpc"

	"github.com/briggleman/kraken/internal/agent"
	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// healthTogglingRuntime is a fake Agent runtime whose reported container-runtime
// health can be flipped mid-test, standing in for a Docker daemon that stops and
// comes back. Everything else behaves like the ordinary fake.
type healthTogglingRuntime struct {
	*agent.FakeRuntime

	mu     sync.Mutex
	status agentpb.RuntimeStatus
	reason string
}

func (r *healthTogglingRuntime) set(status agentpb.RuntimeStatus, reason string) {
	r.mu.Lock()
	r.status, r.reason = status, reason
	r.mu.Unlock()
}

func (r *healthTogglingRuntime) NodeInfo(ctx context.Context) (*agentpb.NodeInfo, error) {
	info, err := r.FakeRuntime.NodeInfo(ctx)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	info.RuntimeStatus, info.RuntimeError = r.status, r.reason
	r.mu.Unlock()
	return info, nil
}

// startAgentWithRuntimeStatus runs a real Agent gRPC server whose NodeInfo
// reports the given runtime health, and returns its address plus a handle for
// changing that health later.
func startAgentWithRuntimeStatus(t *testing.T, nodeID string, status agentpb.RuntimeStatus, reason string) (string, *healthTogglingRuntime) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	rt := &healthTogglingRuntime{FakeRuntime: agent.NewFakeRuntime(nodeID, "linux", true, "test"), status: status, reason: reason}
	srv := grpc.NewServer()
	agentpb.RegisterNodeServiceServer(srv, agent.NewService(rt))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return lis.Addr().String(), rt
}

// pollNode contacts the node's Agent and reconciles its stored status, the way
// the background node reconciler does.
func pollNode(t *testing.T, h http.Handler, token, id string) {
	t.Helper()
	if rec := do(t, h, http.MethodGet, "/api/v1/nodes/"+id+"/info", token, nil); rec.Code != http.StatusOK {
		t.Fatalf("node info: status %d, body %s", rec.Code, rec.Body.String())
	}
}

func nodeStatusAndReason(t *testing.T, h http.Handler, token, id string) (string, string) {
	t.Helper()
	rec := do(t, h, http.MethodGet, "/api/v1/nodes/"+id, token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get node: status %d, body %s", rec.Code, rec.Body.String())
	}
	var n struct {
		Status       string `json:"status"`
		RuntimeError string `json:"runtime_error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &n); err != nil {
		t.Fatalf("decode node: %v", err)
	}
	return n.Status, n.RuntimeError
}

// An Agent that answers but can't reach Docker is neither online nor offline.
// Reported offline it sends the operator to check the network when the fix is to
// start Docker; reported online it lets the scheduler place servers that can
// never start.
func TestNodeGoesPartialWhenRuntimeUnavailable(t *testing.T) {
	h, _ := newTestServerStore(t)
	token := login(t, h)
	const reason = "Cannot connect to the Docker daemon at unix:///var/run/docker.sock"
	addr, _ := startAgentWithRuntimeStatus(t, "partial-node", agentpb.RuntimeStatus_RUNTIME_STATUS_UNAVAILABLE, reason)
	id := registerNode(t, h, token, addr)

	// The Agent is reachable, so the info call succeeds — the degradation shows up
	// in the payload, not as a transport failure.
	rec := do(t, h, http.MethodGet, "/api/v1/nodes/"+id+"/info", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("node info: status %d, body %s", rec.Code, rec.Body.String())
	}
	var info struct {
		Status       string `json:"status"`
		RuntimeError string `json:"runtime_error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
		t.Fatalf("decode info: %v", err)
	}
	if info.Status != "partial" {
		t.Errorf("info status = %q, want %q", info.Status, "partial")
	}
	if info.RuntimeError != reason {
		t.Errorf("info runtime_error = %q, want the Agent's reason %q", info.RuntimeError, reason)
	}

	// And it is persisted, so the fleet list and the scheduler agree.
	status, why := nodeStatusAndReason(t, h, token, id)
	if status != "partial" {
		t.Errorf("stored status = %q, want %q", status, "partial")
	}
	if why != reason {
		t.Errorf("stored runtime_error = %q, want %q", why, reason)
	}
}

// A partial node must not receive placements: nothing it is given can start.
func TestPartialNodeIsNotSchedulable(t *testing.T) {
	h, _ := newTestServerStore(t)
	token := login(t, h)
	addr, _ := startAgentWithRuntimeStatus(t, "partial-node", agentpb.RuntimeStatus_RUNTIME_STATUS_UNAVAILABLE, "docker daemon is not running")
	id := registerNode(t, h, token, addr)
	pollNode(t, h, token, id)

	specID := createSpec(t, h, token, "cs2-partial")
	rec := do(t, h, http.MethodPost, "/api/v1/servers", token, map[string]any{
		"name": "should-not-place", "spec_id": specID,
	})
	if rec.Code < 400 {
		t.Fatalf("deploy onto a partial node succeeded (status %d) — the scheduler must skip it: %s", rec.Code, rec.Body.String())
	}
	// The rejection has to name the real fault; "partial" alone reads as a network
	// problem and sends the operator to the wrong place.
	if body := rec.Body.String(); !strings.Contains(body, "container runtime") {
		t.Errorf("rejection did not explain the runtime fault: %s", body)
	}
}

// An Agent built before runtime_status existed leaves the field UNSPECIFIED.
// Reading that as unavailable would turn a whole pre-upgrade fleet partial the
// moment the Panel is updated, so it must read as healthy.
func TestUnspecifiedRuntimeStatusReadsAsOnline(t *testing.T) {
	h, _ := newTestServerStore(t)
	token := login(t, h)
	addr, _ := startAgentWithRuntimeStatus(t, "old-agent", agentpb.RuntimeStatus_RUNTIME_STATUS_UNSPECIFIED, "")
	id := registerNode(t, h, token, addr)
	pollNode(t, h, token, id)

	if status, _ := nodeStatusAndReason(t, h, token, id); status != "online" {
		t.Errorf("status = %q, want %q — an agent that predates the field must not be flagged partial", status, "online")
	}
}

// Recovery is the other half: once Docker is back the node returns to online on
// its own and the stale reason is dropped.
func TestNodeReturnsToOnlineWhenRuntimeRecovers(t *testing.T) {
	h, _ := newTestServerStore(t)
	token := login(t, h)
	addr, rt := startAgentWithRuntimeStatus(t, "flappy", agentpb.RuntimeStatus_RUNTIME_STATUS_UNAVAILABLE, "docker down")
	id := registerNode(t, h, token, addr)
	pollNode(t, h, token, id)
	if status, _ := nodeStatusAndReason(t, h, token, id); status != "partial" {
		t.Fatalf("precondition: status = %q, want partial", status)
	}

	rt.set(agentpb.RuntimeStatus_RUNTIME_STATUS_OK, "")
	pollNode(t, h, token, id)

	status, why := nodeStatusAndReason(t, h, token, id)
	if status != "online" {
		t.Errorf("status = %q, want %q once the runtime recovered", status, "online")
	}
	if why != "" {
		t.Errorf("runtime_error = %q, want it cleared on recovery", why)
	}
}

// The Panel's own version rides on the node list so the UI can flag agents whose
// build doesn't match it.
func TestNodeListCarriesPanelVersion(t *testing.T) {
	h, _ := newTestServerStore(t)
	token := login(t, h)
	addr, _ := startAgentWithRuntimeStatus(t, "versioned", agentpb.RuntimeStatus_RUNTIME_STATUS_OK, "")
	id := registerNode(t, h, token, addr)
	pollNode(t, h, token, id)

	rec := do(t, h, http.MethodGet, "/api/v1/nodes", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list nodes: status %d", rec.Code)
	}
	var out struct {
		PanelVersion string `json:"panel_version"`
		Nodes        []struct {
			ID           string `json:"id"`
			AgentVersion string `json:"agent_version"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.PanelVersion == "" {
		t.Error("panel_version missing; the UI has nothing to compare agent builds against")
	}
	var found bool
	for _, n := range out.Nodes {
		if n.ID == id {
			found = true
			// The fake agent reports "test", which is deliberately not the Panel's
			// version — this is the skew the UI badges.
			if n.AgentVersion != "test" {
				t.Errorf("agent_version = %q, want the version the Agent reported (%q)", n.AgentVersion, "test")
			}
		}
	}
	if !found {
		t.Fatalf("node %s missing from the list", id)
	}
}
