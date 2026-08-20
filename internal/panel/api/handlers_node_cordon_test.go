package api_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

type nodeStatusView struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Status          string `json:"status"`
	Cordoned        bool   `json:"cordoned"`
	IdentityPending bool   `json:"identity_pending"`
}

func getNode(t *testing.T, h http.Handler, token, id string) nodeStatusView {
	t.Helper()
	rec := do(t, h, http.MethodGet, "/api/v1/nodes/"+id, token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get node: status %d, body %s", rec.Code, rec.Body.String())
	}
	var n nodeStatusView
	if err := json.Unmarshal(rec.Body.Bytes(), &n); err != nil {
		t.Fatalf("decode node: %v", err)
	}
	return n
}

// Cordon holds a node back from placements and is durable: a healthy node
// flips online<->cordoned, and reconcile doesn't flip it back to online while
// the hold stands (#105).
func TestCordonNode(t *testing.T) {
	h, _ := newTestServerStore(t)
	token := login(t, h)
	addr := startFakeAgent(t, "node-cordon")
	id := registerNode(t, h, token, addr)

	// Bring it online first.
	if rec := do(t, h, http.MethodGet, "/api/v1/nodes/"+id+"/info", token, nil); rec.Code != http.StatusOK {
		t.Fatalf("node info: status %d, body %s", rec.Code, rec.Body.String())
	}
	if n := getNode(t, h, token, id); n.Status != "online" {
		t.Fatalf("node should be online before cordon, got %q", n.Status)
	}

	// Cordon.
	if rec := do(t, h, http.MethodPost, "/api/v1/nodes/"+id+"/cordon", token, nil); rec.Code != http.StatusOK {
		t.Fatalf("cordon: status %d, body %s", rec.Code, rec.Body.String())
	}
	n := getNode(t, h, token, id)
	if !n.Cordoned || n.Status != "cordoned" {
		t.Fatalf("after cordon: cordoned=%v status=%q", n.Cordoned, n.Status)
	}

	// A reconcile (node info) must NOT flip a cordoned node back to online.
	if rec := do(t, h, http.MethodGet, "/api/v1/nodes/"+id+"/info", token, nil); rec.Code != http.StatusOK {
		t.Fatalf("node info while cordoned: status %d", rec.Code)
	}
	if n := getNode(t, h, token, id); n.Status != "cordoned" {
		t.Fatalf("reconcile overwrote cordon: status %q", n.Status)
	}

	// Uncordon restores online.
	if rec := do(t, h, http.MethodPost, "/api/v1/nodes/"+id+"/uncordon", token, nil); rec.Code != http.StatusOK {
		t.Fatalf("uncordon: status %d, body %s", rec.Code, rec.Body.String())
	}
	n = getNode(t, h, token, id)
	if n.Cordoned || n.Status != "online" {
		t.Fatalf("after uncordon: cordoned=%v status=%q", n.Cordoned, n.Status)
	}
}

// Registration with a blank name must not fail on a slow/absent agent (#106):
// it returns immediately with a placeholder name + identity_pending, and the
// first reconcile adopts the agent's real node id.
func TestRegisterNode_AsyncIdentity(t *testing.T) {
	h, _ := newTestServerStore(t)
	token := login(t, h)
	addr := startFakeAgent(t, "adopted-id")

	rec := do(t, h, http.MethodPost, "/api/v1/nodes", token, map[string]any{
		"address": addr, "total_memory_mb": 8192,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("register: status %d, body %s", rec.Code, rec.Body.String())
	}
	var reg nodeStatusView
	if err := json.Unmarshal(rec.Body.Bytes(), &reg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !reg.IdentityPending {
		t.Fatal("blank-name registration should be identity_pending")
	}
	if reg.Name == "" || reg.Name == "adopted-id" {
		t.Fatalf("expected a placeholder name, got %q", reg.Name)
	}

	// First reconcile adopts the agent's real node id and clears the flag. The
	// register handler kicks a background reconcile; poll briefly, then force
	// one via the info endpoint if it hasn't landed yet.
	deadline := time.Now().Add(3 * time.Second)
	for {
		if n := getNode(t, h, token, reg.ID); n.Name == "adopted-id" && !n.IdentityPending {
			break
		}
		if time.Now().After(deadline) {
			// Force a synchronous reconcile and re-check once.
			_ = do(t, h, http.MethodGet, "/api/v1/nodes/"+reg.ID+"/info", token, nil)
			n := getNode(t, h, token, reg.ID)
			if n.Name != "adopted-id" || n.IdentityPending {
				t.Fatalf("identity not adopted: name=%q pending=%v", n.Name, n.IdentityPending)
			}
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A node registered with an explicit name is NOT identity-pending.
func TestRegisterNode_ExplicitNameNotPending(t *testing.T) {
	h, _ := newTestServerStore(t)
	token := login(t, h)
	addr := startFakeAgent(t, "explicit")
	id := registerNode(t, h, token, addr)
	if n := getNode(t, h, token, id); n.IdentityPending {
		t.Fatal("explicit-name registration should not be identity_pending")
	}
}
