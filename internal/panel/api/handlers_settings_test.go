package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/briggleman/kraken/internal/panel/store"
)

func createSettingsSpec(t *testing.T, h http.Handler, token string) string {
	t.Helper()
	rec := do(t, h, http.MethodPost, "/api/v1/specs", token, map[string]any{
		"name": "Valheim", "slug": "valheim-settings",
		"platforms": []map[string]string{{"kind": "linux-native", "image": "busybox:latest"}},
		"install":   map[string]any{"script": "echo install"},
		"startup": map[string]any{
			"command": "./run",
			"stop":    map[string]string{"type": "signal", "value": "SIGINT"},
		},
		"ports": []map[string]any{{"name": "game", "protocol": "udp", "default": 2456, "required": true}},
		"settings": map[string]any{
			"groups": []map[string]any{
				{
					"id": "world", "label": "World",
					"fields": []map[string]any{
						{"key": "world_name", "label": "World name", "type": "string", "default": "Midgard"},
						{"key": "max_players", "label": "Max players", "type": "int", "default": "16", "min": 1, "max": 64},
					},
				},
			},
		},
		"config_files": []map[string]any{
			{"path": "/data/server.cfg", "format": "source-cvar", "bindings": map[string]any{
				"servername": "world_name",
				"maxplayers": "max_players",
			}},
		},
		"resources": map[string]int{"min_memory_mb": 256},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create settings spec: %d %s", rec.Code, rec.Body.String())
	}
	var sp struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &sp)
	return sp.ID
}

