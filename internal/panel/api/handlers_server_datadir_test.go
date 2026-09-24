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
// Agent refuses the pass rather than run SteamCMD under it, and no install
// container runs. Nothing touched the tree, so — the #328/#339 rule for any
// phase that never touched it — the server goes back to the state it was in,
// with the refusal naming the container in last_error and in the console; it
// is not install_failed, which would lock start behind a needless reinstall.
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

	state, lastErr := waitForLastError(t, h, token, sv.ID)
	if state != "running" {
		t.Errorf("state after a refused pass: got %q, want running (where it was; the tree was never touched)", state)
	}
	if !strings.Contains(lastErr, "refused to run the install pass") || !strings.Contains(lastErr, "kraken_"+sv.ID) {
		t.Errorf("last_error should carry the refusal naming the container; got %q", lastErr)
	}
	if n := len(rt.InstallScripts(sv.ID)); n != 0 {
		t.Errorf("an install container ran while the data dir was held: %d passes", n)
	}
	var consoled bool
	for _, l := range getInstallLog(t, h, token, sv.ID).Lines {
		if strings.Contains(l.Text, "refused to run the install pass") && strings.Contains(l.Text, "kraken_"+sv.ID) {
			consoled = true
		}
	}
	if !consoled {
		t.Error("the refusal should be in the install console, where the operator is looking")
	}
}

// TestReinstall_RefusedWhileAContainerHoldsTheDataDir — the same refusal on a
// reinstall puts the server back where it was; a fresh create has nowhere to
// go back to and still lands install_failed.
func TestReinstall_RefusedWhileAContainerHoldsTheDataDir(t *testing.T) {
	h, st := newTestServerStore(t)
	token := login(t, h)
	addr, rt := startFakeAgentRuntime(t, "node-x")
	nodeID := registerNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "reinstall-held", map[string]any{"script": "install.sh"})
	sv := seedOfflineServer(t, st, "sv-reheld", nodeID, specID, nil)
	rt.HoldDataDir(sv.ID, "stray", "running")

	rec := do(t, h, http.MethodPost, "/api/v1/servers/"+sv.ID+"/reinstall", token, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("reinstall: got %d, want 202; body: %s", rec.Code, rec.Body.String())
	}
	state, lastErr := waitForLastError(t, h, token, sv.ID)
	if state != "offline" || !strings.Contains(lastErr, "stray") {
		t.Errorf("refused reinstall: got state %q, last_error %q; want offline with the refusal", state, lastErr)
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
