// Package tunnel is the Panel side of the reverse-connection transport: an
// mTLS-only listener that Agents dial into, a registry of live sessions keyed
// by node id, and a dialer the nodeclient pool uses to route gRPC connections
// through those sessions. See docs/design/reverse-connections.md.
package tunnel

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"

	"github.com/hashicorp/yamux"

	"github.com/briggleman/kraken/internal/shared/mtls"
	sharedtunnel "github.com/briggleman/kraken/internal/shared/tunnel"
)

// ErrNoSession is returned by DialContext when the node has no live tunnel.
// The nodeclient pool surfaces it like any dial failure, so a tunnel-mode node
// without a connected agent reads offline exactly the way an unreachable
// direct-mode node does.
var ErrNoSession = errors.New("tunnel: no live session for node")

// NodeResolver maps a Panel-minted agent identity (the cert's URI SAN) to the
// node record that claims it. Implemented by the API server against the store;
// a func type keeps this package free of store imports.
type NodeResolver func(ctx context.Context, agentIdentity string) (nodeID string, err error)

// Server accepts reverse-tunnel connections from Agents and tracks one live
// yamux session per node.
type Server struct {
	tlsCfg  *tls.Config
	resolve NodeResolver
	logger  *slog.Logger

	// onSession, when set, is invoked after a session is registered (connect)
	// and after it is evicted (disconnect) — the API layer uses it to audit
	// and to nudge the node reconciler.
	onSession func(nodeID, fingerprint string, connected bool)

	mu       sync.Mutex
	lis      net.Listener
	sessions map[string]*session // node id → live session
	closed   bool
}

type session struct {
	nodeID      string
	fingerprint string
	remote      string
	mux         *yamux.Session
}

// Option customizes a Server.
type Option func(*Server)

// WithSessionHook registers a callback fired on session connect/disconnect.
func WithSessionHook(fn func(nodeID, fingerprint string, connected bool)) Option {
	return func(s *Server) { s.onSession = fn }
}

