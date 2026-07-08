package mtls

import (
	"crypto/x509"
	"encoding/pem"
	"testing"
	"time"
)

// TestEnrollFlow exercises the full PKI path: generate a CA, create an Agent
// key+CSR, sign it, and verify the issued cert chains to the CA and is valid
// for the pinned AgentServerName with server-auth EKU.
func TestEnrollFlow(t *testing.T) {
	caCertPEM, caKeyPEM, err := GenerateCA()
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	_, csrPEM, err := NewAgentKeyAndCSR([]string{"node1.example.com", "10.0.0.5"})
	if err != nil {
		t.Fatalf("NewAgentKeyAndCSR: %v", err)
	}
	certPEM, err := SignAgentCSR(caCertPEM, caKeyPEM, csrPEM, time.Hour)
	if err != nil {
		t.Fatalf("SignAgentCSR: %v", err)
	}

	block, _ := pem.Decode(certPEM)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse issued cert: %v", err)
	}

	// Chains to the CA.
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caCertPEM) {
		t.Fatal("could not load CA into pool")
	}
	if _, err := cert.Verify(x509.VerifyOptions{
		Roots:     roots,
		DNSName:   AgentServerName,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		t.Fatalf("issued cert failed verification: %v", err)
	}

	// Requested SANs are present.
	for _, want := range []string{AgentServerName, "localhost", "node1.example.com"} {
		if !containsString(cert.DNSNames, want) {
			t.Errorf("issued cert missing DNS SAN %q (got %v)", want, cert.DNSNames)
		}
	}
}

func TestSignAgentCSRRejectsGarbage(t *testing.T) {
	caCertPEM, caKeyPEM, _ := GenerateCA()
	if _, err := SignAgentCSR(caCertPEM, caKeyPEM, []byte("not a csr"), time.Hour); err == nil {
		t.Fatal("expected error signing garbage CSR")
	}
}

// TestIssuePanelClientCert exercises the Panel-side counterpart to
// SignAgentCSR: the Panel signs itself a client cert against its own CA so
// outbound gRPC to Agents can complete mTLS. The Agent side verifies with
// the same CA + a ClientAuth EKU requirement.
func TestIssuePanelClientCert(t *testing.T) {
	caCertPEM, caKeyPEM, err := GenerateCA()
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	certPEM, keyPEM, err := IssuePanelClientCert(caCertPEM, caKeyPEM, time.Hour)
	if err != nil {
		t.Fatalf("IssuePanelClientCert: %v", err)
	}
	if len(keyPEM) == 0 {
		t.Fatal("empty key PEM")
	}

	block, _ := pem.Decode(certPEM)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse issued panel cert: %v", err)
	}
	if cert.Subject.CommonName != PanelServerName {
		t.Fatalf("cn: got %q, want %q", cert.Subject.CommonName, PanelServerName)
	}

	// Must carry the ClientAuth EKU — Agents (mtls.ServerTLS) reject anything
	// else at handshake time.
	var hasClientAuth bool
	for _, ku := range cert.ExtKeyUsage {
		if ku == x509.ExtKeyUsageClientAuth {
			hasClientAuth = true
		}
	}
	if !hasClientAuth {
		t.Errorf("panel cert missing ExtKeyUsageClientAuth (got %v)", cert.ExtKeyUsage)
	}

	// Verify the cert chains against the CA when used as a client cert.
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caCertPEM) {
		t.Fatal("could not load CA into pool")
	}
	if _, err := cert.Verify(x509.VerifyOptions{
		Roots:     roots,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		t.Fatalf("panel cert failed client-auth verify: %v", err)
	}
}

func TestIssuePanelClientCertRejectsBadCA(t *testing.T) {
	if _, _, err := IssuePanelClientCert([]byte("garbage"), []byte("garbage"), time.Hour); err == nil {
		t.Fatal("expected error with garbage CA material")
	}
}