// A spec with no settings block must return groups: [] (not null) — the
// Settings tab crashed to a black screen dereferencing null.length.
func TestServerSettings_NoSettingsBlock_ReturnsEmptyGroups(t *testing.T) {
	h := newTestServer(t)
	token := login(t, h)
	addr := startFakeAgent(t, "node-x")
	nodeID := registerNode(t, h, token, addr)
	if rec := do(t, h, http.MethodGet, "/api/v1/nodes/"+nodeID+"/info", token, nil); rec.Code != http.StatusOK {
		t.Fatalf("node info: %d", rec.Code)
	}
	rec := do(t, h, http.MethodPost, "/api/v1/specs", token, map[string]any{
		"name": "Launch-args only", "slug": "no-settings",
		"platforms": []map[string]string{{"kind": "linux-native", "image": "busybox:latest"}},
		"install":   map[string]any{"script": "echo install"},
		"startup": map[string]any{
			"command": "./run",
			"stop":    map[string]string{"type": "signal", "value": "SIGINT"},
		},
		"ports":     []map[string]any{{"name": "game", "protocol": "udp", "default": 2456, "required": true}},
		"resources": map[string]int{"min_memory_mb": 256},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create spec: %d %s", rec.Code, rec.Body.String())
	}
	var sp struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &sp)

	rec = do(t, h, http.MethodPost, "/api/v1/servers", token, map[string]any{"spec_id": sp.ID, "name": "no-settings-01"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create server: %d %s", rec.Code, rec.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)

	rec = do(t, h, http.MethodGet, "/api/v1/servers/"+created.ID+"/settings", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get settings: %d %s", rec.Code, rec.Body.String())
	}
	var raw struct {
		Groups json.RawMessage `json:"groups"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(raw.Groups) != "[]" {
		t.Fatalf("groups must serialize as [], got %s", raw.Groups)
	}
}

// Launch variables surface on the Settings tab with the server's values; they
// are editable only while the server is stopped and only when user_editable.
func TestServerSettings_LaunchVariables(t *testing.T) {
	h, st := newTestServerStore(t)
	token := login(t, h)
	addr := startFakeAgent(t, "node-x")
	nodeID := registerNode(t, h, token, addr)
	if rec := do(t, h, http.MethodGet, "/api/v1/nodes/"+nodeID+"/info", token, nil); rec.Code != http.StatusOK {
		t.Fatalf("node info: %d", rec.Code)
	}
	rec := do(t, h, http.MethodPost, "/api/v1/specs", token, map[string]any{
		"name": "Valheim-like", "slug": "varsgame",
		"platforms": []map[string]string{{"kind": "linux-native", "image": "busybox:latest"}},
		"install":   map[string]any{"script": "echo install"},
		"startup": map[string]any{
			"command": "./run -name {{SERVER_NAME}}",
			"stop":    map[string]string{"type": "signal", "value": "SIGINT"},
		},
		"ports": []map[string]any{{"name": "game", "protocol": "udp", "default": 2456, "required": true}},
		"variables": []map[string]any{
			{"key": "SERVER_NAME", "label": "Server name", "default": "Kraken", "user_editable": true},
			{"key": "INTERNAL_FLAG", "default": "locked", "user_editable": false},
		},
		"resources": map[string]int{"min_memory_mb": 256},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create spec: %d %s", rec.Code, rec.Body.String())
	}
	var sp struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &sp)

	rec = do(t, h, http.MethodPost, "/api/v1/servers", token, map[string]any{"spec_id": sp.ID, "name": "vars-01"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create server: %d %s", rec.Code, rec.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)

	// GET exposes the variables with the server's resolved values.
	rec = do(t, h, http.MethodGet, "/api/v1/servers/"+created.ID+"/settings", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get settings: %d %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Variables []struct {
			Key          string `json:"key"`
			Value        string `json:"value"`
			UserEditable bool   `json:"user_editable"`
		} `json:"variables"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got.Variables) != 2 || got.Variables[0].Key != "SERVER_NAME" || got.Variables[0].Value != "Kraken" || !got.Variables[0].UserEditable {
		t.Fatalf("unexpected variables: %+v", got.Variables)
	}

	// Editing a user-editable variable on a stopped server persists.
	rec = do(t, h, http.MethodPut, "/api/v1/servers/"+created.ID+"/settings", token, map[string]any{
		"variables": map[string]string{"SERVER_NAME": "Midgard"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("update variables: %d %s", rec.Code, rec.Body.String())
	}
	sv, err := st.GetServer(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("get server: %v", err)
	}
	if sv.Vars["SERVER_NAME"] != "Midgard" {
		t.Fatalf("variable not persisted: %q", sv.Vars["SERVER_NAME"])
	}

	// Non-editable variables are rejected.
	rec = do(t, h, http.MethodPut, "/api/v1/servers/"+created.ID+"/settings", token, map[string]any{
		"variables": map[string]string{"INTERNAL_FLAG": "changed"},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("non-editable variable: expected 400, got %d (%s)", rec.Code, rec.Body.String())
	}

	// Shell metacharacters are rejected (CWE-78 guard).
	rec = do(t, h, http.MethodPut, "/api/v1/servers/"+created.ID+"/settings", token, map[string]any{
		"variables": map[string]string{"SERVER_NAME": "pwn; rm -rf /"},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("shell metachar: expected 400, got %d (%s)", rec.Code, rec.Body.String())
	}

	// A running server locks launch variables.
	sv.State = store.StateRunning
	if err := st.UpdateServer(context.Background(), sv); err != nil {
		t.Fatalf("set running: %v", err)
	}
	rec = do(t, h, http.MethodPut, "/api/v1/servers/"+created.ID+"/settings", token, map[string]any{
		"variables": map[string]string{"SERVER_NAME": "TooLate"},
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("running server: expected 409, got %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestServerSettings_GetAndUpdate(t *testing.T) {
	h := newTestServer(t)
	token := login(t, h)
	addr := startFakeAgent(t, "node-x")
	nodeID := registerNode(t, h, token, addr)
	if rec := do(t, h, http.MethodGet, "/api/v1/nodes/"+nodeID+"/info", token, nil); rec.Code != http.StatusOK {
		t.Fatalf("node info: %d", rec.Code)
	}
	specID := createSettingsSpec(t, h, token)

	rec := do(t, h, http.MethodPost, "/api/v1/servers", token, map[string]any{"spec_id": specID, "name": "leviathan-01"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create server: %d %s", rec.Code, rec.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)

	// GET settings → groups + default values.
	rec = do(t, h, http.MethodGet, "/api/v1/servers/"+created.ID+"/settings", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get settings: %d %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Groups []struct {
			ID     string `json:"id"`
			Fields []struct {
				Key string `json:"key"`
			} `json:"fields"`
		} `json:"groups"`
		Values map[string]string `json:"values"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode settings: %v", err)
	}
	if len(got.Groups) != 1 || got.Groups[0].ID != "world" {
		t.Fatalf("unexpected groups: %+v", got.Groups)
	}
	if got.Values["world_name"] != "Midgard" || got.Values["max_players"] != "16" {
		t.Fatalf("unexpected default values: %+v", got.Values)
	}

	// PUT a valid override → applied (spec has config_files).
	rec = do(t, h, http.MethodPut, "/api/v1/servers/"+created.ID+"/settings", token, map[string]any{
		"values": map[string]string{"world_name": "Asgard", "max_players": "32"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("put settings: %d %s", rec.Code, rec.Body.String())
	}
	var upd struct {
		Values  map[string]string `json:"values"`
		Applied bool              `json:"applied"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &upd)
	if !upd.Applied {
		t.Fatal("expected config to be applied")
	}
	if upd.Values["world_name"] != "Asgard" || upd.Values["max_players"] != "32" {
		t.Fatalf("values not updated: %+v", upd.Values)
	}

	// Invalid value (out of range) → 400.
	rec = do(t, h, http.MethodPut, "/api/v1/servers/"+created.ID+"/settings", token, map[string]any{
		"values": map[string]string{"max_players": "999"},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for out-of-range, got %d", rec.Code)
	}

	// GET reflects the persisted update.
	rec = do(t, h, http.MethodGet, "/api/v1/servers/"+created.ID+"/settings", token, nil)
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Values["world_name"] != "Asgard" {
		t.Fatalf("update not persisted: %+v", got.Values)
	}
}