// New builds a tunnel server. tlsCfg must require and verify client certs
// against the enrollment CA (the caller builds it via mtls helpers); resolve
// binds cert identities to node records.
func New(tlsCfg *tls.Config, resolve NodeResolver, logger *slog.Logger, opts ...Option) *Server {
	s := &Server{
		tlsCfg:   tlsCfg,
		resolve:  resolve,
		logger:   logger,
		sessions: make(map[string]*session),
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Serve listens on addr and accepts agent tunnels until ctx is canceled or
// Close is called. It returns after the listener is torn down.
func (s *Server) Serve(ctx context.Context, addr string) error {
	lis, err := tls.Listen("tcp", addr, s.tlsCfg)
	if err != nil {
		return fmt.Errorf("tunnel: listen %s: %w", addr, err)
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = lis.Close()
		return nil
	}
	s.lis = lis
	s.mu.Unlock()
	s.logger.Info("reverse-tunnel listener up (agents dial in; mTLS required)", "addr", addr)

	go func() {
		<-ctx.Done()
		_ = s.Close()
	}()

	for {
		conn, err := lis.Accept()
		if err != nil {
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if closed || ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("tunnel: accept: %w", err)
		}
		go s.handle(ctx, conn)
	}
}

// handle runs the TLS handshake, resolves the agent's identity to a node, and
// registers the yamux session. The TLS layer is the entire handshake: identity
// comes from the verified client cert's URI SAN, never from application data.
func (s *Server) handle(ctx context.Context, conn net.Conn) {
	remote := conn.RemoteAddr().String()
	tconn, ok := conn.(*tls.Conn)
	if !ok { // tls.Listen guarantees this; belt and braces
		_ = conn.Close()
		return
	}
	if err := tconn.HandshakeContext(ctx); err != nil {
		s.logger.Warn("tunnel: TLS handshake failed", "remote", remote, "err", err)
		_ = conn.Close()
		return
	}
	state := tconn.ConnectionState()
	if len(state.PeerCertificates) == 0 { // RequireAndVerifyClientCert makes this unreachable
		_ = conn.Close()
		return
	}
	peer := state.PeerCertificates[0]
	identity := mtls.AgentIdentityFromCert(peer)
	fingerprint := mtls.FingerprintCert(peer)
	if identity == "" {
		s.logger.Warn("tunnel: rejected — client cert has no agent identity (re-enroll the agent to get a per-node cert)",
			"remote", remote, "peer", mtls.SummarizeCert(peer))
		_ = conn.Close()
		return
	}
	nodeID, err := s.resolve(ctx, identity)
	if err != nil {
		s.logger.Warn("tunnel: rejected — identity is not bound to a node (register the node with this tunnel id first)",
			"remote", remote, "identity", identity, "err", err)
		_ = conn.Close()
		return
	}

	mux, err := yamux.Server(tconn, sharedtunnel.Config())
	if err != nil {
		s.logger.Warn("tunnel: session setup failed", "remote", remote, "node", nodeID, "err", err)
		_ = conn.Close()
		return
	}
	sess := &session{nodeID: nodeID, fingerprint: fingerprint, remote: remote, mux: mux}

	// Adopt the new session; a lingering old one (agent reconnected before we
	// noticed the drop) is closed in its favor — the agent holding the private
	// key for this identity is by definition the current one.
	s.mu.Lock()
	old := s.sessions[nodeID]
	s.sessions[nodeID] = sess
	s.mu.Unlock()
	if old != nil {
		s.logger.Info("tunnel: replacing previous session", "node", nodeID, "old_remote", old.remote)
		_ = old.mux.Close()
	}
	s.logger.Info("tunnel: agent connected", "node", nodeID, "remote", remote, "identity", identity, "fingerprint", fingerprint)
	if s.onSession != nil {
		s.onSession(nodeID, fingerprint, true)
	}

	// Block until the session dies (keepalive failure, agent exit, or our own
	// replacement close), then evict — but only if we are still the live one.
	<-mux.CloseChan()
	s.mu.Lock()
	if s.sessions[nodeID] == sess {
		delete(s.sessions, nodeID)
	}
	replaced := s.sessions[nodeID] != nil
	s.mu.Unlock()
	s.logger.Info("tunnel: agent disconnected", "node", nodeID, "remote", remote)
	if s.onSession != nil && !replaced {
		s.onSession(nodeID, fingerprint, false)
	}
}

// DialContext opens one stream on the node's live tunnel session. It is the
// nodeclient pool's transport for tunnel: targets; each gRPC connection rides
// one stream.
func (s *Server) DialContext(_ context.Context, nodeID string) (net.Conn, error) {
	s.mu.Lock()
	sess := s.sessions[nodeID]
	s.mu.Unlock()
	if sess == nil {
		return nil, fmt.Errorf("%w %s", ErrNoSession, nodeID)
	}
	stream, err := sess.mux.Open()
	if err != nil {
		return nil, fmt.Errorf("tunnel: open stream to node %s: %w", nodeID, err)
	}
	return stream, nil
}

// DialSFTP opens one raw SFTP-typed stream on the node's live tunnel session:
// the stream carries the SFTP discriminator byte, then nothing but the SSH
// byte stream the caller pipes through it (the Agent bridges it to its local
// SFTP server; the Panel never terminates SSH).
func (s *Server) DialSFTP(nodeID string) (net.Conn, error) {
	s.mu.Lock()
	sess := s.sessions[nodeID]
	s.mu.Unlock()
	if sess == nil {
		return nil, fmt.Errorf("%w %s", ErrNoSession, nodeID)
	}
	stream, err := sess.mux.Open()
	if err != nil {
		return nil, fmt.Errorf("tunnel: open SFTP stream to node %s: %w", nodeID, err)
	}
	if _, err := stream.Write([]byte{sharedtunnel.StreamSFTP}); err != nil {
		_ = stream.Close()
		return nil, fmt.Errorf("tunnel: write SFTP discriminator to node %s: %w", nodeID, err)
	}
	return stream, nil
}

// Addr returns the listener's bound address (nil before Serve). Tests use it
// to discover the ephemeral port of a 127.0.0.1:0 listener.
func (s *Server) Addr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lis == nil {
		return nil
	}
	return s.lis.Addr()
}

// Connected reports whether the node currently has a live tunnel session.
func (s *Server) Connected(nodeID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[nodeID] != nil
}

// Close tears down the listener and every live session.
func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	lis := s.lis
	sessions := make([]*session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		sessions = append(sessions, sess)
	}
	s.mu.Unlock()
	for _, sess := range sessions {
		_ = sess.mux.Close()
	}
	if lis != nil {
		return lis.Close()
	}
	return nil
}
