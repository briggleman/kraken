package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// registerNamedNode is registerNode with a caller-chosen name and port range,
// so tests can stand up several distinguishable nodes.
func registerNamedNode(t *testing.T, h http.Handler, token, name, addr string, portStart, portEnd int) string {
	t.Helper()
	rec := do(t, h, http.MethodPost, "/api/v1/nodes", token, map[string]any{
		"name": name, "os": "linux", "wine_enabled": true,
		"address": addr, "total_memory_mb": 16384, "port_start": portStart, "port_end": portEnd,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("register %s: status %d, body %s", name, rec.Code, rec.Body.String())
	}
	var n struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &n)
	if n.ID == "" {
		t.Fatalf("register %s: empty id", name)
	}
	return n.ID
}

// TestCreateServerPinnedPlacement — the wizard's node selection is a real pin:
// the server lands on exactly the requested node, an unknown pin is a 404, and
// an ineligible pin is refused with the reason instead of placing elsewhere.
// (Before this, the placement step was silently advisory — a deploy "aimed" at
// one node could land on another, observed live during the tunnel drill.)
func TestCreateServerPinnedPlacement(t *testing.T) {
	h := newTestServer(t)
	token := login(t, h)

	addrA := startFakeAgent(t, "node-a")
	addrB := startFakeAgent(t, "node-b")
	idA := registerNamedNode(t, h, token, "node-a", addrA, 27000, 27100)
	idB := registerNamedNode(t, h, token, "node-b", addrB, 28000, 28100)
	pollNode(t, h, token, idA)
	pollNode(t, h, token, idB)

	specID := createSpec(t, h, token, "cs2-pinned")

	// Pin each node in turn; the placement must honor the pin exactly.
	for name, want := range map[string]string{"pin-a": idA, "pin-b": idB} {
		rec := do(t, h, http.MethodPost, "/api/v1/servers", token, map[string]any{
			"name": name, "spec_id": specID, "node_id": want,
		})
		if rec.Code != http.StatusCreated {
			t.Fatalf("%s: status %d, body %s", name, rec.Code, rec.Body.String())
		}
		var sv struct {
			NodeID string `json:"node_id"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &sv)
		if sv.NodeID != want {
			t.Fatalf("%s landed on %s, want %s — the pin must be binding", name, sv.NodeID, want)
		}
	}

	// A pin to a node that doesn't exist is its own error, not a fallback.
	rec := do(t, h, http.MethodPost, "/api/v1/servers", token, map[string]any{
		"name": "pin-ghost", "spec_id": specID, "node_id": "no-such-node",
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("ghost pin: status %d, body %s", rec.Code, rec.Body.String())
	}
}

// TestCreateServerPinnedToIneligibleNode — pinning to a node the scheduler
// would skip (here: partial — container runtime down) must refuse with the
// node's name and the scheduler's reason, never place on a different node.
func TestCreateServerPinnedToIneligibleNode(t *testing.T) {
	h := newTestServer(t)
	token := login(t, h)

	// A healthy node the scheduler WOULD pick, plus the broken pin target.
	healthyAddr := startFakeAgent(t, "healthy")
	healthyID := registerNamedNode(t, h, token, "healthy", healthyAddr, 27000, 27100)
	pollNode(t, h, token, healthyID)

	brokenAddr, _ := startAgentWithRuntimeStatus(t, "broken", agentpb.RuntimeStatus_RUNTIME_STATUS_UNAVAILABLE, "docker daemon is not running")
	brokenID := registerNamedNode(t, h, token, "broken", brokenAddr, 28000, 28100)
	pollNode(t, h, token, brokenID)

	specID := createSpec(t, h, token, "cs2-broken-pin")
	rec := do(t, h, http.MethodPost, "/api/v1/servers", token, map[string]any{
		"name": "pin-broken", "spec_id": specID, "node_id": brokenID,
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("broken pin: status %d, body %s — must refuse, not fall back to the healthy node", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, "broken") {
		t.Errorf("rejection should name the pinned node: %s", body)
	}
}
