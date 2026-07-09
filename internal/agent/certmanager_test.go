package agent

import (
	"crypto/tls"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/briggleman/kraken/internal/shared/mtls"
)

// writeBundle enrolls a fake agent against a fresh CA and writes the bundle
// to dir, returning the three paths plus the CA material for re-signing.
func writeBundle(t *testing.T, dir string, hosts []string) (certFile, keyFile, caFile string, caCert, caKey []byte) {
	t.Helper()
	caCert, caKey, err := mtls.GenerateCA()
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	keyPEM, csrPEM, err := mtls.NewAgentKeyAndCSR(hosts)
	if err != nil {
		t.Fatalf("NewAgentKeyAndCSR: %v", err)
	}
	certPEM, err := mtls.SignAgentCSR(caCert, caKey, csrPEM, 90*24*time.Hour)
	if err != nil {
		t.Fatalf("SignAgentCSR: %v", err)
	}
	certFile = filepath.Join(dir, "agent.pem")
	keyFile = filepath.Join(dir, "agent-key.pem")
	caFile = filepath.Join(dir, "ca.pem")
	for path, data := range map[string][]byte{certFile: certPEM, keyFile: keyPEM, caFile: caCert} {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	return certFile, keyFile, caFile, caCert, caKey
}

func TestCertRotationRoundTrip(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile, caFile, caCert, caKey := writeBundle(t, dir, []string{"192.168.0.75", "node.example"})

	cm, err := NewCertManager(certFile, keyFile, caFile, nil)
	if err != nil {
		t.Fatalf("NewCertManager: %v", err)
	}
	oldSerial := cm.current.Leaf.SerialNumber.String()
	oldNotAfter := cm.NotAfter()

	csr, err := cm.BeginRotation()
	if err != nil {
		t.Fatalf("BeginRotation: %v", err)
	}
	newCert, err := mtls.SignAgentCSR(caCert, caKey, csr, 90*24*time.Hour)
	if err != nil {
		t.Fatalf("sign rotation CSR: %v", err)
	}
	notAfter, err := cm.CompleteRotation(newCert)
	if err != nil {
		t.Fatalf("CompleteRotation: %v", err)
	}
	if !notAfter.After(oldNotAfter.Add(-time.Hour)) {
		t.Fatalf("new notAfter %v not after old %v", notAfter, oldNotAfter)
	}

	// The served cert hot-swapped to the new serial …
	served, err := cm.GetCertificate(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatalf("GetCertificate: %v", err)
	}
	if served.Leaf.SerialNumber.String() == oldSerial {
		t.Fatal("serving cert did not rotate (same serial)")
	}
	// … keeps the operator SANs …
	sans := served.Leaf.DNSNames
	found := false
	for _, d := range sans {
		if d == "node.example" {
			found = true
		}
	}
	if !found {
		t.Fatalf("rotated cert lost operator SANs: %v", sans)
	}
	// … and the persisted bundle matches (a restart loads the new pair).
	reloaded, err := NewCertManager(certFile, keyFile, caFile, nil)
	if err != nil {
		t.Fatalf("reload after rotation: %v", err)
	}
	if reloaded.current.Leaf.SerialNumber.String() != served.Leaf.SerialNumber.String() {
		t.Fatal("persisted bundle does not match the served cert")
	}
	if err := mtls.VerifyPEM(mustRead(t, certFile), mustRead(t, caFile)); err != nil {
		t.Fatalf("persisted cert does not verify against CA: %v", err)
	}
}

func TestCompleteRotationRejectsWrongKeyAndForeignCA(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile, caFile, caCert, caKey := writeBundle(t, dir, nil)
	cm, err := NewCertManager(certFile, keyFile, caFile, nil)
	if err != nil {
		t.Fatalf("NewCertManager: %v", err)
	}

	// No rotation in progress → error.
	if _, err := cm.CompleteRotation([]byte("nope")); err == nil {
		t.Fatal("CompleteRotation without Begin should fail")
	}

	// Cert signed for a DIFFERENT key → rejected.
	if _, err := cm.BeginRotation(); err != nil {
		t.Fatalf("BeginRotation: %v", err)
	}
	_, otherCSR, err := mtls.NewAgentKeyAndCSR(nil)
	if err != nil {
		t.Fatalf("other CSR: %v", err)
	}
	otherCert, err := mtls.SignAgentCSR(caCert, caKey, otherCSR, time.Hour)
	if err != nil {
		t.Fatalf("sign other CSR: %v", err)
	}
	if _, err := cm.CompleteRotation(otherCert); err == nil {
		t.Fatal("cert for a different key should be rejected")
	}

	// Cert signed by a FOREIGN CA → rejected.
	csr, err := cm.BeginRotation()
	if err != nil {
		t.Fatalf("BeginRotation: %v", err)
	}
	foreignCA, foreignKey, err := mtls.GenerateCA()
	if err != nil {
		t.Fatalf("foreign CA: %v", err)
	}
	foreignCert, err := mtls.SignAgentCSR(foreignCA, foreignKey, csr, time.Hour)
	if err != nil {
		t.Fatalf("sign with foreign CA: %v", err)
	}
	if _, err := cm.CompleteRotation(foreignCert); err == nil {
		t.Fatal("cert from a foreign CA should be rejected")
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return b
}
