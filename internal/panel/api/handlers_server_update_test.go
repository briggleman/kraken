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
	"github.com/briggleman/kraken/internal/shared/spec"
)

// createSpecWithInstall creates a minimal linux-native spec whose install block
// is the caller's, so a test can turn skip_update_on_start on or hang a BepInEx
// overlay off it.
func createSpecWithInstall(t *testing.T, h http.Handler, token, slug string, install map[string]any) string {
	t.Helper()
	rec := do(t, h, http.MethodPost, "/api/v1/specs", token, map[string]any{
		"name": "Update Target", "slug": slug,
		"steam_app_ids": map[string]int{"linux": 730},
		"platforms":     []map[string]string{{"kind": "linux-native", "image": "registry/kraken/steam-base:latest"}},
		"install":       install,
		"startup": map[string]any{
			"command": "./srv -port {{PORT_GAME}}",
			"stop":    map[string]string{"type": "signal", "value": "SIGINT"},
		},
		"ports":     []map[string]any{{"name": "game", "protocol": "udp", "default": 27015, "required": true}},
		"resources": map[string]int{"min_memory_mb": 1024},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create spec %s: status %d, body %s", slug, rec.Code, rec.Body.String())
	}
	var sp struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &sp)
	if sp.ID == "" {
		t.Fatalf("create spec %s: empty id", slug)
	}
	return sp.ID
}

// seedOfflineServer puts an installed, stopped server in the store — the state a
// start acts on — without going through the scheduler.
func seedOfflineServer(t *testing.T, st interface {
	CreateServer(context.Context, *store.Server) error
}, id, nodeID, specID string, mutate func(*store.Server)) *store.Server {
	t.Helper()
	sv := &store.Server{
		ID: id, Name: id, NodeID: nodeID, SpecID: specID,
		Kind: spec.LinuxNative, State: store.StateOffline,
		Vars:      map[string]string{"APP_ID": "730", "PORT_GAME": "27015"},
		Ports:     map[string]int{"game": 27015},
		MemoryMB:  1024,
		CreatedAt: time.Now(),
	}
	if mutate != nil {
		mutate(sv)
	}
	if err := st.CreateServer(context.Background(), sv); err != nil {
		t.Fatalf("seed server: %v", err)
	}
	return sv
}

// TestPower_StartRunsUpdatePassByDefault — the headline of #307: an ordinary
// start re-runs the install script before launching, so the server picks up
// depot updates instead of staying on its creation-day build. The request
// returns 202 with the server already installing (which gates a racing start
// and routes the console to the live install log), and the pass ends running.
func TestPower_StartRunsUpdatePassByDefault(t *testing.T) {
	h, st := newTestServerStore(t)
	token := login(t, h)
	addr, rt := startFakeAgentRuntime(t, "node-x")
	nodeID := registerNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "update-default", map[string]any{
		"script": "steamcmd +login anonymous +app_update {{APP_ID}} validate +quit",
	})
	sv := seedOfflineServer(t, st, "sv-update", nodeID, specID, nil)

	rec := do(t, h, http.MethodPost, "/api/v1/servers/"+sv.ID+"/power", token,
		map[string]string{"action": "start"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("start: got %d, want 202; body: %s", rec.Code, rec.Body.String())
	}
	waitForState(t, h, token, sv.ID, "running")

	scripts := rt.InstallScripts(sv.ID)
	if len(scripts) != 1 {
		t.Fatalf("install passes: got %d, want 1 (%q)", len(scripts), scripts)
	}
	if !strings.Contains(scripts[0], "app_update 730 validate") {
		t.Errorf("update pass did not render the install script with the server's vars: %q", scripts[0])
	}
}

// TestPower_RestartRunsUpdatePass — a restart takes the same path (stop, update,
// start) rather than the Agent's straight restart.
func TestPower_RestartRunsUpdatePass(t *testing.T) {
	h, st := newTestServerStore(t)
	token := login(t, h)
	addr, rt := startFakeAgentRuntime(t, "node-x")
	nodeID := registerNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "update-restart", map[string]any{"script": "install.sh"})
	sv := seedOfflineServer(t, st, "sv-restart", nodeID, specID, func(s *store.Server) {
		s.State = store.StateRunning
	})

	rec := do(t, h, http.MethodPost, "/api/v1/servers/"+sv.ID+"/power", token,
		map[string]string{"action": "restart"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("restart: got %d, want 202; body: %s", rec.Code, rec.Body.String())
	}
	waitForState(t, h, token, sv.ID, "running")
	if n := len(rt.InstallScripts(sv.ID)); n != 1 {
		t.Fatalf("install passes on restart: got %d, want 1", n)
	}
}

