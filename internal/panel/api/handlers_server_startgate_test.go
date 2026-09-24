package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/briggleman/kraken/internal/agent"
	"github.com/briggleman/kraken/internal/panel"
	"github.com/briggleman/kraken/internal/panel/api"
	"github.com/briggleman/kraken/internal/panel/config"
	"github.com/briggleman/kraken/internal/panel/store"
	"github.com/briggleman/kraken/internal/panel/store/memory"
	"github.com/briggleman/kraken/internal/shared/agentpb"
	"github.com/briggleman/kraken/internal/shared/spec"
)

// The start gate on every path that boots a server. #357 put it on
// POST /servers/{id}/power; these pin the two other ways a start reached the
// Agent without it — a scheduled restart and the node-scoped power endpoint —
// and a spec lookup failure that let a start through unchecked.

// seedDueRestart stores an enabled restart schedule for serverID whose next run
// is already past, so one scheduler pass runs it.
func seedDueRestart(t *testing.T, st interface {
	CreateSchedule(context.Context, *store.ScheduledTask) error
}, id, serverID string) {
	t.Helper()
	due := time.Now().Add(-time.Minute)
	if err := st.CreateSchedule(context.Background(), &store.ScheduledTask{
		ID: id, ServerID: serverID, Name: "nightly restart", Action: store.ScheduleRestart,
		Cron: "0 4 * * *", Enabled: true, NextRunAt: &due, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("seed schedule: %v", err)
	}
}

// agentState is what the fake Agent believes about the server. It starts
// offline and only a power action moves it, so "still offline" means the Agent
// was never told to restart it.
func agentState(t *testing.T, rt *agent.FakeRuntime, id string) agentpb.ServerState {
	t.Helper()
	st, err := rt.Status(context.Background(), id)
	if err != nil {
		t.Fatalf("agent status: %v", err)
	}
	return st.State
}

// TestSchedule_RestartRefusedWhileRequiredSettingEmpty — a nightly restart is
// stop-then-start on the Agent, so it boots the game exactly as a start does.
// Before the fix it went straight to the Agent and restarted a Dragonwilds
// server into the crash the operator-facing gate exists to prevent.
func TestSchedule_RestartRefusedWhileRequiredSettingEmpty(t *testing.T) {
	srv, st := newTestAPI(t)
	h := srv.Handler()
	token := login(t, h)
	addr, rt := startFakeAgentRuntime(t, "node-x")
	nodeID := registerNode(t, h, token, addr)
	specID := createSpecWithRequiredSetting(t, h, token, "owned-sched")
	sv := seedOfflineServer(t, st, "sv-owned-sched", nodeID, specID, func(s *store.Server) {
		s.State = store.StateRunning
	})
	seedDueRestart(t, st, "sch-owned", sv.ID)

	srv.RunDueSchedulesForTest(context.Background())

	task, err := st.GetSchedule(context.Background(), "sch-owned")
	if err != nil {
		t.Fatalf("get schedule: %v", err)
	}
	// The same sentence the power endpoint answers with, where an operator looks
	// when a restart did not happen.
	want := "Owner Player ID is required before this server can start — set it on the Settings tab"
	if task.LastError != want {
		t.Fatalf("last_error: got %q, want %q", task.LastError, want)
	}
	if got := agentState(t, rt, sv.ID); got != agentpb.ServerState_SERVER_STATE_OFFLINE {
		t.Fatalf("the Agent was told to restart the server (agent state %v)", got)
	}
}

// TestSchedule_RestartSkipsAServerThatIsNotUp — the Agent's restart is a stop
// then a start, so on a server someone stopped it would quietly start it again.
// Offline is refused for that reason, and every state a restart is not for is
// refused with it; the schedule records why.
func TestSchedule_RestartSkipsAServerThatIsNotUp(t *testing.T) {
	for _, state := range []store.ServerState{
		store.StateOffline, store.StateStopping, store.StateInstalling, store.StateInstallFailed,
	} {
		t.Run(string(state), func(t *testing.T) {
			srv, st := newTestAPI(t)
			h := srv.Handler()
			token := login(t, h)
			addr, rt := startFakeAgentRuntime(t, "node-x")
			nodeID := registerNode(t, h, token, addr)
			specID := createSpec(t, h, token, "sched-"+string(state))
			sv := seedOfflineServer(t, st, "sv-sched", nodeID, specID, func(s *store.Server) {
				s.State = state
			})
			seedDueRestart(t, st, "sch-1", sv.ID)

			srv.RunDueSchedulesForTest(context.Background())

			task, err := st.GetSchedule(context.Background(), "sch-1")
			if err != nil {
				t.Fatalf("get schedule: %v", err)
			}
			if !strings.Contains(task.LastError, "skipped") || !strings.Contains(task.LastError, string(state)) {
				t.Fatalf("last_error should say the restart was skipped on a %s server: %q", state, task.LastError)
			}
			if got := agentState(t, rt, sv.ID); got != agentpb.ServerState_SERVER_STATE_OFFLINE {
				t.Fatalf("a scheduled restart started a %s server (agent state %v)", state, got)
			}
			stored, err := st.GetServer(context.Background(), sv.ID)
			if err != nil {
				t.Fatalf("get server: %v", err)
			}
			if stored.State != state {
				t.Fatalf("stored state moved from %s to %s", state, stored.State)
			}
		})
	}
}

// TestSchedule_RestartRevivesCrashedAndStuckServers — a nightly restart reviving
// a server the watchdog gave up on is behaviour operators rely on, and a server
// stuck in starting (a ready line that never matches) is exactly what it should
// cycle. Neither is refused.
func TestSchedule_RestartRevivesCrashedAndStuckServers(t *testing.T) {
	for _, state := range []store.ServerState{store.StateCrashed, store.StateStarting} {
		t.Run(string(state), func(t *testing.T) {
			srv, st := newTestAPI(t)
			h := srv.Handler()
			token := login(t, h)
			addr, rt := startFakeAgentRuntime(t, "node-x")
			nodeID := registerNode(t, h, token, addr)
			specID := createSpec(t, h, token, "sched-revive-"+string(state))
			sv := seedOfflineServer(t, st, "sv-revive", nodeID, specID, func(s *store.Server) {
				s.State = state
			})
			seedDueRestart(t, st, "sch-revive", sv.ID)

			srv.RunDueSchedulesForTest(context.Background())

			task, err := st.GetSchedule(context.Background(), "sch-revive")
			if err != nil {
				t.Fatalf("get schedule: %v", err)
			}
			if task.LastError != "" {
				t.Fatalf("scheduled restart of a %s server refused: %q", state, task.LastError)
			}
			if got := agentState(t, rt, sv.ID); got != agentpb.ServerState_SERVER_STATE_RUNNING {
				t.Fatalf("the Agent never restarted the %s server (agent state %v)", state, got)
			}
		})
	}
}

// TestSchedule_RestartRunsOnARunningServer — the gate refuses only what it
// should: a running server with nothing missing is restarted as before.
func TestSchedule_RestartRunsOnARunningServer(t *testing.T) {
	srv, st := newTestAPI(t)
	h := srv.Handler()
	token := login(t, h)
	addr, rt := startFakeAgentRuntime(t, "node-x")
	nodeID := registerNode(t, h, token, addr)
	specID := createSpec(t, h, token, "sched-running")
	sv := seedOfflineServer(t, st, "sv-running", nodeID, specID, func(s *store.Server) {
		s.State = store.StateRunning
	})
	seedDueRestart(t, st, "sch-run", sv.ID)

	srv.RunDueSchedulesForTest(context.Background())

	task, err := st.GetSchedule(context.Background(), "sch-run")
	if err != nil {
		t.Fatalf("get schedule: %v", err)
	}
	if task.LastError != "" {
		t.Fatalf("restart of a running server failed: %q", task.LastError)
	}
	if got := agentState(t, rt, sv.ID); got != agentpb.ServerState_SERVER_STATE_RUNNING {
		t.Fatalf("the Agent never restarted the server (agent state %v)", got)
	}
}

// TestNodePower_StartRefusedWhileRequiredSettingEmpty — the node-scoped power
// endpoint reaches the same Agent, so it answers with the same 409 body; stop
// is still never refused.
func TestNodePower_StartRefusedWhileRequiredSettingEmpty(t *testing.T) {
	h, st := newTestServerStore(t)
	token := login(t, h)
	addr, rt := startFakeAgentRuntime(t, "node-x")
	nodeID := registerNode(t, h, token, addr)
	specID := createSpecWithRequiredSetting(t, h, token, "owned-nodepower")
	sv := seedOfflineServer(t, st, "sv-owned-node", nodeID, specID, nil)
	path := "/api/v1/nodes/" + nodeID + "/servers/" + sv.ID + "/power"

	for _, action := range []string{"start", "restart"} {
		rec := do(t, h, http.MethodPost, path, token, map[string]string{"action": action})
		assertRequiredRefusal(t, rec.Code, rec.Body.Bytes())
	}
	if got := agentState(t, rt, sv.ID); got != agentpb.ServerState_SERVER_STATE_OFFLINE {
		t.Fatalf("a refused start reached the Agent (agent state %v)", got)
	}
	if rec := do(t, h, http.MethodPost, path, token, map[string]string{"action": "stop"}); rec.Code != http.StatusOK {
		t.Fatalf("stop with a required setting blank: got %d, want 200; body %s", rec.Code, rec.Body.String())
	}
}

// TestNodePower_StartRefusedWhileInstalling — the install-state refusals came
// along with the gate: this path used to start a server mid-install.
func TestNodePower_StartRefusedWhileInstalling(t *testing.T) {
	h, st := newTestServerStore(t)
	token := login(t, h)
	addr, _ := startFakeAgentRuntime(t, "node-x")
	nodeID := registerNode(t, h, token, addr)
	specID := createSpec(t, h, token, "nodepower-installing")
	sv := seedOfflineServer(t, st, "sv-installing", nodeID, specID, func(s *store.Server) {
		s.State = store.StateInstalling
	})
	rec := do(t, h, http.MethodPost, "/api/v1/nodes/"+nodeID+"/servers/"+sv.ID+"/power", token,
		map[string]string{"action": "start"})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "still installing") {
		t.Fatalf("start while installing: got %d %s, want 409 still installing", rec.Code, rec.Body.String())
	}
}

