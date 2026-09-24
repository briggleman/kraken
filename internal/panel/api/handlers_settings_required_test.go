package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/briggleman/kraken/internal/panel/store"
)

// ownedSpecBody is the Dragonwilds-shaped spec of createSpecWithRequiredSetting
// with the required OwnerId's default as a parameter, plus a config file bound
// to it, so a test can PUT the same spec back with a default it gained.
func ownedSpecBody(slug, ownerDefault string) map[string]any {
	owner := map[string]any{"key": "OwnerId", "label": "Owner Player ID", "type": "string", "required": true}
	if ownerDefault != "" {
		owner["default"] = ownerDefault
	}
	return map[string]any{
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
				owner,
				{"key": "ServerName", "label": "Server name", "type": "string", "default": "Kraken"},
			},
		}}},
		"config_files": []map[string]any{{
			"path": "/data/owner.cfg", "format": "properties",
			"bindings": map[string]any{"owner": "OwnerId"},
		}},
	}
}

type serverSettingsView struct {
	Values   map[string]string `json:"values"`
	FromSpec []string          `json:"from_spec"`
}

func getSettings(t *testing.T, h http.Handler, token, id string) serverSettingsView {
	t.Helper()
	rec := do(t, h, http.MethodGet, "/api/v1/servers/"+id+"/settings", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get settings: status %d, body %s", rec.Code, rec.Body.String())
	}
	var v serverSettingsView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode settings: %v; body %s", err, rec.Body.String())
	}
	if v.FromSpec == nil {
		t.Fatalf("from_spec should always be an array; body %s", rec.Body.String())
	}
	return v
}

// TestSpecGainingDefault_LiftsStartGate — #367, end to end through the memory
// store. A server built while the spec's required OwnerId had no default stores
// it as "" (settings are stored in full at create) and is refused. Once
// PUT /specs/{id} gives the field a default, that stored blank yields to it: the
// gate lifts, the Settings tab reports the value as the spec's, a save of some
// other field does not freeze the default into the row, and the config file
// renders the default.
func TestSpecGainingDefault_LiftsStartGate(t *testing.T) {
	h, st := newTestServerStore(t)
	token := login(t, h)
	addr, rt := startFakeAgentRuntime(t, "node-x")
	nodeID := registerNode(t, h, token, addr)

	rec := do(t, h, http.MethodPost, "/api/v1/specs", token, ownedSpecBody("owned-gains-default", ""))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create spec: status %d, body %s", rec.Code, rec.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	specID := created.ID

	// Stored exactly as the create handler stores it: every field, the
	// default-less required one as a blank.
	sv := seedOfflineServer(t, st, "sv-gains-default", nodeID, specID, func(s *store.Server) {
		s.Settings = map[string]string{"OwnerId": "", "ServerName": "Kraken"}
	})

	rec = do(t, h, http.MethodPost, "/api/v1/servers/"+sv.ID+"/power", token, map[string]string{"action": "start"})
	assertRequiredRefusal(t, rec.Code, rec.Body.Bytes())
	if got := getSettings(t, h, token, sv.ID); got.Values["OwnerId"] != "" || len(got.FromSpec) != 0 {
		t.Fatalf("before the spec has a default: values %v, from_spec %v; want a blank OwnerId from the server", got.Values, got.FromSpec)
	}

	rec = do(t, h, http.MethodPut, "/api/v1/specs/"+specID, token, ownedSpecBody("owned-gains-default", "0002a"))
	if rec.Code != http.StatusOK {
		t.Fatalf("update spec: status %d, body %s", rec.Code, rec.Body.String())
	}

	got := getSettings(t, h, token, sv.ID)
	if got.Values["OwnerId"] != "0002a" {
		t.Fatalf("OwnerId after the spec gained a default: got %q, want 0002a", got.Values["OwnerId"])
	}
	if strings.Join(got.FromSpec, ",") != "OwnerId" {
		t.Fatalf("from_spec: got %v, want [OwnerId]", got.FromSpec)
	}

	// Saving an unrelated field keeps OwnerId following the spec: the row still
	// stores the blank, and the value is still reported as the spec's.
	rec = do(t, h, http.MethodPut, "/api/v1/servers/"+sv.ID+"/settings", token,
		map[string]any{"values": map[string]string{"ServerName": "Midgard"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("save ServerName: got %d; body %s", rec.Code, rec.Body.String())
	}
	var saved serverSettingsView
	_ = json.Unmarshal(rec.Body.Bytes(), &saved)
	if saved.Values["OwnerId"] != "0002a" || strings.Join(saved.FromSpec, ",") != "OwnerId" {
		t.Fatalf("save response: values %v, from_spec %v; want OwnerId 0002a from the spec", saved.Values, saved.FromSpec)
	}
	row, err := st.GetServer(context.Background(), sv.ID)
	if err != nil {
		t.Fatalf("load server: %v", err)
	}
	if v, ok := row.Settings["OwnerId"]; !ok || v != "" {
		t.Fatalf("a save of another field froze OwnerId into the row: %q (present %v)", v, ok)
	}

	rec = do(t, h, http.MethodPost, "/api/v1/servers/"+sv.ID+"/power", token, map[string]string{"action": "start"})
	if rec.Code != http.StatusAccepted && rec.Code != http.StatusOK {
		t.Fatalf("start after the spec gained a default: got %d, want it accepted; body %s", rec.Code, rec.Body.String())
	}
	waitForState(t, h, token, sv.ID, "running")

	// The config renders from the same effective settings the gate judged.
	data, _, _, _, err := rt.ReadFile(context.Background(), sv.ID, "/data/owner.cfg", 0)
	if err != nil {
		t.Fatalf("read rendered config: %v", err)
	}
	if !strings.Contains(string(data), "owner=0002a") {
		t.Fatalf("rendered config should carry the spec default, got %q", data)
	}
}

// TestSettingsGet_OperatorValueIsNotFromSpec — a value the operator saved is
// the server's own, whatever default the spec carries.
func TestSettingsGet_OperatorValueIsNotFromSpec(t *testing.T) {
	h, st := newTestServerStore(t)
	token := login(t, h)
	addr, _ := startFakeAgentRuntime(t, "node-x")
	nodeID := registerNode(t, h, token, addr)
	rec := do(t, h, http.MethodPost, "/api/v1/specs", token, ownedSpecBody("owned-operator", "0002a"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create spec: status %d, body %s", rec.Code, rec.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	sv := seedOfflineServer(t, st, "sv-operator", nodeID, created.ID, func(s *store.Server) {
		s.Settings = map[string]string{"OwnerId": "00ffee", "ServerName": "Kraken"}
	})
	got := getSettings(t, h, token, sv.ID)
	if got.Values["OwnerId"] != "00ffee" || len(got.FromSpec) != 0 {
		t.Fatalf("values %v, from_spec %v; want the operator's OwnerId and nothing from the spec", got.Values, got.FromSpec)
	}
}
