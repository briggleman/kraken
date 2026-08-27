package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/briggleman/kraken/internal/panel/store"
)

// TestStream_InstallStateNeedsNoAgent guards the reason the install log lives on
// the Panel: a server that failed to install is very often on a node the Panel
// cannot reach, and the old handler resolved the node and dialed the agent
// before accepting the socket. That turned the one place the failure was
// readable into a 5xx/502 — exactly when the operator needed it most.
func TestStream_InstallStateNeedsNoAgent(t *testing.T) {
	h, st := newTestServerStore(t)
	token := login(t, h)

	srv := httptest.NewServer(h)
	defer srv.Close()

	for _, state := range []store.ServerState{store.StateInstalling, store.StateInstallFailed} {
		sv := &store.Server{
			ID: "sv-" + string(state), Name: "sv-" + string(state),
			// A node ID that resolves to nothing: no agent to dial, by design.
			NodeID: "node-that-is-gone", State: state, CreatedAt: time.Now(),
		}
		if err := st.CreateServer(context.Background(), sv); err != nil {
			t.Fatalf("seed %s: %v", state, err)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v1/servers/" + sv.ID + "/stream/ws"
		c, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{
			Subprotocols: []string{"kraken.token", token},
		})
		if err != nil {
			code := 0
			if resp != nil {
				code = resp.StatusCode
			}
			cancel()
			t.Fatalf("%s: dial failed (HTTP %d): %v — the install surface must not depend on the agent", state, code, err)
		}
		// No install output was buffered for a seeded server, so the stream has
		// nothing to replay and must end rather than hang.
		_, _, rerr := c.Read(ctx)
		if rerr == nil {
			t.Errorf("%s: want the stream to close with no buffered output", state)
		}
		c.CloseNow()
		cancel()
	}
}

// TestStream_InstallOutputReachesTheBrowser is the fix for #130 end to end. The
// installer's lines are produced by the Agent and consumed by the Panel's own
// provisioning goroutine — no container survives the phase — so unless the Panel
// keeps them, "install failed" is a single summary line for what may have been a
// twenty-minute download. Here the node's address is broken after registration
// so provisioning fails, and the console stream must still carry the account of
// what happened.
func TestStream_InstallOutputReachesTheBrowser(t *testing.T) {
	h, st := newTestServerStore(t)
	token := login(t, h)
	addr := startFakeAgent(t, "node-x")
	nodeID := registerNode(t, h, token, addr)
	if rec := do(t, h, http.MethodGet, "/api/v1/nodes/"+nodeID+"/info", token, nil); rec.Code != http.StatusOK {
		t.Fatalf("node info: status %d, body %s", rec.Code, rec.Body.String())
	}
	specID := createSpec(t, h, token, "install-log-probe")

	// Break the route to the agent, leaving the node schedulable: the placement
	// succeeds, the provisioning call does not. (The fake agent's install always
	// succeeds, and a successful install drops its buffer by design — the failure
	// is the case whose output has to survive.)
	node, err := st.GetNode(context.Background(), nodeID)
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	node.Address = "127.0.0.1:1" // nothing listens here
	if err := st.UpdateNode(context.Background(), node); err != nil {
		t.Fatalf("update node: %v", err)
	}

	rec := do(t, h, http.MethodPost, "/api/v1/servers", token, map[string]any{
		"spec_id": specID, "name": "doomed-01",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create server: status %d, body %s", rec.Code, rec.Body.String())
	}
	var created struct{ ID string }
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created: %v", err)
	}

	deadline := time.Now().Add(20 * time.Second)
	for getServerState(t, h, token, created.ID) != "install_failed" {
		if time.Now().After(deadline) {
			t.Fatalf("server never reached install_failed (state %q)", getServerState(t, h, token, created.ID))
		}
		time.Sleep(50 * time.Millisecond)
	}

	srv := httptest.NewServer(h)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v1/servers/" + created.ID + "/stream/ws"
	c, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		Subprotocols: []string{"kraken.token", token},
	})
	if err != nil {
		code := 0
		if resp != nil {
			code = resp.StatusCode
		}
		t.Fatalf("dial (HTTP %d): %v", code, err)
	}
	defer c.CloseNow()

	type consoleFrame struct {
		Type   string `json:"type"`
		Stream string `json:"stream"`
		Text   string `json:"text"`
	}
	var lines []consoleFrame
	for {
		_, data, rerr := c.Read(ctx)
		if rerr != nil {
			break // the stream ends once the buffer is replayed
		}
		var f consoleFrame
		if err := json.Unmarshal(data, &f); err != nil {
			t.Fatalf("decode frame %q: %v", data, err)
		}
		lines = append(lines, f)
	}
	if len(lines) == 0 {
		t.Fatal("no install output reached the stream — the lines were dropped again")
	}
	var sawStart, sawFailure bool
	for _, l := range lines {
		if l.Type != "console" {
			t.Errorf("frame type %q, want console so the existing surface renders it", l.Type)
		}
		if strings.Contains(l.Text, "provisioning doomed-01") {
			sawStart = true
		}
		// The reason must be marked as a failure, not left to read as ordinary
		// output, and it must name the actual fault.
		if l.Stream == "error" && strings.Contains(l.Text, "agent create") {
			sawFailure = true
		}
	}
	if !sawStart {
		t.Errorf("install log never recorded the start of provisioning; got %+v", lines)
	}
	if !sawFailure {
		t.Errorf("install log never recorded the failure on the error stream; got %+v", lines)
	}
}
