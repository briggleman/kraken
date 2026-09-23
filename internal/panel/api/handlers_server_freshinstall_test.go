package api_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/briggleman/kraken/internal/panel/store"
)

// waitForStateWithin polls until the server reaches want, for a create or
// reinstall whose install pass runs in the background.
func waitForStateWithin(t *testing.T, h http.Handler, token, id, want string, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for getServerState(t, h, token, id) != want {
		if time.Now().After(deadline) {
			t.Fatalf("server %s never reached %q (state %q)", id, want, getServerState(t, h, token, id))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestPower_FirstStartAfterCreateSkipsUpdatePass — the regression. A fresh
// deploy with "start once the install finishes" ticked (the form's default)
// installs, lands offline, and is started straight away. Before the fix that
// start ran update-on-start over a tree seconds old: SteamCMD twice, back to
// back, for nothing. Create + start must be exactly one install pass.
func TestPower_FirstStartAfterCreateSkipsUpdatePass(t *testing.T) {
	h, _ := newTestServerStore(t)
	token := login(t, h)
	addr, rt := startFakeAgentRuntime(t, "node-fresh")
	nodeID := registerNode(t, h, token, addr)
	// The info probe marks the node online and so schedulable.
	if rec := do(t, h, http.MethodGet, "/api/v1/nodes/"+nodeID+"/info", token, nil); rec.Code != http.StatusOK {
		t.Fatalf("node info: status %d, body %s", rec.Code, rec.Body.String())
	}
	specID := createSpecWithInstall(t, h, token, "fresh-create", map[string]any{
		"script": "steamcmd +login anonymous +app_update {{APP_ID}} validate +quit",
	})

	rec := do(t, h, http.MethodPost, "/api/v1/servers", token, map[string]any{
		"spec_id": specID, "name": "fresh-01",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create server: status %d, body %s", rec.Code, rec.Body.String())
	}
	var created struct{ ID string }
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created: %v", err)
	}
	waitForStateWithin(t, h, token, created.ID, "offline", 20*time.Second)

	rec = do(t, h, http.MethodPost, "/api/v1/servers/"+created.ID+"/power", token,
		map[string]string{"action": "start"})
	// 200, not 202: no update pass, so the start is the plain synchronous call.
	if rec.Code != http.StatusOK {
		t.Fatalf("first start: got %d, want 200 (no update pass); body: %s", rec.Code, rec.Body.String())
	}
	if got := getServerState(t, h, token, created.ID); got != "running" {
		t.Fatalf("first start state: got %q, want running", got)
	}
	if n := len(rt.InstallScripts(created.ID)); n != 1 {
		t.Fatalf("create + first start ran %d install passes, want 1 — the create's own", n)
	}
}

// TestPower_FirstStartAfterReinstallSkipsUpdatePass — a reinstall is the other
// way a tree gets freshly installed, and it ends the same way a create does. The
// seeded server has no stamp, so the stamp this start sees must be the
// reinstall's own.
func TestPower_FirstStartAfterReinstallSkipsUpdatePass(t *testing.T) {
	h, st := newTestServerStore(t)
	token := login(t, h)
	addr, rt := startFakeAgentRuntime(t, "node-x")
	nodeID := registerNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "fresh-reinstall", map[string]any{"script": "install.sh"})
	sv := seedOfflineServer(t, st, "sv-reinstall", nodeID, specID, func(s *store.Server) {
		s.State = store.StateInstallFailed
	})

	rec := do(t, h, http.MethodPost, "/api/v1/servers/"+sv.ID+"/reinstall", token, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("reinstall: got %d, want 202; body: %s", rec.Code, rec.Body.String())
	}
	waitForStateWithin(t, h, token, sv.ID, "offline", 20*time.Second)

	rec = do(t, h, http.MethodPost, "/api/v1/servers/"+sv.ID+"/power", token,
		map[string]string{"action": "start"})
	if rec.Code != http.StatusOK {
		t.Fatalf("start after reinstall: got %d, want 200 (no update pass); body: %s", rec.Code, rec.Body.String())
	}
	if n := len(rt.InstallScripts(sv.ID)); n != 1 {
		t.Fatalf("reinstall + start ran %d install passes, want 1 — the reinstall's own", n)
	}
}

// TestPower_StartLongAfterProvisionStillUpdates — why this is a window and not a
// "never started" flag. A server created and then left alone must still update
// on its first start; skipping it would boot a stale build and quietly undo
// update-on-start for exactly the servers that need it most.
func TestPower_StartLongAfterProvisionStillUpdates(t *testing.T) {
	h, st := newTestServerStore(t)
	token := login(t, h)
	addr, rt := startFakeAgentRuntime(t, "node-x")
	nodeID := registerNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "stale-provision", map[string]any{"script": "install.sh"})
	longAgo := time.Now().Add(-2 * time.Hour).UTC()
	sv := seedOfflineServer(t, st, "sv-stale", nodeID, specID, func(s *store.Server) {
		s.ProvisionedAt = &longAgo
	})

	rec := do(t, h, http.MethodPost, "/api/v1/servers/"+sv.ID+"/power", token,
		map[string]string{"action": "start"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("start long after provision: got %d, want 202 (update pass); body: %s", rec.Code, rec.Body.String())
	}
	waitForState(t, h, token, sv.ID, "running")
	if n := len(rt.InstallScripts(sv.ID)); n != 1 {
		t.Fatalf("stale server ran %d install passes, want 1 — the update", n)
	}
}
