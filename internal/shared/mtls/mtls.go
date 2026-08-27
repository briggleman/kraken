// Package mtls builds mutual-TLS configs for Panel↔Agent gRPC. The CA is the
// trust anchor: the Agent (server) requires a Panel client cert signed by the CA,
// and the Panel (client) verifies the Agent's server cert against the CA using a
// fixed logical ServerName, so trust is decoupled from each node's network
// address.
package mtls

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
)

// Logical certificate identities (SANs) baked into issued certs. The Panel pins
// AgentServerName when dialing, regardless of the node's host:port.
const (
	AgentServerName = "kraken-agent"
	PanelServerName = "kraken-panel"
	CAName          = "kraken-ca"
)

func caPool(caFile string) (*x509.CertPool, error) {
	pem, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("mtls: read CA %q: %w", caFile, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("mtls: no certificates found in %q", caFile)
	}
	return pool, nil
}

// RequirePeerCN returns a tls.Config.VerifyConnection hook that, on top of the
// CA-chain verification RequireAndVerifyClientCert already did, insists the
// verified client leaf's Subject CommonName equals wantCN.
//
// This is the load-bearing authorization for the Agent's gRPC listener. The CA
// is a *shared* trust anchor: it signs the Panel's client cert AND every Agent's
// server cert, and Agent certs carry ClientAuth EKU (they need it to dial the
// reverse tunnel). Chain-validation alone therefore accepts any Agent's own cert
// as a client — so a single stolen/enrolled Agent cert could drive the full
// NodeService (UpdateAgent → arbitrary binary → RCE) on every other node. The CN
// is authoritative because the signer sets it (SignAgentCSR* hardcodes it and
// never copies the CSR subject), so an enrollee cannot forge PanelServerName.
func RequirePeerCN(wantCN string) func(tls.ConnectionState) error {
	return func(cs tls.ConnectionState) error {
		if len(cs.VerifiedChains) == 0 || len(cs.VerifiedChains[0]) == 0 {
			return fmt.Errorf("mtls: no verified client certificate")
		}
		got := cs.VerifiedChains[0][0].Subject.CommonName
		if got != wantCN {
			return fmt.Errorf("mtls: client identity %q is not authorized (want %q)", got, wantCN)
		}
		return nil
	}
}

// ServerTLS builds the Agent's direct-listener server config: it presents
// certFile/keyFile, requires a client cert signed by caFile, AND authorizes the
// client by identity — only the Panel (CN=PanelServerName) may connect. Without
// that last check any peer Agent's cert would authenticate (see RequirePeerCN).
func ServerTLS(certFile, keyFile, caFile string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("mtls: load server keypair: %w", err)
	}
	pool, err := caPool(caFile)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates:     []tls.Certificate{cert},
		ClientAuth:       tls.RequireAndVerifyClientCert,
		ClientCAs:        pool,
		MinVersion:       tls.VersionTLS12,
		VerifyConnection: RequirePeerCN(PanelServerName),
	}, nil
}

// ServerTLSFromBytes is the byte-slice counterpart to ServerTLS — used for the
// Panel's reverse-tunnel listener, whose server cert is auto-issued in memory
// at startup (same rationale as ClientTLSFromBytes).
func ServerTLSFromBytes(certPEM, keyPEM, caPEM []byte) (*tls.Config, error) {
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("mtls: parse server keypair PEM: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("mtls: no CA certificates found in provided PEM")
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
		MinVersion:   tls.VersionTLS12,
	}, nil
}

// ClientTLS builds the Panel's client-side config: it presents certFile/keyFile
// and verifies the server's cert against caFile using serverName (typically
// AgentServerName).
func ClientTLS(certFile, keyFile, caFile, serverName string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("mtls: load client keypair: %w", err)
	}
	pool, err := caPool(caFile)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
		ServerName:   serverName,
		MinVersion:   tls.VersionTLS12,
	}, nil
}

// ClientTLSFromBytes is the byte-slice counterpart to ClientTLS. Used when the
// Panel auto-issues its client cert against its own CA at startup and keeps
// the bundle in memory rather than round-tripping through a filesystem the
// distroless-nonroot process may not have write access to.
func ClientTLSFromBytes(certPEM, keyPEM, caPEM []byte, serverName string) (*tls.Config, error) {
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("mtls: parse client keypair PEM: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("mtls: no CA certificates found in provided PEM")
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
		ServerName:   serverName,
		MinVersion:   tls.VersionTLS12,
	}, nil
}
