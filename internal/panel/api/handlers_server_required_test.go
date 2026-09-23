package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/briggleman/kraken/internal/panel/store"
)

// createSpecWithRequiredSetting creates a spec whose OwnerId must be set before
// a server built from it may start — the shape of the Dragonwilds spec.
func createSpecWithRequiredSetting(t *testing.T, h http.Handler, token, slug string) string {
	t.Helper()
	rec := do(t, h, http.MethodPost, "/api/v1/specs", token, map[string]any{
		"name": "Owned Game", "slug": slug,
		"steam_app_ids": map[string]int{"linux": 730},
		"platforms":     []map[string]string{{"kind": "linux-native", "image": "registry/kraken/steam-base:latest"}},
		"install":       map[string]any{"script": "install.sh"},
		"startup": map[string]any{
			"command": "./srv -port {{PORT_GAME}}",
			"stop":    map[string]string{"type": "signal", "value": "SIGINT"},
		},
		"ports":     []map[string]any{{"name": "game", "protocol": "udp", "default": 27015, "required": true}},
		"resources": map[string]int{"min_memory_mb": 1024},
		"settings": map[string]any{"groups": []map[string]any{{
			"id": "server",
			"fields": []map[string]any{
				{"key": "OwnerId", "label": "Owner Player ID", "type": "string", "required": true},
				{"key": "ServerName", "label": "Server name", "type": "string", "default": "Kraken"},
			},
		}}},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create spec %s: status %d, body %s", slug, rec.Code, rec.Body.String())
	}
	var sp struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &sp)
	return sp.ID
}

