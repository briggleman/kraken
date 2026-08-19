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

// newTunnelTestServer is newEnrollTestServer plus an enabled reverse-tunnel
// listener config, so tunnel-mode node registration is live. The listener
// itself is never started — registration only checks that it is configured.
func newTunnelTestServer(t *testing.T, tunnelAddr string) http.Handler {
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
		TunnelAddr:             tunnelAddr,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := panel.Seed(context.Background(), st, cfg, logger); err != nil {
		t.Fatalf("seed: %v", err)
	}
	clearMustChangePassword(t, st, testAdmin)
	return api.New(cfg, st, logger).Handler()
}

// TestRegisterTunnelNode — the whole tunnel registration contract: tunnel_id
// is required, address is not, the stored node carries the binding, and a
// second node cannot claim the same identity.
func TestRegisterTunnelNode(t *testing.T) {
	h := newTunnelTestServer(t, "127.0.0.1:0")
	tok := login(t, h)

	// Missing tunnel_id → rejected.
	rec := do(t, h, http.MethodPost, "/api/v1/nodes", tok, map[string]any{
		"connection_mode": "tunnel", "name": "deep-node",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("no tunnel_id: status %d, body %s", rec.Code, rec.Body.String())
	}

	// Bogus mode → rejected.
	rec = do(t, h, http.MethodPost, "/api/v1/nodes", tok, map[string]any{
		"connection_mode": "carrier-pigeon", "name": "deep-node",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad mode: status %d, body %s", rec.Code, rec.Body.String())
	}

	// Proper tunnel registration: no address needed.
	rec = do(t, h, http.MethodPost, "/api/v1/nodes", tok, map[string]any{
		"connection_mode": "tunnel", "tunnel_id": "ident-abc", "name": "deep-node",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("tunnel register: status %d, body %s", rec.Code, rec.Body.String())
	}
	var created struct {
		ID             string `json:"id"`
		ConnectionMode string `json:"connection_mode"`
		TunnelID       string `json:"tunnel_id"`
		Address        string `json:"address"`
		OS             string `json:"os"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ConnectionMode != "tunnel" || created.TunnelID != "ident-abc" || created.Address != "" {
		t.Fatalf("created node: %+v", created)
	}
	if created.OS != "linux" {
		t.Fatalf("tunnel node should default to linux until first contact, got %q", created.OS)
	}

	// A second node claiming the same identity → conflict.
	rec = do(t, h, http.MethodPost, "/api/v1/nodes", tok, map[string]any{
		"connection_mode": "tunnel", "tunnel_id": "ident-abc", "name": "impostor",
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate tunnel_id: status %d, body %s", rec.Code, rec.Body.String())
	}

	// Direct registration still requires an address.
	rec = do(t, h, http.MethodPost, "/api/v1/nodes", tok, map[string]any{"name": "classic"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("direct without address: status %d, body %s", rec.Code, rec.Body.String())
	}
}

// TestRegisterTunnelNodeListenerDisabled — with the tunnel listener off, a
// tunnel registration is refused with a message naming the fix.
func TestRegisterTunnelNodeListenerDisabled(t *testing.T) {
	h := newTunnelTestServer(t, "off")
	tok := login(t, h)
	rec := do(t, h, http.MethodPost, "/api/v1/nodes", tok, map[string]any{
		"connection_mode": "tunnel", "tunnel_id": "ident-abc", "name": "deep-node",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
}

// TestEnrollSurfacesTunnelID — redeeming a bootstrap token issues a cert whose
// identity is reported by the enroll-status endpoint, so the Add Node dialog
// can bind a tunnel registration to it.
func TestEnrollSurfacesTunnelID(t *testing.T) {
	h := newTunnelTestServer(t, "127.0.0.1:0")
	tok := login(t, h)

	rec := do(t, h, http.MethodPost, "/api/v1/agents/bootstrap-tokens", tok, map[string]any{"node_name": "deep-node"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("token: status %d, body %s", rec.Code, rec.Body.String())
	}
	var minted struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &minted); err != nil {
		t.Fatal(err)
	}

	_, csr, err := mtls.NewAgentKeyAndCSR(nil)
	if err != nil {
		t.Fatal(err)
	}
	rec = do(t, h, http.MethodPost, "/api/v1/agents/enroll", "", map[string]any{"token": minted.Token, "csr": string(csr)})
	if rec.Code != http.StatusOK {
		t.Fatalf("enroll: status %d, body %s", rec.Code, rec.Body.String())
	}

	rec = do(t, h, http.MethodGet, "/api/v1/agents/enroll-status?token="+minted.Token, tok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d, body %s", rec.Code, rec.Body.String())
	}
	var st struct {
		Status   string `json:"status"`
		TunnelID string `json:"tunnel_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st.Status != "redeemed" || st.TunnelID == "" {
		t.Fatalf("enroll status: %+v — a redeemed enrollment must carry its tunnel_id", st)
	}
}
