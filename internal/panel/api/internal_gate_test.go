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

// newGateServer builds a Panel with a specific setup allowlist (nil → the
// private-network defaults). httptest requests arrive from 192.0.2.1
// (TEST-NET-1), which is public — outside the defaults.
func newGateServer(t *testing.T, cidrs []string) http.Handler {
	t.Helper()
	st := memory.New()
	cfg := &config.Config{
		Env:                    "test",
		SessionTTL:             time.Hour,
		BootstrapAdminUser:     testAdmin,
		BootstrapAdminPassword: testPass,
		SetupAllowedCIDRs:      cidrs,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := panel.Seed(context.Background(), st, cfg, logger); err != nil {
		t.Fatalf("seed: %v", err)
	}
	clearMustChangePassword(t, st, testAdmin)
	return api.New(cfg, st, logger).Handler()
}

func TestSetupGateDeniesPublicSource(t *testing.T) {
	// Default allowlist = private networks only → the TEST-NET peer is denied,
	// even with a valid admin session.
	h := newGateServer(t, nil)
	token := login(t, h)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/setup/status"},
		{http.MethodGet, "/api/v1/setup/database"},
		{http.MethodPost, "/api/v1/setup/dismiss"},
	} {
		rec := do(t, h, tc.method, tc.path, token, nil)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s %s from a public source = %d; want 403", tc.method, tc.path, rec.Code)
		}
	}
	// Unauthenticated local-enroll is gated too.
	rec := do(t, h, http.MethodPost, "/api/v1/setup/local-enroll", "", nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("local-enroll from a public source = %d; want 403", rec.Code)
	}
}

func TestSetupGateAllowsConfiguredCIDR(t *testing.T) {
	h := newGateServer(t, []string{"192.0.2.0/24"})
	token := login(t, h)
	rec := do(t, h, http.MethodGet, "/api/v1/setup/status", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("setup/status from an allowed CIDR = %d; want 200", rec.Code)
	}
}

func TestSetupDismissLatch(t *testing.T) {
	h := newGateServer(t, []string{"192.0.2.0/24"})
	token := login(t, h)

	// Fresh install: nothing deployed → not complete.
	rec := do(t, h, http.MethodGet, "/api/v1/setup/status", token, nil)
	var st struct {
		SetupComplete bool `json:"setup_complete"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if st.SetupComplete {
		t.Fatal("fresh install should not report setup_complete")
	}

	// Explicit dismissal latches permanently.
	if rec := do(t, h, http.MethodPost, "/api/v1/setup/dismiss", token, nil); rec.Code != http.StatusOK {
		t.Fatalf("dismiss = %d; want 200", rec.Code)
	}
	rec = do(t, h, http.MethodGet, "/api/v1/setup/status", token, nil)
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if !st.SetupComplete {
		t.Fatal("setup_complete should stay true after dismissal")
	}
}
