package api_test

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestDatabaseConfig_MemoryDefault(t *testing.T) {
	h := newTestServer(t)
	token := login(t, h)

	rec := do(t, h, http.MethodGet, "/api/v1/setup/database", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get database: %d %s", rec.Code, rec.Body.String())
	}
	var v struct {
		UsingMemory bool `json:"using_memory"`
		EnvLocked   bool `json:"env_locked"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !v.UsingMemory {
		t.Fatal("expected using_memory=true for the in-memory test server")
	}
	if v.EnvLocked {
		t.Fatal("expected env_locked=false (no KRAKEN_DATABASE_URL in tests)")
	}
}

func TestDatabaseTest_ValidatesInput(t *testing.T) {
	h := newTestServer(t)
	token := login(t, h)

	// Missing host/user → 400 before any connection is attempted.
	rec := do(t, h, http.MethodPost, "/api/v1/setup/database/test", token, map[string]any{"port": 5432})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing host/user, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestDatabaseConnect_ValidatesInput(t *testing.T) {
	h := newTestServer(t)
	token := login(t, h)

	rec := do(t, h, http.MethodPost, "/api/v1/setup/database", token, map[string]any{"user": "kraken"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing host, got %d %s", rec.Code, rec.Body.String())
	}
}

// TestSetupStatus_ReportsMemory confirms the wizard can see the datastore type.
func TestSetupStatus_ReportsMemory(t *testing.T) {
	h := newTestServer(t)
	token := login(t, h)
	rec := do(t, h, http.MethodGet, "/api/v1/setup/status", token, nil)
	var v struct {
		UsingMemory bool `json:"using_memory"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	if !v.UsingMemory {
		t.Fatal("expected using_memory=true on the in-memory test server")
	}
}
