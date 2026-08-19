package mtls

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"strings"
	"time"
)

// parseFirstCert decodes the first CERTIFICATE block in pemBytes.
func parseFirstCert(pemBytes []byte) (*x509.Certificate, error) {
	for {
		var block *pem.Block
		block, pemBytes = pem.Decode(pemBytes)
		if block == nil {
			return nil, fmt.Errorf("mtls: no CERTIFICATE block in PEM")
		}
		if block.Type == "CERTIFICATE" {
			return x509.ParseCertificate(block.Bytes)
		}
	}
}

// FingerprintPEM returns a short SHA-256 fingerprint of the first certificate
// in pemBytes — enough hex to compare identities across Panel and Agent logs.
func FingerprintPEM(pemBytes []byte) string {
	cert, err := parseFirstCert(pemBytes)
	if err != nil {
		return "unparseable"
	}
	sum := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(sum[:8])
}

// FingerprintCert is FingerprintPEM for an already-parsed certificate.
func FingerprintCert(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(sum[:8])
}

// SummarizeCert renders the identity of a certificate on one log-friendly
// line: CN, issuer, serial, fingerprint, validity window, and SANs.
func SummarizeCert(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	sans := append([]string{}, cert.DNSNames...)
	for _, ip := range cert.IPAddresses {
		sans = append(sans, ip.String())
	}
	return fmt.Sprintf("cn=%s issuer=%s serial=%s sha256=%s notBefore=%s notAfter=%s sans=[%s]",
		cert.Subject.CommonName,
		cert.Issuer.CommonName,
		cert.SerialNumber.Text(16),
		hex.EncodeToString(sum[:8]),
		cert.NotBefore.UTC().Format(time.RFC3339),
		cert.NotAfter.UTC().Format(time.RFC3339),
		strings.Join(sans, " "))
}

// SummarizePEM is SummarizeCert for raw PEM input.
func SummarizePEM(pemBytes []byte) string {
	cert, err := parseFirstCert(pemBytes)
	if err != nil {
		return "unparseable: " + err.Error()
	}
	return SummarizeCert(cert)
}

// SANHosts returns the DNS + IP SANs of the first certificate in pemBytes,
// in cert order (DNS names first). Empty on parse failure.
func SANHosts(pemBytes []byte) []string {
	cert, err := parseFirstCert(pemBytes)
	if err != nil {
		return nil
	}
	hosts := append([]string{}, cert.DNSNames...)
	for _, ip := range cert.IPAddresses {
		hosts = append(hosts, ip.String())
	}
	return hosts
}

// VerifyPEM reports whether the first cert in certPEM chains to the CA(s) in
// caPEM and is valid right now. Used at startup to catch a stale bundle (cert
// enrolled under a previous CA, or expired) before the first handshake fails.
func VerifyPEM(certPEM, caPEM []byte) error {
	cert, err := parseFirstCert(certPEM)
	if err != nil {
		return err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return fmt.Errorf("mtls: no CA certificates in PEM")
	}
	_, err = cert.Verify(x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	})
	return err
}

// CAFingerprintPEM returns the full SHA-256 fingerprint (64 hex chars) of the
// first certificate in pemBytes. This is the pinning identity embedded in
// Panel-generated deploy commands and checked by the Agent's remote-enroll
// path, so unlike FingerprintPEM it must carry the whole digest.
func CAFingerprintPEM(pemBytes []byte) (string, error) {
	cert, err := parseFirstCert(pemBytes)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(sum[:]), nil
}

// VerifyCAFingerprint checks pemBytes against a pinned fingerprint as produced
// by CAFingerprintPEM. The comparison is case-insensitive and tolerates a
// "sha256:" prefix so operators can paste either spelling.
func VerifyCAFingerprint(pemBytes []byte, pinned string) error {
	want := strings.ToLower(strings.TrimSpace(pinned))
	want = strings.TrimPrefix(want, "sha256:")
	got, err := CAFingerprintPEM(pemBytes)
	if err != nil {
		return err
	}
	if want == "" {
		return fmt.Errorf("mtls: empty CA fingerprint")
	}
	if got != want {
		return fmt.Errorf("mtls: CA fingerprint mismatch: pinned %s, got %s — the Panel that answered is not the one that issued this token", want, got)
	}
	return nil
}