// TestPower_StartRefusedWhenSpecIsGone — the gate is judged on the spec, so a
// start whose spec no longer exists is refused rather than let through
// unchecked. It is a 409 naming the reason, not a 500: a spec's id is a UUID,
// so this server can never start again, and retrying will not help. Stop and
// kill need no spec and still work.
func TestPower_StartRefusedWhenSpecIsGone(t *testing.T) {
	h, st := newTestServerStore(t)
	token := login(t, h)
	addr, rt := startFakeAgentRuntime(t, "node-x")
	nodeID := registerNode(t, h, token, addr)
	sv := seedOfflineServer(t, st, "sv-nospec", nodeID, "no-such-spec", nil)

	for _, path := range []string{
		"/api/v1/servers/" + sv.ID + "/power",
		"/api/v1/nodes/" + nodeID + "/servers/" + sv.ID + "/power",
	} {
		for _, action := range []string{"start", "restart"} {
			rec := do(t, h, http.MethodPost, path, token, map[string]string{"action": action})
			if rec.Code != http.StatusConflict {
				t.Fatalf("%s %s without its spec: got %d, want 409; body %s", path, action, rec.Code, rec.Body.String())
			}
			var body struct {
				Error string `json:"error"`
				Code  string `json:"code"`
			}
			_ = json.Unmarshal(rec.Body.Bytes(), &body)
			if body.Code != "spec_missing" || !strings.Contains(body.Error, "no longer exists") {
				t.Fatalf("%s %s: want code spec_missing and a sentence saying the spec is gone, got %+v", path, action, body)
			}
			if want := "was not " + action + "ed"; !strings.Contains(body.Error, want) {
				t.Fatalf("%s %s: the sentence should say it %s: %q", path, action, want, body.Error)
			}
		}
		for _, action := range []string{"stop", "kill"} {
			if rec := do(t, h, http.MethodPost, path, token, map[string]string{"action": action}); rec.Code != http.StatusOK {
				t.Fatalf("%s %s without its spec: got %d, want 200; body %s", path, action, rec.Code, rec.Body.String())
			}
		}
	}
	if got := agentState(t, rt, sv.ID); got != agentpb.ServerState_SERVER_STATE_OFFLINE {
		t.Fatalf("an unchecked start reached the Agent (agent state %v)", got)
	}
}

