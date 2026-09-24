package api_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/briggleman/kraken/internal/panel/store"
)

// TestPower_UpdateRefusedWhileAContainerHoldsTheDataDir — #351. The pre-update
// stop succeeds, but a container it did not account for (untracked, or one
// whose stop did not take) still has the data dir mounted and running. The
// Agent refuses the pass rather than run SteamCMD under it: no install
// container runs, and the server lands in install_failed with the refusal —
// naming the container — as last_error, like any other install failure the
// Agent reports. Start stays refused until a reinstall.
func TestPower_UpdateRefusedWhileAContainerHoldsTheDataDir(t *testing.T) {
	h, st := newTestServerStore(t)
	token := login(t, h)
	addr, rt := startFakeAgentRuntime(t, "node-x")
	nodeID := registerNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "update-held", map[string]any{"script": "install.sh"})
	sv := seedOfflineServer(t, st, "sv-held", nodeID, specID, func(s *store.Server) {
		s.State = store.StateRunning
	})
	rt.HoldDataDir(sv.ID, "kraken_"+sv.ID, "running")

	rec := do(t, h, http.MethodPost, "/api/v1/servers/"+sv.ID+"/power", token,
		map[string]string{"action": "restart"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("restart: got %d, want 202; body: %s", rec.Code, rec.Body.String())
	}
	waitForState(t, h, token, sv.ID, "install_failed")

	_, lastErr := serverRecord(t, h, token, sv.ID)
	if !strings.Contains(lastErr, "refused to run the install pass") || !strings.Contains(lastErr, "kraken_"+sv.ID) {
		t.Errorf("last_error should carry the refusal naming the container; got %q", lastErr)
	}
	if n := len(rt.InstallScripts(sv.ID)); n != 0 {
		t.Errorf("an install container ran while the data dir was held: %d passes", n)
	}
	again := do(t, h, http.MethodPost, "/api/v1/servers/"+sv.ID+"/power", token,
		map[string]string{"action": "start"})
	if again.Code != http.StatusConflict {
		t.Errorf("start after a refused update: got %d, want 409", again.Code)
	}
}

// TestPower_UpdateClearsAStoppedDataDirHolder — the other half: a container
// that holds the dir but has exited is removed by the Agent and the update
// goes ahead. It is recreated on start anyway.
func TestPower_UpdateClearsAStoppedDataDirHolder(t *testing.T) {
	h, st := newTestServerStore(t)
	token := login(t, h)
	addr, rt := startFakeAgentRuntime(t, "node-x")
	nodeID := registerNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "update-exited", map[string]any{"script": "install.sh"})
	sv := seedOfflineServer(t, st, "sv-exited", nodeID, specID, nil)
	rt.HoldDataDir(sv.ID, "kraken_"+sv.ID, "exited")

	rec := do(t, h, http.MethodPost, "/api/v1/servers/"+sv.ID+"/power", token,
		map[string]string{"action": "start"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("start: got %d, want 202; body: %s", rec.Code, rec.Body.String())
	}
	waitForState(t, h, token, sv.ID, "running")
	if n := len(rt.InstallScripts(sv.ID)); n != 1 {
		t.Errorf("install passes: got %d, want 1", n)
	}
}
