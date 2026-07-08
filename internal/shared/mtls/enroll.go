package mtls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"strings"
	"time"
)

// DefaultAgentCertTTL is how long an enrolled Agent certificate is valid.
const DefaultAgentCertTTL = 90 * 24 * time.Hour

// DefaultPanelClientCertTTL is how long an auto-issued Panel client cert is
// valid. Long-lived because the Panel binds its own lifecycle to it and
// rotates on restart when the file is deleted, not on any external schedule.
const DefaultPanelClientCertTTL = 5 * 365 * 24 * time.Hour

func newSerial() *big.Int {
	max := new(big.Int).Lsh(big.NewInt(1), 128)
	n, _ := rand.Int(rand.Reader, max)
	return n
}

// GenerateCA creates a self-signed CA keypair and returns the cert and key as
// PEM. The Panel holds these to sign Agent enrollment requests.
func GenerateCA() (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          newSerial(),
		Subject:               pkix.Name{CommonName: CAName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, nil
}

// NewAgentKeyAndCSR generates an ECDSA P-256 key and a certificate-signing
// request for an Agent server cert. The CSR always requests the logical
// AgentServerName plus loopback, and any extra hosts (DNS names or IPs) given.
func NewAgentKeyAndCSR(hosts []string) (keyPEM, csrPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	dns := []string{AgentServerName, "localhost"}
	ips := []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}
	for _, h := range hosts {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		if ip := net.ParseIP(h); ip != nil {
			ips = append(ips, ip)
		} else {
			dns = append(dns, h)
		}
	}
	tmpl := &x509.CertificateRequest{
		Subject:     pkix.Name{CommonName: AgentServerName},
		DNSNames:    dns,
		IPAddresses: ips,
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, tmpl, key)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	csrPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
	return keyPEM, csrPEM, nil
}

// SignAgentCSR verifies csrPEM and issues an Agent server certificate signed by
// the CA (caCertPEM/caKeyPEM), valid for ttl. The issued cert always carries the
// AgentServerName SAN (the Panel pins it when dialing) and server+client-auth
// EKUs. This is the core of the bootstrap/enrollment + rotation flow.
func SignAgentCSR(caCertPEM, caKeyPEM, csrPEM []byte, ttl time.Duration) ([]byte, error) {
	caCert, caKey, err := loadCAKeyPair(caCertPEM, caKeyPEM)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(csrPEM)
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return nil, fmt.Errorf("mtls: invalid CSR PEM")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("mtls: parse CSR: %w", err)
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, fmt.Errorf("mtls: CSR signature invalid: %w", err)
	}
	if ttl <= 0 {
		ttl = DefaultAgentCertTTL
	}
	dns := csr.DNSNames
	if !containsString(dns, AgentServerName) {
		dns = append([]string{AgentServerName}, dns...)
	}
	tmpl := &x509.Certificate{
		SerialNumber: newSerial(),
		Subject:      pkix.Name{CommonName: AgentServerName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(ttl),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		DNSNames:     dns,
		IPAddresses:  csr.IPAddresses,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, csr.PublicKey, caKey)
	if err != nil {
		return nil, fmt.Errorf("mtls: sign cert: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}

// IssuePanelClientCert generates a fresh ECDSA keypair for the Panel and
// returns a client certificate signed by the given CA. The cert carries the
// PanelServerName CN + ClientAuth EKU so an Agent (which trusts the same CA
// and requires client auth) accepts the Panel's outbound mTLS handshake.
//
// This is the symmetric counterpart to SignAgentCSR: an Agent cert is server-
// auth + client-auth (Agent listens and Panel connects), a Panel cert is
// client-auth only (Panel connects out).
func IssuePanelClientCert(caCertPEM, caKeyPEM []byte, ttl time.Duration) (certPEM, keyPEM []byte, err error) {
	caCert, caKey, err := loadCAKeyPair(caCertPEM, caKeyPEM)
	if err != nil {
		return nil, nil, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	if ttl <= 0 {
		ttl = DefaultPanelClientCertTTL
	}
	tmpl := &x509.Certificate{
		SerialNumber: newSerial(),
		Subject:      pkix.Name{CommonName: PanelServerName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(ttl),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		DNSNames:     []string{PanelServerName},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
	if err != nil {
		return nil, nil, fmt.Errorf("mtls: sign panel client cert: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, nil
}

func loadCAKeyPair(certPEM, keyPEM []byte) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	cb, _ := pem.Decode(certPEM)
	if cb == nil {
		return nil, nil, fmt.Errorf("mtls: invalid CA cert PEM")
	}
	caCert, err := x509.ParseCertificate(cb.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("mtls: parse CA cert: %w", err)
	}
	kb, _ := pem.Decode(keyPEM)
	if kb == nil {
		return nil, nil, fmt.Errorf("mtls: invalid CA key PEM")
	}
	caKey, err := x509.ParseECPrivateKey(kb.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("mtls: parse CA key: %w", err)
	}
	return caCert, caKey, nil
}

func containsString(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
