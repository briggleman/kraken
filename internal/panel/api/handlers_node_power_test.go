package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/briggleman/kraken/internal/agent"
	"github.com/briggleman/kraken/internal/panel/rbac"
	"github.com/briggleman/kraken/internal/panel/store"
	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// powerLog records every power action a fake Agent is sent.
type powerLog struct {
	mu    sync.Mutex
	calls []string
}

func (l *powerLog) hook(serverID string, action agentpb.PowerAction) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, serverID+" "+action.String())
}

func (l *powerLog) seen() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.calls...)
}

func recordPower(rt *agent.FakeRuntime) *powerLog {
	l := &powerLog{}
	rt.SetPowerHook(l.hook)
	return l
}

// TestNodePower_RefusesServerOnAnotherNode is the regression for #369.
// POST /nodes/{id}/servers/{serverID}/power sends the action to the URL's node.
// It used to do that without checking the server lives there, so a caller
// allowed to power server A could have any node's Agent sent an action for A's
// id, and got that node's answer back as if it were A's. The pairing is now
// checked against the store. A server on another node answers exactly like a
// server that does not exist, or one the caller may not reach. None of those
// cases sends anything to either Agent.
func TestNodePower_RefusesServerOnAnotherNode(t *testing.T) {
	h, st := newTestServerStore(t)
	token := login(t, h)
	ctx := context.Background()

	addrA, rtA := startFakeAgentRuntime(t, "node-a")
	addrB, rtB := startFakeAgentRuntime(t, "node-b")
	nodeA := registerNode(t, h, token, addrA)
	rec := do(t, h, http.MethodPost, "/api/v1/nodes", token, map[string]any{
		"name": "abyss-node-02", "os": "linux", "address": addrB,
		"total_memory_mb": 16384, "port_start": 27200, "port_end": 27300,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("register node B: status %d, body %s", rec.Code, rec.Body.String())
	}
	var nb struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &nb); err != nil || nb.ID == "" {
		t.Fatalf("register node B decode: %v (body %s)", err, rec.Body.String())
	}
	nodeB := nb.ID

	specID := createSpec(t, h, token, "node-power-pairing")
	sv := seedOfflineServer(t, st, "sv-on-a", nodeA, specID, nil)
	logA, logB := recordPower(rtA), recordPower(rtB)

	powerPath := func(nodeID, serverID string) string {
		return "/api/v1/nodes/" + nodeID + "/servers/" + serverID + "/power"
	}

	// An Operator (server.power, but not server.any) who does not own sv. Its
	// denial is the third way of "not found" and must read the same.
	if err := st.CreateUser(ctx, &store.User{ID: "olga", Username: "olga", RoleID: rbac.RoleOperator, CreatedAt: time.Now()}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := st.CreateSession(ctx, &store.Session{Token: "olga-token", UserID: "olga", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// The ordinary not-found: a server id that exists nowhere.
	want := do(t, h, http.MethodPost, powerPath(nodeB, "00000000-0000-0000-0000-000000000000"), token,
		map[string]string{"action": "stop"})
	if want.Code != http.StatusNotFound {
		t.Fatalf("unknown server: got %d, want 404; body %s", want.Code, want.Body.String())
	}
	var body struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	if err := json.Unmarshal(want.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (body %s)", err, want.Body.String())
	}
	if body.Code != "not_found" || body.Error != "server not found on this node" {
		t.Errorf("unknown server answered %+v, want code not_found and \"server not found on this node\"", body)
	}

	cases := []struct {
		name, tok, node string
	}{
		{"admin, server on node A via node B", token, nodeB},
		{"non-owner, server on node A via node B", "olga-token", nodeB},
		{"non-owner, server on node A via node A", "olga-token", nodeA},
	}
	for _, c := range cases {
		for _, action := range []string{"start", "stop", "restart", "kill"} {
			rec := do(t, h, http.MethodPost, powerPath(c.node, sv.ID), c.tok, map[string]string{"action": action})
			if rec.Code != http.StatusNotFound || rec.Body.String() != want.Body.String() {
				t.Errorf("%s, %s: got %d %s, want the unknown-server answer %d %s",
					c.name, action, rec.Code, rec.Body.String(), want.Code, want.Body.String())
			}
		}
	}
	if got := logA.seen(); len(got) != 0 {
		t.Fatalf("node A's agent was sent power actions: %v", got)
	}
	if got := logB.seen(); len(got) != 0 {
		t.Fatalf("node B's agent was sent power actions: %v", got)
	}

	// The control: through its own node's path the same call is delivered, to
	// that node's agent only. This is what makes the empty logs above mean
	// something.
	rec = do(t, h, http.MethodPost, powerPath(nodeA, sv.ID), token, map[string]string{"action": "stop"})
	if rec.Code != http.StatusOK {
		t.Fatalf("stop via its own node: got %d, want 200; body %s", rec.Code, rec.Body.String())
	}
	if got := logA.seen(); len(got) != 1 || got[0] != sv.ID+" "+agentpb.PowerAction_POWER_ACTION_STOP.String() {
		t.Fatalf("node A's agent saw %v, want exactly one stop for %s", got, sv.ID)
	}
	if got := logB.seen(); len(got) != 0 {
		t.Fatalf("node B's agent was sent power actions: %v", got)
	}
}