// specLookupFails is the memory store with a spec read that always errors, the
// way a Postgres read does when the connection drops mid-request.
type specLookupFails struct{ *memory.Store }

func (specLookupFails) GetSpec(context.Context, string) (*spec.Spec, error) {
	return nil, errors.New("connection reset by peer")
}

// TestPower_StartRefusedWhenSpecLookupErrors — a store error is not a missing
// spec: it is a 500, and the start is still refused rather than let through
// unchecked.
func TestPower_StartRefusedWhenSpecLookupErrors(t *testing.T) {
	st := memory.New()
	cfg := &config.Config{
		Env: "test", SessionTTL: time.Hour,
		BootstrapAdminUser: testAdmin, BootstrapAdminPassword: testPass,
		SetupAllowedCIDRs: []string{"192.0.2.0/24"},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := panel.Seed(context.Background(), st, cfg, logger); err != nil {
		t.Fatalf("seed: %v", err)
	}
	clearMustChangePassword(t, st, testAdmin)
	h := api.New(cfg, specLookupFails{st}, logger).Handler()
	token := login(t, h)
	addr, rt := startFakeAgentRuntime(t, "node-x")
	nodeID := registerNode(t, h, token, addr)
	sv := seedOfflineServer(t, st, "sv-specerr", nodeID, "some-spec", nil)

	rec := do(t, h, http.MethodPost, "/api/v1/servers/"+sv.ID+"/power", token, map[string]string{"action": "start"})
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("start with the spec read failing: got %d, want 500; body %s", rec.Code, rec.Body.String())
	}
	if got := agentState(t, rt, sv.ID); got != agentpb.ServerState_SERVER_STATE_OFFLINE {
		t.Fatalf("an unchecked start reached the Agent (agent state %v)", got)
	}
}

