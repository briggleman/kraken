package mtls

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"testing"
	"time"
)

func parseCertPEM(t *testing.T, certPEM []byte) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("no PEM block")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	return cert
}

// TestAgentIdentityRoundTrip — an identity signed into a cert's URI SAN comes
// back out of AgentIdentityFromCert verbatim.
func TestAgentIdentityRoundTrip(t *testing.T) {
	caCert, caKey, err := GenerateCA()
	if err != nil {
		t.Fatal(err)
	}
	_, csr, err := NewAgentKeyAndCSR([]string{"192.0.2.10"})
	if err != nil {
		t.Fatal(err)
	}
	certPEM, err := SignAgentCSRWithIdentity(caCert, caKey, csr, time.Hour, "node-identity-42")
	if err != nil {
		t.Fatal(err)
	}
	cert := parseCertPEM(t, certPEM)
	if got := AgentIdentityFromCert(cert); got != "node-identity-42" {
		t.Fatalf("identity: got %q, want %q", got, "node-identity-42")
	}
	// The identity rides alongside the normal SANs, not instead of them.
	if !containsString(cert.DNSNames, AgentServerName) {
		t.Fatalf("cert lost the %s SAN: %v", AgentServerName, cert.DNSNames)
	}
}

// TestAgentIdentityLegacyCert — a cert issued without an identity (the
// pre-tunnel path) reads back as empty, never as a bogus value.
func TestAgentIdentityLegacyCert(t *testing.T) {
	caCert, caKey, err := GenerateCA()
	if err != nil {
		t.Fatal(err)
	}
	_, csr, err := NewAgentKeyAndCSR(nil)
	if err != nil {
		t.Fatal(err)
	}
	certPEM, err := SignAgentCSR(caCert, caKey, csr, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if got := AgentIdentityFromCert(parseCertPEM(t, certPEM)); got != "" {
		t.Fatalf("legacy cert identity: got %q, want empty", got)
	}
}

// TestIssuePanelServerCert — the tunnel listener's cert chains to the CA,
// carries the PanelServerName SAN agents pin, and loads as a TLS keypair.
func TestIssuePanelServerCert(t *testing.T) {
	caCert, caKey, err := GenerateCA()
	if err != nil {
		t.Fatal(err)
	}
	certPEM, keyPEM, err := IssuePanelServerCert(caCert, caKey, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tls.X509KeyPair(certPEM, keyPEM); err != nil {
		t.Fatalf("keypair: %v", err)
	}
	if err := VerifyPEM(certPEM, caCert); err != nil {
		t.Fatalf("chain: %v", err)
	}
	cert := parseCertPEM(t, certPEM)
	if !containsString(cert.DNSNames, PanelServerName) {
		t.Fatalf("missing %s SAN: %v", PanelServerName, cert.DNSNames)
	}
	hasServerAuth := false
	for _, eku := range cert.ExtKeyUsage {
		if eku == x509.ExtKeyUsageServerAuth {
			hasServerAuth = true
		}
	}
	if !hasServerAuth {
		t.Fatal("missing ServerAuth EKU")
	}
}