// TestPower_PinnedServerSkipsUpdatePass — the per-server opt-out. The start must
// be the old synchronous power call: no install container, no installing state.
func TestPower_PinnedServerSkipsUpdatePass(t *testing.T) {
	h, st := newTestServerStore(t)
	token := login(t, h)
	addr, rt := startFakeAgentRuntime(t, "node-x")
	nodeID := registerNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "update-pinned", map[string]any{"script": "install.sh"})
	sv := seedOfflineServer(t, st, "sv-pinned", nodeID, specID, func(s *store.Server) {
		s.PinBuild = true
	})

	rec := do(t, h, http.MethodPost, "/api/v1/servers/"+sv.ID+"/power", token,
		map[string]string{"action": "start"})
	if rec.Code != http.StatusOK {
		t.Fatalf("start on pinned server: got %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	if got := getServerState(t, h, token, sv.ID); got != "running" {
		t.Fatalf("pinned start state: got %q, want running", got)
	}
	if n := len(rt.InstallScripts(sv.ID)); n != 0 {
		t.Fatalf("pinned server ran %d install passes, want 0", n)
	}
}

// TestPower_SpecOptOutSkipsUpdatePass — the per-spec opt-out, for an install
// script that cannot be made idempotent.
func TestPower_SpecOptOutSkipsUpdatePass(t *testing.T) {
	h, st := newTestServerStore(t)
	token := login(t, h)
	addr, rt := startFakeAgentRuntime(t, "node-x")
	nodeID := registerNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "update-optout", map[string]any{
		"script": "install.sh", "skip_update_on_start": true,
	})
	sv := seedOfflineServer(t, st, "sv-optout", nodeID, specID, nil)

	rec := do(t, h, http.MethodPost, "/api/v1/servers/"+sv.ID+"/power", token,
		map[string]string{"action": "start"})
	if rec.Code != http.StatusOK {
		t.Fatalf("start on opted-out spec: got %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	if n := len(rt.InstallScripts(sv.ID)); n != 0 {
		t.Fatalf("opted-out spec ran %d install passes, want 0", n)
	}
}

// TestPower_UpdatePassExcludesBepInExScript — the update pass runs the vanilla
// install only. The overlay scripts copy over the tree (clobbering
// BepInEx/config) and pull unpinned "latest" builds, so re-running them on
// every start would silently rewrite a modded server's mod loader.
func TestPower_UpdatePassExcludesBepInExScript(t *testing.T) {
	h, st := newTestServerStore(t)
	token := login(t, h)
	addr, rt := startFakeAgentRuntime(t, "node-x")
	nodeID := registerNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "update-bepinex", map[string]any{
		"script":             "steamcmd +app_update {{APP_ID}} validate +quit",
		"bepinex_compatible": true,
		"bepinex_script":     "curl -L bepinex.zip | unzip-into /data",
	})
	sv := seedOfflineServer(t, st, "sv-modded", nodeID, specID, func(s *store.Server) {
		s.BepInEx = true
	})

	rec := do(t, h, http.MethodPost, "/api/v1/servers/"+sv.ID+"/power", token,
		map[string]string{"action": "start"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("start: got %d, want 202; body: %s", rec.Code, rec.Body.String())
	}
	waitForState(t, h, token, sv.ID, "running")

	scripts := rt.InstallScripts(sv.ID)
	if len(scripts) != 1 {
		t.Fatalf("install passes: got %d, want 1", len(scripts))
	}
	if strings.Contains(scripts[0], "bepinex.zip") {
		t.Errorf("update pass carried the BepInEx overlay: %q", scripts[0])
	}
	if !strings.Contains(scripts[0], "app_update") {
		t.Errorf("update pass did not carry the vanilla install script: %q", scripts[0])
	}
}

