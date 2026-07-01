package api_test

import (
	"encoding/json"
	"net/http"
	"testing"
)

// setupServer spins up a fake agent, registers + onlines a node, creates a spec
// and a server, returning the new server ID. Reused by the DNS tests.
func setupServer(t *testing.T, h http.Handler, token string) string {
	t.Helper()
	addr := startFakeAgent(t, "node-dns")
	nodeID := registerNode(t, h, token, addr)
	if rec := do(t, h, http.MethodGet, "/api/v1/nodes/"+nodeID+"/info", token, nil); rec.Code != http.StatusOK {
		t.Fatalf("node info: %d", rec.Code)
	}
	specID := createSettingsSpec(t, h, token)
	rec := do(t, h, http.MethodPost, "/api/v1/servers", token, map[string]any{"spec_id": specID, "name": "dns-srv"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create server: %d %s", rec.Code, rec.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	return created.ID
}

func TestPanelSettings_RoundTrip(t *testing.T) {
	h := newTestServer(t)
	token := login(t, h)

	get := func() bool {
		rec := do(t, h, http.MethodGet, "/api/v1/settings", token, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("get settings: %d %s", rec.Code, rec.Body.String())
		}
		var v struct {
			CloudflareConfigured bool `json:"cloudflare_configured"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &v)
		return v.CloudflareConfigured
	}

	if get() {
		t.Fatal("expected cloudflare unconfigured on a fresh panel")
	}
	if rec := do(t, h, http.MethodPut, "/api/v1/settings", token, map[string]any{"cloudflare_api_token": "tok-123"}); rec.Code != http.StatusOK {
		t.Fatalf("put settings: %d %s", rec.Code, rec.Body.String())
	}
	if !get() {
		t.Fatal("expected configured=true after saving a token")
	}
	// Empty string clears it.
	if rec := do(t, h, http.MethodPut, "/api/v1/settings", token, map[string]any{"cloudflare_api_token": ""}); rec.Code != http.StatusOK {
		t.Fatalf("clear token: %d", rec.Code)
	}
	if get() {
		t.Fatal("expected configured=false after clearing the token")
	}
}

func TestServerDNS_RequiresCloudflare(t *testing.T) {
	h := newTestServer(t)
	token := login(t, h)
	id := setupServer(t, h, token)

	// No Cloudflare token configured → 503.
	rec := do(t, h, http.MethodPut, "/api/v1/servers/"+id+"/dns", token, map[string]any{"name": "play.example.com"})
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 without Cloudflare configured, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestServerDNS_ValidatesName(t *testing.T) {
	h := newTestServer(t)
	token := login(t, h)
	id := setupServer(t, h, token)
	// Configure a token so we get past the 503 gate to name validation.
	if rec := do(t, h, http.MethodPut, "/api/v1/settings", token, map[string]any{"cloudflare_api_token": "tok"}); rec.Code != http.StatusOK {
		t.Fatalf("set token: %d", rec.Code)
	}
	rec := do(t, h, http.MethodPut, "/api/v1/servers/"+id+"/dns", token, map[string]any{"name": "not a hostname"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid hostname, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestServerDNS_GetAndDeleteEmpty(t *testing.T) {
	h := newTestServer(t)
	token := login(t, h)
	id := setupServer(t, h, token)

	rec := do(t, h, http.MethodGet, "/api/v1/servers/"+id+"/dns", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get dns: %d %s", rec.Code, rec.Body.String())
	}
	var got struct {
		CloudflareConfigured bool            `json:"cloudflare_configured"`
		TargetHost           string          `json:"target_host"`
		DNS                  json.RawMessage `json:"dns"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.TargetHost == "" {
		t.Fatal("expected a non-empty target_host for an online node")
	}
	if string(got.DNS) != "null" {
		t.Fatalf("expected dns null for a server with no DNS, got %s", got.DNS)
	}
	// Deleting when none is set is a no-op 200.
	if rec := do(t, h, http.MethodDelete, "/api/v1/servers/"+id+"/dns", token, nil); rec.Code != http.StatusOK {
		t.Fatalf("delete dns (none): %d", rec.Code)
	}
}
