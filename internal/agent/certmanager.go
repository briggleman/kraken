package agent

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/briggleman/kraken/internal/shared/mtls"
)

// pendingRotationTTL bounds how long a BeginCertRotation key waits for its
// signed certificate before the slot is considered stale.
const pendingRotationTTL = 10 * time.Minute

// CertManager owns the Agent's mTLS serving certificate. It hands the live
// cert to the TLS stack via GetCertificate (so a rotation needs no restart),
// reports its expiry for NodeInfo, and executes the Panel-driven two-step
// rotation: BeginRotation mints a fresh key + CSR, CompleteRotation installs
// the CA-signed certificate after verifying it.
type CertManager struct {
	mu       sync.Mutex
	certFile string
	keyFile  string
	caFile   string
	current  *tls.Certificate // Leaf is always parsed

	pendingKeyPEM []byte
	pendingAt     time.Time

	logger *slog.Logger
}

// NewCertManager loads the bundle at certFile/keyFile and parses the leaf.
func NewCertManager(certFile, keyFile, caFile string, logger *slog.Logger) (*CertManager, error) {
	cert, err := loadKeyPair(certFile, keyFile)
	if err != nil {
		return nil, err
	}
	return &CertManager{
		certFile: certFile,
		keyFile:  keyFile,
		caFile:   caFile,
		current:  cert,
		logger:   logger,
	}, nil
}

func loadKeyPair(certFile, keyFile string) (*tls.Certificate, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("certmanager: load keypair: %w", err)
	}
	if cert.Leaf == nil {
		leaf, perr := x509.ParseCertificate(cert.Certificate[0])
		if perr != nil {
			return nil, fmt.Errorf("certmanager: parse leaf: %w", perr)
		}
		cert.Leaf = leaf
	}
	return &cert, nil
}

// GetCertificate serves the live certificate to the TLS handshake.
func (m *CertManager) GetCertificate(_ *tls.ClientHelloInfo) (*tls.Certificate, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.current, nil
}

// NotAfter reports the live certificate's expiry.
func (m *CertManager) NotAfter() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.current.Leaf.NotAfter
}

// BeginRotation generates a fresh key + CSR carrying the same extra SANs as
// the live cert. The key is held in a single pending slot until
// CompleteRotation installs its signed certificate.
func (m *CertManager) BeginRotation() (csrPEM []byte, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	keyPEM, csrPEM, err := mtls.NewAgentKeyAndCSR(m.extraHostsLocked())
	if err != nil {
		return nil, fmt.Errorf("certmanager: generate rotation key/CSR: %w", err)
	}
	m.pendingKeyPEM = keyPEM
	m.pendingAt = time.Now()
	return csrPEM, nil
}

// extraHostsLocked returns the live cert's operator-added SANs (the baked-in
// logical/loopback names are re-added by NewAgentKeyAndCSR itself).
func (m *CertManager) extraHostsLocked() []string {
	skipDNS := map[string]bool{mtls.AgentServerName: true, "localhost": true}
	var hosts []string
	for _, d := range m.current.Leaf.DNSNames {
		if !skipDNS[d] {
			hosts = append(hosts, d)
		}
	}
	for _, ip := range m.current.Leaf.IPAddresses {
		if !ip.IsLoopback() {
			hosts = append(hosts, ip.String())
		}
	}
	return hosts
}

// CompleteRotation verifies and installs the signed certificate for the
// pending key: it must match the pending key's public key and chain to the
// CA the Agent already trusts. On success the new bundle is persisted and the
// serving cert hot-swapped; the old cert keeps serving existing connections.
func (m *CertManager) CompleteRotation(certPEM []byte) (time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pendingKeyPEM == nil {
		return time.Time{}, fmt.Errorf("certmanager: no rotation in progress (call BeginCertRotation first)")
	}
	if time.Since(m.pendingAt) > pendingRotationTTL {
		m.pendingKeyPEM = nil
		return time.Time{}, fmt.Errorf("certmanager: pending rotation expired; begin again")
	}
	caPEM, err := os.ReadFile(m.caFile)
	if err != nil {
		return time.Time{}, fmt.Errorf("certmanager: read trusted CA: %w", err)
	}
	if err := mtls.VerifyPEM(certPEM, caPEM); err != nil {
		return time.Time{}, fmt.Errorf("certmanager: new cert does not chain to the trusted CA: %w", err)
	}
	pair, err := tls.X509KeyPair(certPEM, m.pendingKeyPEM)
	if err != nil {
		return time.Time{}, fmt.Errorf("certmanager: cert does not match the pending key: %w", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return time.Time{}, fmt.Errorf("certmanager: parse new leaf: %w", err)
	}
	pair.Leaf = leaf

	// Persist key first, then cert. A crash between the two writes leaves a
	// mismatched bundle on disk — the startup bundle check reports it loudly —
	// but the in-memory cert keeps serving until then.
	if err := replaceFile(m.keyFile, m.pendingKeyPEM); err != nil {
		return time.Time{}, fmt.Errorf("certmanager: persist key: %w", err)
	}
	if err := replaceFile(m.certFile, certPEM); err != nil {
		return time.Time{}, fmt.Errorf("certmanager: persist cert: %w", err)
	}

	m.current = &pair
	m.pendingKeyPEM = nil
	if m.logger != nil {
		m.logger.Info("mTLS: serving certificate rotated", "cert", mtls.SummarizeCert(leaf))
	}
	return leaf.NotAfter, nil
}

// replaceFile writes data to path via a temp file + rename. Windows cannot
// rename over an existing file, so the old file is removed first — the temp
// file is already durable on disk at that point.
func replaceFile(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}
