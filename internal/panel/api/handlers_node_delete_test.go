package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/briggleman/kraken/internal/panel/store"
)

// servers.node_id carries no foreign key, so nothing but this handler stops a
// delete from leaving rows pointing at a node that no longer exists. Those
// orphans are unrecoverable — a re-enrolled node is minted a fresh id — and they
// stop being reconciled, so they sit claiming their last state forever.
func TestDeleteNodeRefusesWhileServersArePlaced(t *testing.T) {
	h, st := newTestServerStore(t)
	token := login(t, h)
	addr := startFakeAgent(t, "node-x")
	nodeID := registerNode(t, h, token, addr)

	if err := st.CreateServer(context.Background(), &store.Server{
		ID: "s1", Name: "s1", NodeID: nodeID, State: store.StateRunning, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("seed server: %v", err)
	}

	rec := do(t, h, http.MethodDelete, "/api/v1/nodes/"+nodeID, token, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("delete with a server placed: status %d, want 409; body %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	// The count is the actionable part — "1 server" tells the operator how much
	// work clearing the node is.
	if !strings.Contains(body.Error, "1 server ") {
		t.Errorf("refusal should name the count, got %q", body.Error)
	}

	// And the node must still be there: a refused delete that half-happened
	// would be worse than the orphaning it prevents.
	if rec := do(t, h, http.MethodGet, "/api/v1/nodes/"+nodeID, token, nil); rec.Code != http.StatusOK {
		t.Errorf("node vanished despite the refusal: status %d", rec.Code)
	}
}

func TestDeleteNodeSucceedsOnceItIsEmpty(t *testing.T) {
	h, st := newTestServerStore(t)
	token := login(t, h)
	addr := startFakeAgent(t, "node-y")
	nodeID := registerNode(t, h, token, addr)

	if err := st.CreateServer(context.Background(), &store.Server{
		ID: "s1", Name: "s1", NodeID: nodeID, State: store.StateOffline, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("seed server: %v", err)
	}
	// An offline server still occupies the node: the guard is about the record, not
	// the runtime state.
	if rec := do(t, h, http.MethodDelete, "/api/v1/nodes/"+nodeID, token, nil); rec.Code != http.StatusConflict {
		t.Fatalf("offline server should still block: status %d", rec.Code)
	}

	if err := st.DeleteServer(context.Background(), "s1"); err != nil {
		t.Fatalf("delete server: %v", err)
	}
	if rec := do(t, h, http.MethodDelete, "/api/v1/nodes/"+nodeID, token, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete on an empty node: status %d, want 204", rec.Code)
	}
}

// A server on a DIFFERENT node must not block this one.
func TestDeleteNodeIgnoresServersOnOtherNodes(t *testing.T) {
	h, st := newTestServerStore(t)
	token := login(t, h)
	keepID := registerNode(t, h, token, startFakeAgent(t, "node-keep"))
	dropID := registerNode(t, h, token, startFakeAgent(t, "node-drop"))

	if err := st.CreateServer(context.Background(), &store.Server{
		ID: "s1", Name: "s1", NodeID: keepID, State: store.StateRunning, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("seed server: %v", err)
	}

	if rec := do(t, h, http.MethodDelete, "/api/v1/nodes/"+dropID, token, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("empty node blocked by another node's server: status %d, body %s", rec.Code, rec.Body.String())
	}
}
