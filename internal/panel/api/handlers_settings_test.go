package api_test

import (
	"encoding/json"
	"net/http"
	"testing"
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
