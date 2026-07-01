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

// ServerTLS builds the Agent's server-side config: it presents certFile/keyFile
// and requires every client to present a certificate signed by caFile.
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
