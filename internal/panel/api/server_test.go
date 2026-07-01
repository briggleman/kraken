package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/briggleman/kraken/internal/panel"
	"github.com/briggleman/kraken/internal/panel/api"
	"github.com/briggleman/kraken/internal/panel/config"
	"github.com/briggleman/kraken/internal/panel/store/memory"
)

const (
	testAdmin = "admin"
	testPass  = "abyss-key"
)

func newTestServer(t *testing.T) http.Handler {
	t.Helper()
	h, _ := newTestServerStore(t)
	return h
}

// newTestServerStore is newTestServer but also returns the backing store, so
// tests can seed records (e.g. a server) that would otherwise require a full
// deploy flow.
func newTestServerStore(t *testing.T) (http.Handler, *memory.Store) {
	t.Helper()
	st := memory.New()
	cfg := &config.Config{
		Env:                    "test",
		SessionTTL:             time.Hour,
		BootstrapAdminUser:     testAdmin,
		BootstrapAdminPassword: testPass,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := panel.Seed(context.Background(), st, cfg, logger); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// The bootstrap admin is seeded with must-change-password set (first-run
	// security). Clear it here so the broader test suite exercises its endpoints
	// directly; the gate itself is covered by dedicated tests in handlers_setup_test.go.
	clearMustChangePassword(t, st, testAdmin)
	return api.New(cfg, st, logger).Handler(), st
}

// clearMustChangePassword removes the first-run password-change gate from the
// named user, simulating an admin who has already rotated their credential.
func clearMustChangePassword(t *testing.T, st *memory.Store, username string) {
	t.Helper()
	u, err := st.GetUserByUsername(context.Background(), username)
	if err != nil {
		t.Fatalf("clear must-change: get user: %v", err)
	}
	u.MustChangePassword = false
	if err := st.UpdateUser(context.Background(), u); err != nil {
		t.Fatalf("clear must-change: update user: %v", err)
	}
}

func do(t *testing.T, h http.Handler, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		rdr = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func login(t *testing.T, h http.Handler) string {
	t.Helper()
	rec := do(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]string{
		"username": testAdmin, "password": testPass,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("login: status %d, body %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("login: decode: %v", err)
	}
	if resp.Token == "" {
		t.Fatal("login: empty token")
	}
	return resp.Token
}

func TestHealth(t *testing.T) {
	h := newTestServer(t)
	rec := do(t, h, http.MethodGet, "/healthz", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz: status %d", rec.Code)
	}
}

func TestLoginBadCredentials(t *testing.T) {
	h := newTestServer(t)
	rec := do(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]string{
		"username": testAdmin, "password": "wrong",
	})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestSpecsRequireAuth(t *testing.T) {
	h := newTestServer(t)
	rec := do(t, h, http.MethodGet, "/api/v1/specs", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", rec.Code)
	}
}

func TestLoginMeAndSpecCRUD(t *testing.T) {
	h := newTestServer(t)
	token := login(t, h)

	// me
	rec := do(t, h, http.MethodGet, "/api/v1/auth/me", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("me: status %d, body %s", rec.Code, rec.Body.String())
	}

	// create a valid spec
	specBody := map[string]any{
		"name": "Valheim",
		"slug": "valheim",
		"platforms": []map[string]string{
			{"kind": "linux-native", "image": "registry/kraken/steam-base:latest"},
		},
		"install": map[string]any{"script": "steamcmd +login anonymous +app_update 896660 +quit"},
		"startup": map[string]any{
			"command": "./valheim_server",
			"stop":    map[string]string{"type": "signal", "value": "SIGINT"},
		},
		"ports": []map[string]any{
			{"name": "game", "protocol": "udp", "default": 2456, "required": true},
		},
		"resources": map[string]int{"min_memory_mb": 2048},
	}
	rec = do(t, h, http.MethodPost, "/api/v1/specs", token, specBody)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create spec: status %d, body %s", rec.Code, rec.Body.String())
	}
	var created struct {
		ID      string `json:"id"`
		Version int    `json:"version"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("create spec decode: %v", err)
	}
	if created.ID == "" || created.Version != 1 {
		t.Fatalf("create spec: server should assign id + version 1, got %+v", created)
	}

	// list includes it
	rec = do(t, h, http.MethodGet, "/api/v1/specs", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list specs: status %d", rec.Code)
	}
	var list struct {
		Specs []struct {
			Slug string `json:"slug"`
		} `json:"specs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("list specs decode: %v", err)
	}
	if len(list.Specs) != 1 || list.Specs[0].Slug != "valheim" {
		t.Fatalf("expected one valheim spec, got %+v", list.Specs)
	}

	// invalid spec rejected
	rec = do(t, h, http.MethodPost, "/api/v1/specs", token, map[string]any{"name": "x"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid spec, got %d", rec.Code)
	}

	// logout invalidates the token
	rec = do(t, h, http.MethodPost, "/api/v1/auth/logout", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("logout: status %d", rec.Code)
	}
	rec = do(t, h, http.MethodGet, "/api/v1/auth/me", token, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("me after logout: expected 401, got %d", rec.Code)
	}
}
