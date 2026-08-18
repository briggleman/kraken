package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	panel "github.com/briggleman/kraken/internal/panel"
	"github.com/briggleman/kraken/internal/panel/api"
	"github.com/briggleman/kraken/internal/panel/config"
	"github.com/briggleman/kraken/internal/panel/store/memory"
	"github.com/briggleman/kraken/internal/shared/mtls"
)

// newEnrollTestServer is newTestServer with CA signing material configured, so
// the enrollment endpoints are live instead of 503ing.
func newEnrollTestServer(t *testing.T) (http.Handler, []byte) {
	t.Helper()
	caCert, caKey, err := mtls.GenerateCA()
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "ca.pem"), filepath.Join(dir, "ca-key.pem")
	for p, b := range map[string][]byte{certFile: caCert, keyFile: caKey} {
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	st := memory.New()
	cfg := &config.Config{
		Env:                    "test",
		SessionTTL:             time.Hour,
		BootstrapAdminUser:     testAdmin,
		BootstrapAdminPassword: testPass,
		SetupAllowedCIDRs:      []string{"192.0.2.0/24"},
		CACert:                 certFile,
		CAKey:                  keyFile,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := panel.Seed(context.Background(), st, cfg, logger); err != nil {
		t.Fatalf("seed: %v", err)
	}
	clearMustChangePassword(t, st, testAdmin)
	return api.New(cfg, st, logger).Handler(), caCert
}

// TestCreateBootstrapTokenIncludesCAFingerprint — the token response must carry
// the full CA fingerprint so the Add Node dialog can embed a pin in the
// generated install command.
func TestCreateBootstrapTokenIncludesCAFingerprint(t *testing.T) {
	h, caCert := newEnrollTestServer(t)
	tok := login(t, h)

	rec := do(t, h, http.MethodPost, "/api/v1/agents/bootstrap-tokens", tok, map[string]any{"node_name": "test-node"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Token         string `json:"token"`
		CAFingerprint string `json:"ca_fingerprint"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Token == "" {
		t.Error("response missing token")
	}
	want, err := mtls.CAFingerprintPEM(caCert)
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	if resp.CAFingerprint != want {
		t.Errorf("ca_fingerprint = %q, want %q", resp.CAFingerprint, want)
	}
	if len(resp.CAFingerprint) != 64 {
		t.Errorf("ca_fingerprint length = %d, want full 64-hex digest", len(resp.CAFingerprint))
	}
}
