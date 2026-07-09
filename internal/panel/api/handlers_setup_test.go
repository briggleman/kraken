package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/briggleman/kraken/internal/panel"
	"github.com/briggleman/kraken/internal/panel/api"
	"github.com/briggleman/kraken/internal/panel/config"
	"github.com/briggleman/kraken/internal/panel/store/memory"
)

// newSetupServer builds a Panel whose bootstrap admin still has the first-run
// must-change-password flag set (unlike newTestServer, which clears it).
func newSetupServer(t *testing.T) http.Handler {
	t.Helper()
	st := memory.New()
	cfg := &config.Config{
		Env:                    "test",
		SessionTTL:             time.Hour,
		BootstrapAdminUser:     testAdmin,
		BootstrapAdminPassword: testPass,
		// httptest requests arrive from 192.0.2.1 (TEST-NET-1, public) — allow
		// it so the /setup internal-network gate doesn't 403 the whole suite.
		SetupAllowedCIDRs: []string{"192.0.2.0/24"},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := panel.Seed(context.Background(), st, cfg, logger); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return api.New(cfg, st, logger).Handler()
}

// TestMustChangePasswordGate verifies the first-run gate: a bootstrap admin can
// read its own state but is blocked from feature endpoints until it rotates the
// password, after which it has full access on a fresh (rotated) session.
func TestMustChangePasswordGate(t *testing.T) {
	h := newSetupServer(t)
	token := login(t, h)

	// Allow-listed endpoints work despite the pending change, and /me exposes the flag.
	meRec := do(t, h, http.MethodGet, "/api/v1/auth/me", token, nil)
	if meRec.Code != http.StatusOK {
		t.Fatalf("me should be allowed for must-change user, got %d", meRec.Code)
	}
	var me struct {
		User struct {
			MustChangePassword bool `json:"must_change_password"`
		} `json:"user"`
	}
	_ = json.Unmarshal(meRec.Body.Bytes(), &me)
	if !me.User.MustChangePassword {
		t.Fatal("/auth/me must expose must_change_password=true on a fresh admin")
	}
	rec := do(t, h, http.MethodGet, "/api/v1/setup/status", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("setup/status should be allowed, got %d", rec.Code)
	}
	var st setupStatusBody
	_ = json.Unmarshal(rec.Body.Bytes(), &st)
	if !st.AdminMustChangePassword {
		t.Fatal("expected admin_must_change_password=true on fresh install")
	}

	// A feature endpoint is gated with the machine-readable code.
	rec = do(t, h, http.MethodGet, "/api/v1/specs", token, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 on /specs before password change, got %d", rec.Code)
	}
	var errBody struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &errBody)
	if errBody.Code != "password_change_required" {
		t.Fatalf("expected code password_change_required, got %q", errBody.Code)
	}

	// Wrong current password is rejected.
	rec = do(t, h, http.MethodPost, "/api/v1/auth/change-password", token, map[string]string{
		"current_password": "wrong", "new_password": "new-strong-pass",
	})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for wrong current password, got %d", rec.Code)
	}

	// Successful change clears the flag and rotates the session.
	rec = do(t, h, http.MethodPost, "/api/v1/auth/change-password", token, map[string]string{
		"current_password": testPass, "new_password": "new-strong-pass",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("change-password: status %d, body %s", rec.Code, rec.Body.String())
	}
	var changed struct {
		Token string `json:"token"`
		User  struct {
			MustChangePassword bool `json:"must_change_password"`
		} `json:"user"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &changed); err != nil {
		t.Fatalf("change-password decode: %v", err)
	}
	if changed.Token == "" || changed.Token == token {
		t.Fatal("change-password should return a new, rotated session token")
	}
	if changed.User.MustChangePassword {
		t.Fatal("must_change_password should be false after change")
	}

	// Old token is invalid; new token has full access.
	if rec := do(t, h, http.MethodGet, "/api/v1/auth/me", token, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("old token should be invalid after rotation, got %d", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, "/api/v1/specs", changed.Token, nil); rec.Code != http.StatusOK {
		t.Fatalf("specs should be accessible after password change, got %d", rec.Code)
	}
}

// setupStatusBody mirrors the setup/status response for assertions.
type setupStatusBody struct {
	AdminMustChangePassword bool `json:"admin_must_change_password"`
	HasNodeOnline           bool `json:"has_node_online"`
	HasSpec                 bool `json:"has_spec"`
	HasServer               bool `json:"has_server"`
	SetupComplete           bool `json:"setup_complete"`
}

// TestCatalogListAndImport covers the bundled catalog: listing, one-click import,
// the already-imported flag, and the 409 on re-import.
func TestCatalogListAndImport(t *testing.T) {
	h := newTestServer(t) // admin already past the password gate
	token := login(t, h)

	rec := do(t, h, http.MethodGet, "/api/v1/catalog", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list catalog: status %d, body %s", rec.Code, rec.Body.String())
	}
	var list struct {
		Catalog []struct {
			ID              string `json:"id"`
			AlreadyImported bool   `json:"already_imported"`
		} `json:"catalog"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("catalog decode: %v", err)
	}
	if len(list.Catalog) == 0 {
		t.Fatal("expected a non-empty catalog")
	}
	for _, c := range list.Catalog {
		if c.AlreadyImported {
			t.Fatalf("nothing should be imported yet, but %q is", c.ID)
		}
	}

	// Import the first entry.
	id := list.Catalog[0].ID
	rec = do(t, h, http.MethodPost, "/api/v1/catalog/"+id+"/import", token, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("import %q: status %d, body %s", id, rec.Code, rec.Body.String())
	}

	// Re-import is a conflict.
	rec = do(t, h, http.MethodPost, "/api/v1/catalog/"+id+"/import", token, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("re-import should 409, got %d", rec.Code)
	}

	// The list now flags it imported.
	rec = do(t, h, http.MethodGet, "/api/v1/catalog", token, nil)
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	var found bool
	for _, c := range list.Catalog {
		if c.ID == id {
			found = true
			if !c.AlreadyImported {
				t.Fatalf("%q should be flagged already_imported", id)
			}
		}
	}
	if !found {
		t.Fatalf("imported id %q missing from catalog list", id)
	}

	// Unknown id → 404.
	if rec := do(t, h, http.MethodPost, "/api/v1/catalog/nope/import", token, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("import unknown id should 404, got %d", rec.Code)
	}
}