// TestDeleteSpec_RefusedWhileInUse — the hole spec_missing reports, closed at
// its source: a spec cannot be deleted while any server is built from it.
func TestDeleteSpec_RefusedWhileInUse(t *testing.T) {
	h, st := newTestServerStore(t)
	token := login(t, h)
	addr, _ := startFakeAgentRuntime(t, "node-x")
	nodeID := registerNode(t, h, token, addr)
	specID := createSpec(t, h, token, "in-use")
	seedOfflineServer(t, st, "sv-a", nodeID, specID, nil)
	seedOfflineServer(t, st, "sv-b", nodeID, specID, nil)

	rec := do(t, h, http.MethodDelete, "/api/v1/specs/"+specID, token, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("delete a spec in use: got %d, want 409; body %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Error   string `json:"error"`
		Code    string `json:"code"`
		Servers int    `json:"servers"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Code != "spec_in_use" || body.Servers != 2 || !strings.Contains(body.Error, "2 servers") {
		t.Fatalf("want spec_in_use naming 2 servers, got %+v", body)
	}
	if _, err := st.GetSpec(context.Background(), specID); err != nil {
		t.Fatalf("a refused delete removed the spec: %v", err)
	}

	// Once nothing uses it, it deletes as before.
	for _, id := range []string{"sv-a", "sv-b"} {
		if err := st.DeleteServer(context.Background(), id); err != nil {
			t.Fatalf("delete server %s: %v", id, err)
		}
	}
	if rec := do(t, h, http.MethodDelete, "/api/v1/specs/"+specID, token, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete an unused spec: got %d, want 204; body %s", rec.Code, rec.Body.String())
	}
}
