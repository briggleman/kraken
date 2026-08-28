package tunnel

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/yamux"

	"github.com/briggleman/kraken/internal/shared/mtls"
	sharedtunnel "github.com/briggleman/kraken/internal/shared/tunnel"
)

// testPKI holds everything one test tunnel needs: a CA, the Panel's server
// TLS config, and a client-cert factory.
type testPKI struct {
	caCert, caKey []byte
	serverTLS     *tls.Config
}

func newTestPKI(t *testing.T) *testPKI {
	t.Helper()
	caCert, caKey, err := mtls.GenerateCA()
	if err != nil {
		t.Fatal(err)
	}
	certPEM, keyPEM, err := mtls.IssuePanelServerCert(caCert, caKey, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	serverTLS, err := mtls.ServerTLSFromBytes(certPEM, keyPEM, caCert)
	if err != nil {
		t.Fatal(err)
	}
	return &testPKI{caCert: caCert, caKey: caKey, serverTLS: serverTLS}
}

// agentTLS builds a client TLS config from a cert enrolled with the given
// identity ("" = legacy cert without one).
func (p *testPKI) agentTLS(t *testing.T, identity string) *tls.Config {
	t.Helper()
	keyPEM, csr, err := mtls.NewAgentKeyAndCSR(nil)
	if err != nil {
		t.Fatal(err)
	}
	certPEM, err := mtls.SignAgentCSRWithIdentity(p.caCert, p.caKey, csr, time.Hour, identity)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(p.caCert) {
		t.Fatal("bad CA PEM")
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
		ServerName:   mtls.PanelServerName,
		MinVersion:   tls.VersionTLS12,
	}
}

// startServer runs a tunnel server on an ephemeral port and returns it plus
// its dial address. resolve maps identities to node ids.
func startServer(t *testing.T, pki *testPKI, resolve NodeResolver, opts ...Option) (*Server, string) {
	t.Helper()
	srv := New(pki.serverTLS, resolve, slog.New(slog.NewTextHandler(io.Discard, nil)), opts...)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, "127.0.0.1:0") }()
	t.Cleanup(func() {
		_ = srv.Close()
		<-done
	})
	// Wait for the listener to bind.
	deadline := time.Now().Add(3 * time.Second)
	for srv.Addr() == nil {
		if time.Now().After(deadline) {
			t.Fatal("listener never bound")
		}
		time.Sleep(5 * time.Millisecond)
	}
	return srv, srv.Addr().String()
}

// dialAgent plays the agent side: TLS dial + yamux server session that echoes
// one line per accepted stream.
func dialAgent(t *testing.T, addr string, tlsCfg *tls.Config) (*yamux.Session, error) {
	t.Helper()
	conn, err := tls.Dial("tcp", addr, tlsCfg)
	if err != nil {
		return nil, err
	}
	sess, err := yamux.Server(conn, sharedtunnel.Config())
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	go func() {
		for {
			stream, err := sess.Accept()
			if err != nil {
				return
			}
			go func(s net.Conn) {
				defer func() { _ = s.Close() }()
				line, err := bufio.NewReader(s).ReadString('\n')
				if err != nil {
					return
				}
				_, _ = fmt.Fprintf(s, "echo:%s", line)
			}(stream)
		}
	}()
	return sess, nil
}

func staticResolver(bindings map[string]string) NodeResolver {
	return func(_ context.Context, identity string) (string, error) {
		if id, ok := bindings[identity]; ok {
			return id, nil
		}
		return "", errors.New("unbound")
	}
}

