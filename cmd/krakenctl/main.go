// Command krakenctl is the Kraken admin CLI. Today it generates the mutual-TLS
// material for Panel↔Agent: a CA plus a Panel client cert and an Agent server
// cert, all written as PEM files — and enrolls an Agent against a Panel. It
// also carries two development tools for push alerts: a stand-in relay and a
// test-device key generator.
package main

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
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/briggleman/kraken/internal/shared/mtls"
	"github.com/briggleman/kraken/internal/shared/version"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "gen-certs":
		if err := genCerts(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "enroll":
		if err := enroll(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "push-relay-stub":
		if err := pushRelayStub(os.Args[2:], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "push-keygen":
		if err := pushKeygen(os.Args[2:], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "version", "-version", "--version":
		fmt.Println("krakenctl", version.String())
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `krakenctl — Kraken admin CLI

Usage:
  krakenctl enroll -panel URL -token TOKEN [-hosts h1,h2,...] [-port N] [-out DIR]
      Enroll this Agent: generate a key + CSR, exchange the one-time bootstrap
      token for a signed Agent cert, and write agent.pem/agent-key.pem/ca.pem.
      Re-run with a fresh token to rotate the cert.
      -panel        Panel base URL (e.g. http://panel:8080)
      -token        one-time bootstrap token (from the admin API)
      -hosts        IPs / DNS names the Panel can dial this agent at (SAN +
                    registration prefill). Use real addresses — bare computer
                    names don't resolve from a remote Panel.
      -port         the agent's gRPC port (KRAKEN_AGENT_ADDR; default 9090) —
                    lets the Panel prefill registration with the right port
      -out          output directory (default ./certs)

  krakenctl gen-certs [-out DIR] [-agent-hosts h1,h2,...]
      Generate a CA, a Panel client cert, and an Agent server cert (PEM).
      -out          output directory (default ./certs)
      -agent-hosts  extra DNS names / IPs to add to the Agent cert SAN
                    (localhost and 127.0.0.1 are always included)

  krakenctl push-relay-stub [-addr ADDR] [-key B64] [-answer CODE] [-times N]
                            [-relay-token TOKEN]
      DEVELOPMENT TOOL. A stand-in for the Kraken push relay: accepts the
      Panel's POST /v1/push, opens each envelope with a test device's private
      key and prints the alert as one line. Point the Panel at it with
      KRAKEN_PUSH_RELAY_URL=http://127.0.0.1:8787. Never run it in front of
      real devices.
      -addr         listen address (default 127.0.0.1:8787)
      -key          the test device's X25519 private key, base64; when empty a
                    key pair is generated and the public key printed, to
                    register as the device's public_key
      -answer       ok (default), 410, 400, 429 or 503: the status to answer
                    with, to exercise the Panel's token-dead, refused and
                    retry paths
      -times        answer -answer for only the next N requests, then 200
      -relay-token  require Authorization: Bearer TOKEN, as a relay that
                    checks KRAKEN_PUSH_RELAY_TOKEN would
      A request's ?answer=CODE query parameter overrides -answer for that
      request, so KRAKEN_PUSH_RELAY_URL=http://127.0.0.1:8787?answer=503 works.

  krakenctl push-keygen
      Print a fresh X25519 key pair (base64) for registering a test device.

  krakenctl version`)
}

func genCerts(args []string) error {
	out := "./certs"
	var agentHosts string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-out":
			i++
			if i < len(args) {
				out = args[i]
			}
		case "-agent-hosts":
			i++
			if i < len(args) {
				agentHosts = args[i]
			}
		default:
			return fmt.Errorf("unknown flag %q", args[i])
		}
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}

	// 1) CA
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: mtls.CAName},
		NotBefore:             notBefore(),
		NotAfter:              notAfter(),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		return err
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return err
	}
	if err := writeCert(filepath.Join(out, "ca.pem"), caDER); err != nil {
		return err
	}
	if err := writeKey(filepath.Join(out, "ca-key.pem"), caKey); err != nil {
		return err
	}

	// 2) Panel client cert
	if err := leaf(out, "panel", caCert, caKey, mtls.PanelServerName,
		[]x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, nil, nil); err != nil {
		return err
	}

	// 3) Agent server cert (+ extra SANs for convenience)
	dns := []string{mtls.AgentServerName, "localhost"}
	ips := []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}
	for _, h := range strings.Split(agentHosts, ",") {
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
	if err := leaf(out, "agent", caCert, caKey, mtls.AgentServerName,
		[]x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}, dns, ips); err != nil {
		return err
	}

	fmt.Printf("Wrote CA + panel + agent certs to %s\n", out)
	fmt.Println("Panel:  KRAKEN_TLS_CERT=panel.pem KRAKEN_TLS_KEY=panel-key.pem KRAKEN_TLS_CA=ca.pem")
	fmt.Println("Agent:  KRAKEN_TLS_CERT=agent.pem KRAKEN_TLS_KEY=agent-key.pem KRAKEN_TLS_CA=ca.pem")
	return nil
}

func leaf(out, name string, caCert *x509.Certificate, caKey *ecdsa.PrivateKey, cn string, eku []x509.ExtKeyUsage, dns []string, ips []net.IP) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    notBefore(),
		NotAfter:     notAfter(),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  eku,
		DNSNames:     dns,
		IPAddresses:  ips,
	}
	if dns == nil {
		tmpl.DNSNames = []string{cn}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
	if err != nil {
		return err
	}
	if err := writeCert(filepath.Join(out, name+".pem"), der); err != nil {
		return err
	}
	return writeKey(filepath.Join(out, name+"-key.pem"), key)
}

func serial() *big.Int {
	max := new(big.Int).Lsh(big.NewInt(1), 128)
	n, _ := rand.Int(rand.Reader, max)
	return n
}

func notBefore() time.Time { return time.Now().Add(-time.Hour) }
func notAfter() time.Time  { return time.Now().AddDate(5, 0, 0) }

func writeCert(path string, der []byte) error {
	return os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
}

func writeKey(path string, key *ecdsa.PrivateKey) error {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	return os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), 0o600)
}
