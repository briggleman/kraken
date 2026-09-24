package api_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func createSpec(t *testing.T, h http.Handler, token, slug string) string {
	t.Helper()
	rec := do(t, h, http.MethodPost, "/api/v1/specs", token, map[string]any{
		"name": "Counter-Strike 2", "slug": slug,
		"steam_app_ids": map[string]int{"linux": 730},
		"platforms":     []map[string]string{{"kind": "linux-native", "image": "registry/kraken/steam-base:latest"}},
		"install":       map[string]any{"script": "steamcmd +login anonymous +app_update {{APP_ID}} +quit"},
		"startup": map[string]any{
			"command": "./cs2 -dedicated +maxplayers {{MAX_PLAYERS}} -port {{PORT_GAME}}",
			"stop":    map[string]string{"type": "signal", "value": "SIGINT"},
		},
		"variables": []map[string]any{
			{"key": "MAX_PLAYERS", "default": "16", "user_editable": true},
		},
		"ports":     []map[string]any{{"name": "game", "protocol": "udp", "default": 27015, "required": true}},
		"resources": map[string]int{"min_memory_mb": 2048},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create spec: status %d, body %s", rec.Code, rec.Body.String())
	}
	var sp struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &sp)
	if sp.ID == "" {
		t.Fatal("create spec: empty id")
	}
	return sp.ID
}

func getServerState(t *testing.T, h http.Handler, token, id string) string {
	t.Helper()
	rec := do(t, h, http.MethodGet, "/api/v1/servers/"+id, token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get server: status %d, body %s", rec.Code, rec.Body.String())
	}
	var sv struct {
		State string `json:"state"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &sv)
	return sv.State
}

// waitForState polls the server until it reaches want, failing the test if it
// does not within a few seconds. Several paths here are asynchronous (the
// install goroutine, the update-on-start pass), so the state is never asserted
// straight off the response that kicked one off.
func waitForState(t *testing.T, h http.Handler, token, id, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		got := getServerState(t, h, token, id)
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("server %s did not reach %q; state=%s", id, want, got)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func TestServerLifecycle_CreateInstallStart(t *testing.T) {
	h := newTestServer(t)
	token := login(t, h)
	addr := startFakeAgent(t, "node-x")
	nodeID := registerNode(t, h, token, addr) // linux, wine-enabled, 16GB, ports 27000-27100
	// Contact the node so the Panel marks it online (schedulable).
	if rec := do(t, h, http.MethodGet, "/api/v1/nodes/"+nodeID+"/info", token, nil); rec.Code != http.StatusOK {
		t.Fatalf("node info: status %d, body %s", rec.Code, rec.Body.String())
	}
	specID := createSpec(t, h, token, "cs2-lifecycle")

	// Create the server → scheduled + installing.
	rec := do(t, h, http.MethodPost, "/api/v1/servers", token, map[string]any{
		"spec_id": specID, "name": "leviathan-01",
		"variables": map[string]string{"MAX_PLAYERS": "32"},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create server: status %d, body %s", rec.Code, rec.Body.String())
	}
	var created struct {
		ID    string            `json:"id"`
		State string            `json:"state"`
		Kind  string            `json:"kind"`
		Ports map[string]int    `json:"ports"`
		Vars  map[string]string `json:"vars"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created server: %v", err)
	}
	if created.ID == "" || created.State != "installing" {
		t.Fatalf("expected installing server with id, got %+v", created)
	}
	if created.Kind != "linux-native" {
		t.Fatalf("expected linux-native placement, got %q", created.Kind)
	}
	// Port allocated from the node pool (prefers spec default 27015).
	if created.Ports["game"] != 27015 {
		t.Fatalf("expected game port 27015, got %d", created.Ports["game"])
	}
	// User override applied; injected PORT_GAME present.
	if created.Vars["MAX_PLAYERS"] != "32" {
		t.Fatalf("expected MAX_PLAYERS override 32, got %q", created.Vars["MAX_PLAYERS"])
	}
	if created.Vars["APP_ID"] != "730" || created.Vars["PORT_GAME"] != "27015" {
		t.Fatalf("expected injected APP_ID/PORT_GAME, got %+v", created.Vars)
	}

	// Wait for install (fake agent) to flip the server to offline.
	waitForState(t, h, token, created.ID, "offline")

	// Start it. The install just ran, so this first start is the plain
	// synchronous call — no update-on-start pass (#307) over a tree that is
	// seconds old. This assertion briefly expected a 202 update pass here, which
	// recorded exactly the redundant double install a fresh deploy suffered.
	// Update-on-start itself is covered in handlers_server_update_test.go.
	rec = do(t, h, http.MethodPost, "/api/v1/servers/"+created.ID+"/power", token, map[string]string{"action": "start"})
	if rec.Code != http.StatusOK {
		t.Fatalf("power start: status %d, body %s", rec.Code, rec.Body.String())
	}
	waitForState(t, h, token, created.ID, "running")

	// Stop it → offline.
	rec = do(t, h, http.MethodPost, "/api/v1/servers/"+created.ID+"/power", token, map[string]string{"action": "stop"})
	if rec.Code != http.StatusOK {
		t.Fatalf("power stop: status %d", rec.Code)
	}
	if st := getServerState(t, h, token, created.ID); st != "offline" {
		t.Fatalf("expected offline after stop, got %q", st)
	}

	// Retire it, then delete it permanently.
	retireServer(t, h, token, created.ID)
	rec = do(t, h, http.MethodDelete, "/api/v1/servers/"+created.ID, token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete server: status %d", rec.Code)
	}
}

func TestServerCreate_NoCapacity(t *testing.T) {
	h := newTestServer(t)
	token := login(t, h)
	// No nodes registered → scheduler can't place.
	specID := createSpec(t, h, token, "cs2-nocap")
	rec := do(t, h, http.MethodPost, "/api/v1/servers", token, map[string]any{
		"spec_id": specID, "name": "orphan",
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 with no nodes, got %d (body %s)", rec.Code, rec.Body.String())
	}
}
