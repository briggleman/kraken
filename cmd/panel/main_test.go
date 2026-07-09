package main

import (
	"crypto/x509"
	"encoding/pem"
	"io"
	"log/slog"
	"testing"

	"github.com/briggleman/kraken/internal/panel/config"
	"github.com/briggleman/kraken/internal/shared/mtls"
)

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// TestEnsurePanelClient_AutoIssue — with no operator-provided TLS envs, the
// Panel signs itself a client cert in memory against its own CA and returns
// the PEM bundle (which api.New consumes via WithClientTLSBytes). No
// filesystem writes — sidesteps volume permission constraints entirely.
func TestEnsurePanelClient_AutoIssue(t *testing.T) {
	caCert, caKey, err := mtls.GenerateCA()
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	cfg := &config.Config{StateDir: "/some/state/dir"}

	certPEM, keyPEM, caPEM, err := ensurePanelClient(cfg, caCert, caKey, testLogger())
	if err != nil {
		t.Fatalf("ensurePanelClient: %v", err)
	}
	if len(certPEM) == 0 || len(keyPEM) == 0 || len(caPEM) == 0 {
		t.Fatalf("expected non-empty bundle, got cert=%d key=%d ca=%d bytes",
			len(certPEM), len(keyPEM), len(caPEM))
	}

	// Cert must chain to the CA when treated as a client cert.
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("cert PEM did not decode")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		t.Fatal("ca PEM did not load")
	}
	if _, err := cert.Verify(x509.VerifyOptions{
		Roots:     roots,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		t.Fatalf("client-auth verify failed: %v", err)
	}

	// cfg.TLS* must remain untouched — the in-memory path bypasses the
	// file-based nodepool entirely (see api.buildNodePool).
	if cfg.MutualTLS() {
		t.Error("cfg.MutualTLS() should stay false when the auto-issue is in-memory only")
	}
}

// TestEnsurePanelClient_OperatorOverride — when KRAKEN_TLS_CERT/KEY/CA are
// set, ensurePanelClient must not generate anything. api.buildNodePool
// picks up the file-based path unmodified.
func TestEnsurePanelClient_OperatorOverride(t *testing.T) {
	caCert, caKey, _ := mtls.GenerateCA()
	cfg := &config.Config{
		TLSCert: "/operator/panel.pem",
		TLSKey:  "/operator/panel-key.pem",
		TLSCA:   "/operator/ca.pem",
	}
	certPEM, keyPEM, caPEM, err := ensurePanelClient(cfg, caCert, caKey, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	if certPEM != nil || keyPEM != nil || caPEM != nil {
		t.Errorf("operator override should return nil bundle, got %d/%d/%d bytes",
			len(certPEM), len(keyPEM), len(caPEM))
	}
	if cfg.TLSCert != "/operator/panel.pem" {
		t.Errorf("operator TLSCert was overwritten: %q", cfg.TLSCert)
	}
}

// TestEnsurePanelClient_NoCA — no CA available (e.g. Panel couldn't load /
// generate one). Function must not error and must return an empty bundle,
// leaving the Panel to fall back to the documented insecure state.
func TestEnsurePanelClient_NoCA(t *testing.T) {
	cfg := &config.Config{}
	certPEM, keyPEM, caPEM, err := ensurePanelClient(cfg, nil, nil, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	if certPEM != nil || keyPEM != nil || caPEM != nil {
		t.Fatal("expected nil bundle when no CA is available")
	}
}
