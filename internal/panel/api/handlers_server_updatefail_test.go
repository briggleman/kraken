package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/briggleman/kraken/internal/agent"
	"github.com/briggleman/kraken/internal/panel/store"
	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// serverRecord is the slice of a server the update-pass failure paths are about.
func serverRecord(t *testing.T, h http.Handler, token, id string) (state, lastError string) {
	t.Helper()
	rec := do(t, h, http.MethodGet, "/api/v1/servers/"+id, token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get server: status %d, body %s", rec.Code, rec.Body.String())
	}
	var sv struct {
		State     string `json:"state"`
		LastError string `json:"last_error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &sv)
	return sv.State, sv.LastError
}

// waitForLastError polls until the server carries a reason, and returns it with
// the state it settled in. The update pass is asynchronous and — this being the
// point of #328 — a pass that aborts can settle in the state it started from,
// so there is no state transition to wait on.
func waitForLastError(t *testing.T, h http.Handler, token, id string) (state, lastError string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		state, lastError = serverRecord(t, h, token, id)
		if lastError != "" {
			return state, lastError
		}
		if time.Now().After(deadline) {
			t.Fatalf("server %s never recorded a failure reason; state=%s", id, state)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// TestPower_UpdateStopFailureKeepsState — the headline of #328. A restart runs
// the update pass, whose pre-update stop fails because the Panel has lost its
// channel to the node. Nothing on the node was touched (the game is still
// running), so the server must stay exactly where it was with the reason
// attached — not land in install_failed, which claims the install tree is
// suspect and locks START/RESTART behind a reinstall.
func TestPower_UpdateStopFailureKeepsState(t *testing.T) {
	h, st := newTestServerStore(t)
	token := login(t, h)
	addr, rt := startFakeAgentRuntime(t, "node-x", agent.WithFakePowerFailure(
		agentpb.PowerAction_POWER_ACTION_STOP, "tunnel: no live session for node abyss-win"))
	nodeID := registerNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "update-stopfail", map[string]any{"script": "install.sh"})
	sv := seedOfflineServer(t, st, "sv-stopfail", nodeID, specID, func(s *store.Server) {
		s.State = store.StateRunning
	})

	rec := do(t, h, http.MethodPost, "/api/v1/servers/"+sv.ID+"/power", token,
		map[string]string{"action": "restart"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("restart: got %d, want 202; body: %s", rec.Code, rec.Body.String())
	}

	state, lastErr := waitForLastError(t, h, token, sv.ID)
	if state != "running" {
		t.Errorf("state after a failed pre-update stop: got %q, want running (the container never stopped)", state)
	}
	if !strings.Contains(lastErr, "stop before update") || !strings.Contains(lastErr, "no live session") {
		t.Errorf("last_error should carry the failed stop verbatim; got %q", lastErr)
	}
	// The install tree was never touched: no pass ran against it.
	if n := len(rt.InstallScripts(sv.ID)); n != 0 {
		t.Errorf("install passes after a failed pre-update stop: got %d, want 0", n)
	}
	// And the power gate is open — the retry is the button the operator already
	// pressed, not a reinstall.
	again := do(t, h, http.MethodPost, "/api/v1/servers/"+sv.ID+"/power", token,
		map[string]string{"action": "start"})
	if again.Code == http.StatusConflict {
		t.Errorf("start after a failed pre-update stop was gated: %s", again.Body.String())
	}
}

// TestPower_UpdateStartFailureLandsOffline — the other side of the split: the
// install pass succeeded, so the tree is good and only the start failed. That
// stays `offline` (retriable with a plain start), not install_failed.
func TestPower_UpdateStartFailureLandsOffline(t *testing.T) {
	h, st := newTestServerStore(t)
	token := login(t, h)
	addr, rt := startFakeAgentRuntime(t, "node-x", agent.WithFakePowerFailure(
		agentpb.PowerAction_POWER_ACTION_START, "agent: create container: no such image"))
	nodeID := registerNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "update-startfail", map[string]any{"script": "install.sh"})
	sv := seedOfflineServer(t, st, "sv-startfail", nodeID, specID, nil)

	rec := do(t, h, http.MethodPost, "/api/v1/servers/"+sv.ID+"/power", token,
		map[string]string{"action": "start"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("start: got %d, want 202; body: %s", rec.Code, rec.Body.String())
	}
	waitForState(t, h, token, sv.ID, "offline")
	if n := len(rt.InstallScripts(sv.ID)); n != 1 {
		t.Errorf("install passes: got %d, want 1 (the update itself must have run)", n)
	}
	// The start is retriable: no gate, and the record claims no install failure.
	if _, lastErr := serverRecord(t, h, token, sv.ID); lastErr != "" {
		t.Errorf("last_error after a good install with a failed start: got %q, want empty", lastErr)
	}
}

// TestPower_OfflineNodeRefusesLifecycleAction — the pass is not attempted at all
// when the Panel cannot reach the node (#328): the operator gets the reason on
// the spot instead of a 202 followed by a state flip nothing on the node backs.
func TestPower_OfflineNodeRefusesLifecycleAction(t *testing.T) {
	h, st := newTestServerStore(t)
	token := login(t, h)
	// A node whose agent address has nothing listening: unreachable, and the
	// record starts offline until first contact.
	nodeID := registerNode(t, h, token, "127.0.0.1:1")
	specID := createSpecWithInstall(t, h, token, "update-deadnode", map[string]any{"script": "install.sh"})
	sv := seedOfflineServer(t, st, "sv-deadnode", nodeID, specID, func(s *store.Server) {
		s.State = store.StateRunning
	})

	for _, action := range []string{"restart", "start", "stop"} {
		rec := do(t, h, http.MethodPost, "/api/v1/servers/"+sv.ID+"/power", token,
			map[string]string{"action": action})
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s on an offline node: got %d, want 503; body: %s", action, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "offline") {
			t.Errorf("%s: the refusal should name the node as offline; got %s", action, rec.Body.String())
		}
	}
	// Nothing was written: the server is where it was, with no invented reason.
	if state, lastErr := serverRecord(t, h, token, sv.ID); state != "running" || lastErr != "" {
		t.Errorf("after a refused action: state=%q last_error=%q, want running/empty", state, lastErr)
	}
}

// TestReconcile_AdoptsAgentRunningContainer — the reconnect half of #328. The
// Agent adopts running containers when it boots; the Panel should believe it.
// A stopped row the Agent contradicts becomes `running` — including an
// install_failed one, where a running container is proof the tree is fine — and
// that is what clears both the stale error and the node's `untracked` count.
func TestReconcile_AdoptsAgentRunningContainer(t *testing.T) {
	ctx := context.Background()
	srv, st := newTestAPI(t)
	h := srv.Handler()
	token := login(t, h)
	addr, rt := startFakeAgentRuntime(t, "node-x")
	nodeID := registerNode(t, h, token, addr)
	// Contact the node so the Panel believes it is live — a stopped row on a
	// node believed down is deliberately not worth a round trip.
	if rec := do(t, h, http.MethodGet, "/api/v1/nodes/"+nodeID+"/info", token, nil); rec.Code != http.StatusOK {
		t.Fatalf("node info: status %d, body %s", rec.Code, rec.Body.String())
	}
	specID := createSpecWithInstall(t, h, token, "reconcile-adopt", map[string]any{"script": "install.sh"})

	offline := seedOfflineServer(t, st, "sv-adopt-offline", nodeID, specID, nil)
	failed := seedOfflineServer(t, st, "sv-adopt-failed", nodeID, specID, func(s *store.Server) {
		s.State = store.StateInstallFailed
		s.LastError = "stop before update: rpc error: code = Unavailable"
	})
	installing := seedOfflineServer(t, st, "sv-adopt-installing", nodeID, specID, func(s *store.Server) {
		s.State = store.StateInstalling
	})
	stopped := seedOfflineServer(t, st, "sv-adopt-stopped", nodeID, specID, nil)

	// The agent has a managed container running for all but the last.
	for _, id := range []string{offline.ID, failed.ID, installing.ID} {
		if _, err := rt.Power(ctx, id, agentpb.PowerAction_POWER_ACTION_START); err != nil {
			t.Fatalf("fake start %s: %v", id, err)
		}
	}

	srv.ReconcileOnceForTest(ctx)

	for _, id := range []string{offline.ID, failed.ID} {
		got, err := st.GetServer(ctx, id)
		if err != nil {
			t.Fatalf("get %s: %v", id, err)
		}
		if got.State != store.StateRunning {
			t.Errorf("%s: state %q, want running (the agent has it running)", id, got.State)
		}
		if got.LastError != "" {
			t.Errorf("%s: last_error %q survived adoption of a running container", id, got.LastError)
		}
	}
	// An install pass owns its row: a container still running underneath one is
	// not evidence the pass has finished.
	if got, _ := st.GetServer(ctx, installing.ID); got.State != store.StateInstalling {
		t.Errorf("installing row: state %q, want installing (reconcile must not touch it)", got.State)
	}
	// And a genuinely stopped server stays stopped.
	if got, _ := st.GetServer(ctx, stopped.ID); got.State != store.StateOffline {
		t.Errorf("stopped row: state %q, want offline", got.State)
	}
}