// waitConnected polls until the node has a live session (or fails the test).
func waitConnected(t *testing.T, srv *Server, nodeID string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !srv.Connected(nodeID) {
		if time.Now().After(deadline) {
			t.Fatalf("node %s never connected", nodeID)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestTunnelRoundTrip — an agent with a bound identity connects, and a stream
// opened via DialContext carries bytes both ways.
func TestTunnelRoundTrip(t *testing.T) {
	pki := newTestPKI(t)
	srv, addr := startServer(t, pki, staticResolver(map[string]string{"ident-1": "node-1"}))

	sess, err := dialAgent(t, addr, pki.agentTLS(t, "ident-1"))
	if err != nil {
		t.Fatalf("agent dial: %v", err)
	}
	defer func() { _ = sess.Close() }()
	waitConnected(t, srv, "node-1")

	stream, err := srv.DialContext(context.Background(), "node-1")
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	defer func() { _ = stream.Close() }()
	if _, err := io.WriteString(stream, "ping\n"); err != nil {
		t.Fatal(err)
	}
	reply, err := bufio.NewReader(stream).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if reply != "echo:ping\n" {
		t.Fatalf("reply: got %q", reply)
	}
}

// TestTunnelRejectsUnboundIdentity — a valid CA-signed cert whose identity no
// node claims is disconnected, and no session registers.
func TestTunnelRejectsUnboundIdentity(t *testing.T) {
	pki := newTestPKI(t)
	srv, addr := startServer(t, pki, staticResolver(nil))

	sess, err := dialAgent(t, addr, pki.agentTLS(t, "nobody-claims-this"))
	if err != nil {
		// TLS itself may complete before the server closes; either failure
		// point is acceptable as long as no session registers.
		t.Logf("dial failed early (fine): %v", err)
	} else {
		defer func() { _ = sess.Close() }()
		select {
		case <-sess.CloseChan():
		case <-time.After(3 * time.Second):
			t.Fatal("server kept an unbound session open")
		}
	}
	if srv.Connected("nobody-claims-this") {
		t.Fatal("unbound identity registered a session")
	}
}

// TestTunnelRejectsLegacyCert — a cert without an identity URI SAN cannot open
// a tunnel at all.
func TestTunnelRejectsLegacyCert(t *testing.T) {
	pki := newTestPKI(t)
	_, addr := startServer(t, pki, staticResolver(map[string]string{"": "node-x"}))

	sess, err := dialAgent(t, addr, pki.agentTLS(t, ""))
	if err != nil {
		return // rejected during handshake teardown — fine
	}
	defer func() { _ = sess.Close() }()
	select {
	case <-sess.CloseChan():
	case <-time.After(3 * time.Second):
		t.Fatal("server accepted a cert with no identity")
	}
}

// TestTunnelRejectsUntrustedCert — a client cert from a different CA fails the
// TLS handshake outright.
func TestTunnelRejectsUntrustedCert(t *testing.T) {
	pki := newTestPKI(t)
	rogue := newTestPKI(t) // its own CA
	srv, addr := startServer(t, pki, staticResolver(map[string]string{"ident-1": "node-1"}))

	rogueTLS := rogue.agentTLS(t, "ident-1")
	rogueTLS.RootCAs = nil
	rogueTLS.InsecureSkipVerify = true // the rogue doesn't trust our CA either
	sess, err := dialAgent(t, addr, rogueTLS)
	if err == nil {
		defer func() { _ = sess.Close() }()
		select {
		case <-sess.CloseChan():
		case <-time.After(3 * time.Second):
			t.Fatal("server accepted a cert from an untrusted CA")
		}
	}
	if srv.Connected("node-1") {
		t.Fatal("untrusted cert registered a session")
	}
}

// TestTunnelReplacesSession — a reconnect with the same identity supersedes
// the old session, and streams route to the new one.
func TestTunnelReplacesSession(t *testing.T) {
	pki := newTestPKI(t)
	agentTLS := pki.agentTLS(t, "ident-1")
	srv, addr := startServer(t, pki, staticResolver(map[string]string{"ident-1": "node-1"}))

	old, err := dialAgent(t, addr, agentTLS)
	if err != nil {
		t.Fatal(err)
	}
	waitConnected(t, srv, "node-1")

	replacement, err := dialAgent(t, addr, agentTLS)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = replacement.Close() }()

	// The old session gets closed by the server in favor of the new one.
	select {
	case <-old.CloseChan():
	case <-time.After(3 * time.Second):
		t.Fatal("old session was not replaced")
	}
	// And the node stays connected throughout — never a gap.
	if !srv.Connected("node-1") {
		t.Fatal("node lost its session during replacement")
	}
	stream, err := srv.DialContext(context.Background(), "node-1")
	if err != nil {
		t.Fatalf("DialContext after replacement: %v", err)
	}
	_ = stream.Close()
}

// sessionRecorder captures session-hook callbacks for assertion.
//
// The hook fires just AFTER the server updates its session registry, and
// deliberately not under the same lock — the server will not hold its mutex
// across a caller-supplied callback. So Connected() flipping is not proof the
// matching event has been recorded yet, and a test that waits on Connected()
// can reach its assertion first. Wait on the events themselves.
type sessionRecorder struct {
	mu     sync.Mutex
	events []bool
}

func (r *sessionRecorder) hook(_ string, _ string, connected bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, connected)
}

func (r *sessionRecorder) snapshot() []bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]bool(nil), r.events...)
}

// waitFor polls the recorded events until cond is satisfied, returning them.
func (r *sessionRecorder) waitFor(t *testing.T, want string, cond func([]bool) bool) []bool {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		got := r.snapshot()
		if cond(got) {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("session hook events: got %v, waiting for %s", got, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestTunnelEvictsOnClose — when the agent goes away, DialContext returns
// ErrNoSession and the session hook reports the disconnect.
func TestTunnelEvictsOnClose(t *testing.T) {
	pki := newTestPKI(t)
	var rec sessionRecorder
	srv, addr := startServer(t, pki, staticResolver(map[string]string{"ident-1": "node-1"}), WithSessionHook(rec.hook))

	sess, err := dialAgent(t, addr, pki.agentTLS(t, "ident-1"))
	if err != nil {
		t.Fatal(err)
	}
	waitConnected(t, srv, "node-1")
	_ = sess.Close()

	// The server evicts the session before firing the disconnect hook, so
	// waiting for the event also establishes that eviction has happened —
	// which is what the DialContext assertion below depends on.
	events := rec.waitFor(t, "connect then disconnect", func(e []bool) bool {
		return len(e) >= 2 && !e[len(e)-1]
	})
	if !events[0] {
		t.Fatalf("session hook events: got %v, want the connect first", events)
	}
	if srv.Connected("node-1") {
		t.Fatal("session still registered after the disconnect hook fired")
	}
	if _, err := srv.DialContext(context.Background(), "node-1"); !errors.Is(err, ErrNoSession) {
		t.Fatalf("DialContext after eviction: got %v, want ErrNoSession", err)
	}
}
