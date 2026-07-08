package main

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/briggleman/kraken/internal/panel/config"
	"github.com/briggleman/kraken/internal/shared/mtls"
)

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// TestEnsurePanelClient_AutoIssue — with no operator-provided TLS envs, the
// Panel signs itself a client cert against its own CA and points cfg.TLS*
// at the resulting files, so defaultNodePool picks up mTLS on next dial.
func TestEnsurePanelClient_AutoIssue(t *testing.T) {
	caCert, caKey, err := mtls.GenerateCA()
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	dir := t.TempDir()
	cfg := &config.Config{StateDir: dir}

	if err := ensurePanelClient(cfg, caCert, caKey, testLogger()); err != nil {
		t.Fatalf("ensurePanelClient: %v", err)
	}
	if !cfg.MutualTLS() {
		t.Fatalf("MutualTLS should be true after auto-issue, cfg=%+v", cfg)
	}
	for _, p := range []string{cfg.TLSCert, cfg.TLSKey, cfg.TLSCA} {
		st, err := os.Stat(p)
		if err != nil || st.Size() == 0 {
			t.Fatalf("expected non-empty file at %s: %v", p, err)
		}
	}
}

// TestEnsurePanelClient_Idempotent — a re-run with the same state dir
// reuses the existing bundle rather than re-signing. Guards against
// rotating the Panel cert on every restart (which would invalidate any
// pinned trust downstream).
func TestEnsurePanelClient_Idempotent(t *testing.T) {
	caCert, caKey, _ := mtls.GenerateCA()
	dir := t.TempDir()
	cfg := &config.Config{StateDir: dir}

	if err := ensurePanelClient(cfg, caCert, caKey, testLogger()); err != nil {
		t.Fatal(err)
	}
	firstCert, _ := os.ReadFile(cfg.TLSCert)

	// Blow away cfg, run again — files still on disk, should be reused byte-
	// for-byte.
	cfg2 := &config.Config{StateDir: dir}
	if err := ensurePanelClient(cfg2, caCert, caKey, testLogger()); err != nil {
		t.Fatal(err)
	}
	secondCert, _ := os.ReadFile(cfg2.TLSCert)
	if string(firstCert) != string(secondCert) {
		t.Fatal("cert was re-signed on second call; should have been reused")
	}
}

// TestEnsurePanelClient_OperatorOverride — when the operator has set the
// TLS envs explicitly (KRAKEN_TLS_CERT/KEY/CA all present), skip the
// auto-issue entirely so we don't clobber their PKI.
func TestEnsurePanelClient_OperatorOverride(t *testing.T) {
	caCert, caKey, _ := mtls.GenerateCA()
	dir := t.TempDir()
	cfg := &config.Config{
		StateDir: dir,
		TLSCert:  "/operator/panel.pem",
		TLSKey:   "/operator/panel-key.pem",
		TLSCA:    "/operator/ca.pem",
	}
	if err := ensurePanelClient(cfg, caCert, caKey, testLogger()); err != nil {
		t.Fatal(err)
	}
	if cfg.TLSCert != "/operator/panel.pem" {
		t.Errorf("operator TLSCert was overwritten: %q", cfg.TLSCert)
	}
	// No files should have been created in state dir.
	if _, err := os.Stat(filepath.Join(dir, "panel-client.pem")); err == nil {
		t.Error("panel-client.pem was created despite operator override")
	}
}

// TestEnsurePanelClient_NoCA — no CA available (e.g. Panel couldn't load /
// generate one). Function must not error, must not populate cfg.TLS*, and
// must leave the Panel in its documented "insecure fallback" state.
func TestEnsurePanelClient_NoCA(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{StateDir: dir}
	if err := ensurePanelClient(cfg, nil, nil, testLogger()); err != nil {
		t.Fatal(err)
	}
	if cfg.MutualTLS() {
		t.Fatal("expected MutualTLS() to remain false when no CA is available")
	}
}