// TestPower_UpdatePassFailureLandsInstallFailed — a failed update must not go on
// to start the server over a half-written tree. It lands in install_failed with
// the reason on the record, which is also what makes the power handler refuse
// further starts until a reinstall.
func TestPower_UpdatePassFailureLandsInstallFailed(t *testing.T) {
	h, st := newTestServerStore(t)
	token := login(t, h)
	addr, _ := startFakeAgentRuntime(t, "node-x",
		agent.WithFakeInstallFailure("Error! App '730' state is 0x202 after update job"))
	nodeID := registerNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "update-broken", map[string]any{"script": "install.sh"})
	sv := seedOfflineServer(t, st, "sv-broken", nodeID, specID, nil)

	rec := do(t, h, http.MethodPost, "/api/v1/servers/"+sv.ID+"/power", token,
		map[string]string{"action": "start"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("start: got %d, want 202; body: %s", rec.Code, rec.Body.String())
	}
	waitForState(t, h, token, sv.ID, "install_failed")

	get := do(t, h, http.MethodGet, "/api/v1/servers/"+sv.ID, token, nil)
	var body struct {
		LastError string `json:"last_error"`
	}
	_ = json.Unmarshal(get.Body.Bytes(), &body)
	if !strings.Contains(body.LastError, "0x202") {
		t.Errorf("last_error should carry the installer's reason; got %q", body.LastError)
	}
	// And the gate holds: no start until a reinstall clears it.
	again := do(t, h, http.MethodPost, "/api/v1/servers/"+sv.ID+"/power", token,
		map[string]string{"action": "start"})
	if again.Code != http.StatusConflict {
		t.Errorf("start after failed update: got %d, want 409", again.Code)
	}
}

// TestSettings_PinBuildRoundTrip — the Config tab's toggle: the pin saves with
// the settings form and reads back, and an omitted field leaves it alone (every
// other settings save must not silently unpin a server).
func TestSettings_PinBuildRoundTrip(t *testing.T) {
	h, st := newTestServerStore(t)
	token := login(t, h)
	addr, _ := startFakeAgentRuntime(t, "node-x")
	nodeID := registerNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "update-settings", map[string]any{"script": "install.sh"})
	sv := seedOfflineServer(t, st, "sv-settings", nodeID, specID, nil)

	readPin := func() (pin, updates bool) {
		rec := do(t, h, http.MethodGet, "/api/v1/servers/"+sv.ID+"/settings", token, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("get settings: %d %s", rec.Code, rec.Body.String())
		}
		var body struct {
			PinBuild       bool `json:"pin_build"`
			UpdatesOnStart bool `json:"updates_on_start"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		return body.PinBuild, body.UpdatesOnStart
	}

	if pin, updates := readPin(); pin || !updates {
		t.Fatalf("fresh server: pin=%v updates_on_start=%v, want false/true", pin, updates)
	}
	if rec := do(t, h, http.MethodPut, "/api/v1/servers/"+sv.ID+"/settings", token,
		map[string]any{"values": map[string]string{}, "pin_build": true}); rec.Code != http.StatusOK {
		t.Fatalf("set pin: %d %s", rec.Code, rec.Body.String())
	}
	if pin, _ := readPin(); !pin {
		t.Fatal("pin_build did not persist")
	}
	// A settings save that says nothing about the pin leaves it set.
	if rec := do(t, h, http.MethodPut, "/api/v1/servers/"+sv.ID+"/settings", token,
		map[string]any{"values": map[string]string{}}); rec.Code != http.StatusOK {
		t.Fatalf("save without pin: %d %s", rec.Code, rec.Body.String())
	}
	if pin, _ := readPin(); !pin {
		t.Fatal("a settings save with no pin_build field cleared the pin")
	}
	if rec := do(t, h, http.MethodPut, "/api/v1/servers/"+sv.ID+"/settings", token,
		map[string]any{"values": map[string]string{}, "pin_build": false}); rec.Code != http.StatusOK {
		t.Fatalf("clear pin: %d %s", rec.Code, rec.Body.String())
	}
	if pin, _ := readPin(); pin {
		t.Fatal("pin_build did not clear")
	}
}