// assertRequiredRefusal checks a power response is the required-settings 409:
// the machine-readable code, the missing key, and a message that names the
// field by its label and says where to set it.
func assertRequiredRefusal(t *testing.T, code int, body []byte) {
	t.Helper()
	if code != http.StatusConflict {
		t.Fatalf("got %d, want 409; body: %s", code, body)
	}
	var r struct {
		Error           string   `json:"error"`
		Code            string   `json:"code"`
		MissingSettings []string `json:"missing_settings"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		t.Fatalf("decode refusal: %v; body %s", err, body)
	}
	if r.Code != "required_settings_missing" {
		t.Fatalf("code: got %q, want required_settings_missing", r.Code)
	}
	if len(r.MissingSettings) != 1 || r.MissingSettings[0] != "OwnerId" {
		t.Fatalf("missing_settings: got %v, want [OwnerId]", r.MissingSettings)
	}
	if !strings.Contains(r.Error, "Owner Player ID") || !strings.Contains(r.Error, "Settings tab") {
		t.Fatalf("message should name the field and where to set it: %q", r.Error)
	}
}

// TestPower_FreshDeployWithRequiredSettingIsRefused — the incident, end to end.
// The deploy form's "start once the install finishes" (on by default) starts a
// server the moment its install lands, before anyone has opened the Settings
// tab. For a spec with a required setting that start must be refused, not
// launched into the crash the spec predicts.
func TestPower_FreshDeployWithRequiredSettingIsRefused(t *testing.T) {
	h, _ := newTestServerStore(t)
	token := login(t, h)
	addr, rt := startFakeAgentRuntime(t, "node-owned")
	nodeID := registerNode(t, h, token, addr)
	if rec := do(t, h, http.MethodGet, "/api/v1/nodes/"+nodeID+"/info", token, nil); rec.Code != http.StatusOK {
		t.Fatalf("node info: status %d, body %s", rec.Code, rec.Body.String())
	}
	specID := createSpecWithRequiredSetting(t, h, token, "owned-fresh")

	rec := do(t, h, http.MethodPost, "/api/v1/servers", token, map[string]any{"spec_id": specID, "name": "owned-01"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create server: status %d, body %s", rec.Code, rec.Body.String())
	}
	var created struct{ ID string }
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	deadline := time.Now().Add(20 * time.Second)
	for getServerState(t, h, token, created.ID) != "offline" {
		if time.Now().After(deadline) {
			t.Fatalf("install never finished (state %q)", getServerState(t, h, token, created.ID))
		}
		time.Sleep(50 * time.Millisecond)
	}
	installsBefore := len(rt.InstallScripts(created.ID))

	rec = do(t, h, http.MethodPost, "/api/v1/servers/"+created.ID+"/power", token, map[string]string{"action": "start"})
	assertRequiredRefusal(t, rec.Code, rec.Body.Bytes())
	// A refusal changes nothing: still offline, and no update pass started.
	if got := getServerState(t, h, token, created.ID); got != "offline" {
		t.Fatalf("refused start moved the state to %q", got)
	}
	if n := len(rt.InstallScripts(created.ID)); n != installsBefore {
		t.Fatalf("refused start ran an install pass (%d → %d)", installsBefore, n)
	}
}

// TestPower_StartAllowedOnceRequiredSettingSaved — the gate lifts the moment
// the operator does what it asked, and saving settings is never blocked by it.
func TestPower_StartAllowedOnceRequiredSettingSaved(t *testing.T) {
	h, st := newTestServerStore(t)
	token := login(t, h)
	addr, _ := startFakeAgentRuntime(t, "node-x")
	nodeID := registerNode(t, h, token, addr)
	specID := createSpecWithRequiredSetting(t, h, token, "owned-save")
	sv := seedOfflineServer(t, st, "sv-owned", nodeID, specID, nil)

	// Saving an unrelated field first must succeed: an operator fills the form in
	// whatever order they like, and the gate is on starting, not on saving.
	rec := do(t, h, http.MethodPut, "/api/v1/servers/"+sv.ID+"/settings", token,
		map[string]any{"values": map[string]string{"ServerName": "Midgard"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("saving an optional setting with the required one blank: got %d; body %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodPost, "/api/v1/servers/"+sv.ID+"/power", token, map[string]string{"action": "start"})
	assertRequiredRefusal(t, rec.Code, rec.Body.Bytes())

	rec = do(t, h, http.MethodPut, "/api/v1/servers/"+sv.ID+"/settings", token,
		map[string]any{"values": map[string]string{"OwnerId": "00023a5e"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("save OwnerId: got %d; body %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodPost, "/api/v1/servers/"+sv.ID+"/power", token, map[string]string{"action": "start"})
	if rec.Code != http.StatusAccepted && rec.Code != http.StatusOK {
		t.Fatalf("start with OwnerId set: got %d, want it accepted; body %s", rec.Code, rec.Body.String())
	}
	waitForState(t, h, token, sv.ID, "running")
}

// TestPower_RestartRefusedWhileRequiredSettingEmpty — a restart boots the game
// too, so it is gated the same way, and a refused restart leaves the running
// server alone rather than stopping it first.
func TestPower_RestartRefusedWhileRequiredSettingEmpty(t *testing.T) {
	h, st := newTestServerStore(t)
	token := login(t, h)
	addr, rt := startFakeAgentRuntime(t, "node-x")
	nodeID := registerNode(t, h, token, addr)
	specID := createSpecWithRequiredSetting(t, h, token, "owned-restart")
	sv := seedOfflineServer(t, st, "sv-owned-restart", nodeID, specID, func(s *store.Server) {
		s.State = store.StateRunning
	})

	rec := do(t, h, http.MethodPost, "/api/v1/servers/"+sv.ID+"/power", token, map[string]string{"action": "restart"})
	assertRequiredRefusal(t, rec.Code, rec.Body.Bytes())
	if got := getServerState(t, h, token, sv.ID); got != "running" {
		t.Fatalf("refused restart changed the state to %q", got)
	}
	if n := len(rt.InstallScripts(sv.ID)); n != 0 {
		t.Fatalf("refused restart ran %d install passes", n)
	}
}

// TestPower_StopNeverBlockedByRequiredSettings — the gate is about booting a
// server into a crash. Refusing to STOP one would be the opposite of safe.
func TestPower_StopNeverBlockedByRequiredSettings(t *testing.T) {
	h, st := newTestServerStore(t)
	token := login(t, h)
	addr, _ := startFakeAgentRuntime(t, "node-x")
	nodeID := registerNode(t, h, token, addr)
	specID := createSpecWithRequiredSetting(t, h, token, "owned-stop")
	sv := seedOfflineServer(t, st, "sv-owned-stop", nodeID, specID, func(s *store.Server) {
		s.State = store.StateRunning
	})

	rec := do(t, h, http.MethodPost, "/api/v1/servers/"+sv.ID+"/power", token, map[string]string{"action": "stop"})
	if rec.Code != http.StatusOK {
		t.Fatalf("stop with a required setting blank: got %d, want 200; body %s", rec.Code, rec.Body.String())
	}
}
