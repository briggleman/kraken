package agent

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
	"google.golang.org/grpc"

	"github.com/briggleman/kraken/internal/shared/agentpb"
	"github.com/briggleman/kraken/internal/shared/mtls"
	sharedtunnel "github.com/briggleman/kraken/internal/shared/tunnel"
)

// TunnelClient keeps a reverse connection to the Panel open: it dials the
// Panel's tunnel listener over mutual TLS, then serves the Agent's NodeService
// on the multiplexed session so the Panel can open gRPC connections back
// through it. The node needs no inbound gRPC port at all in this mode.
//
// The gRPC service inside the tunnel is served plaintext by a dedicated
// grpc.Server instance: the tunnel session is already mutually-authenticated
// TLS, so wrapping each stream in TLS again would authenticate nothing new.
type TunnelClient struct {
	addr    string
	tlsCfg  *tls.Config
	service agentpb.NodeServiceServer
	// sftpAddr is the local SFTP server's listen address; SFTP-typed streams
	// from the Panel's proxy are bridged to it byte-for-byte (the SSH session
	// stays end-to-end between the operator's client and this Agent). Empty
	// means SFTP is unavailable and such streams are refused.
	sftpAddr string
	logger   *slog.Logger
}

// NewTunnelClient builds a tunnel client from the Agent's enrolled cert
// bundle. addr is the Panel's tunnel endpoint (host:port); sftpAddr is the
// Agent's own SFTP listen address ("" when SFTP is disabled).
func NewTunnelClient(addr, certFile, keyFile, caFile, sftpAddr string, svc agentpb.NodeServiceServer, logger *slog.Logger) (*TunnelClient, error) {
	tlsCfg, err := mtls.ClientTLS(certFile, keyFile, caFile, mtls.PanelServerName)
	if err != nil {
		return nil, err
	}
	return &TunnelClient{addr: addr, tlsCfg: tlsCfg, service: svc, sftpAddr: sftpAddr, logger: logger}, nil
}

// reconnect backoff: fast enough that a Panel restart barely registers, slow
// enough not to hammer a Panel that is down for the weekend. Jittered so a
// fleet restarting together doesn't reconnect in lockstep.
const (
	tunnelBackoffMin = 2 * time.Second
	tunnelBackoffMax = 60 * time.Second
)

// Run dials, serves, and reconnects until ctx is canceled. It never returns
// early: every failure is a logged retry, because in tunnel mode this loop IS
// the Agent's availability.
func (t *TunnelClient) Run(ctx context.Context) {
	backoff := tunnelBackoffMin
	for {
		start := time.Now()
		err := t.serveOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		// A session that held for a while earns a fresh backoff; rapid-fire
		// failures (bad address, refused handshake) climb toward the cap.
		if time.Since(start) > 2*tunnelBackoffMax {
			backoff = tunnelBackoffMin
		}
		wait := backoff + time.Duration(rand.Int64N(int64(backoff/2+1)))
		t.logger.Warn("tunnel: session ended — reconnecting", "panel", t.addr, "retry_in", wait.Round(time.Second), "err", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if backoff < tunnelBackoffMax {
			backoff *= 2
			if backoff > tunnelBackoffMax {
				backoff = tunnelBackoffMax
			}
		}
	}
}

// serveOnce runs one tunnel session to completion: TLS dial, yamux session,
// then an accept loop that routes each stream by its first byte — the HTTP/2
// preface ('P') goes to the session's gRPC server, a StreamSFTP discriminator
// gets bridged to the local SFTP server, anything else is dropped.
func (t *TunnelClient) serveOnce(ctx context.Context) error {
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	conn, err := tls.DialWithDialer(dialer, "tcp", t.addr, t.tlsCfg)
	if err != nil {
		return err
	}
	mux, err := yamux.Server(conn, sharedtunnel.Config())
	if err != nil {
		_ = conn.Close()
		return err
	}
	t.logger.Info("tunnel: connected to Panel", "panel", t.addr)

	grpcServer := grpc.NewServer()
	agentpb.RegisterNodeServiceServer(grpcServer, t.service)
	grpcStreams := newStreamListener(mux.Addr())

	grpcDone := make(chan error, 1)
	go func() { grpcDone <- grpcServer.Serve(grpcStreams) }()

	acceptDone := make(chan error, 1)
	go func() {
		for {
			stream, err := mux.Accept()
			if err != nil {
				acceptDone <- err
				return
			}
			go t.routeStream(stream, grpcStreams)
		}
	}()

	var sessErr error
	select {
	case <-ctx.Done():
		sessErr = ctx.Err()
	case sessErr = <-acceptDone:
		if sessErr == nil || errors.Is(sessErr, yamux.ErrSessionShutdown) {
			sessErr = errors.New("session closed")
		}
	}
	grpcServer.Stop()
	grpcStreams.Close()
	_ = mux.Close()
	<-grpcDone
	return sessErr
}

// routeStream reads one stream's discriminator byte and dispatches it. The
// read is deadline-bound so a stream that never sends anything can't leak.
func (t *TunnelClient) routeStream(stream net.Conn, grpcStreams *streamListener) {
	_ = stream.SetReadDeadline(time.Now().Add(30 * time.Second))
	var head [1]byte
	if _, err := io.ReadFull(stream, head[:]); err != nil {
		_ = stream.Close()
		return
	}
	_ = stream.SetReadDeadline(time.Time{})
	switch head[0] {
	case sharedtunnel.StreamGRPCPreface:
		// gRPC carries no discriminator; hand the byte back with the stream.
		if !grpcStreams.deliver(&prefixConn{Conn: stream, head: head[:]}) {
			_ = stream.Close()
		}
	case sharedtunnel.StreamSFTP:
		t.bridgeSFTP(stream)
	default:
		t.logger.Warn("tunnel: dropping stream with unknown discriminator", "byte", head[0])
		_ = stream.Close()
	}
}

// bridgeSFTP pipes a Panel-proxied SSH connection to the local SFTP server.
// The bytes are opaque: the SSH handshake, auth, and chroot all happen in the
// SFTP server exactly as they do for a LAN client.
func (t *TunnelClient) bridgeSFTP(stream net.Conn) {
	defer func() { _ = stream.Close() }()
	if t.sftpAddr == "" {
		return
	}
	addr := t.sftpAddr
	if host, port, err := net.SplitHostPort(addr); err == nil && host == "" {
		addr = net.JoinHostPort("127.0.0.1", port)
	}
	local, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		t.logger.Warn("tunnel: SFTP bridge could not reach local SFTP server", "addr", addr, "err", err)
		return
	}
	defer func() { _ = local.Close() }()
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(local, stream); _ = local.Close(); done <- struct{}{} }()
	go func() { _, _ = io.Copy(stream, local); _ = stream.Close(); done <- struct{}{} }()
	<-done
	<-done
}

// prefixConn replays already-peeked bytes ahead of the wrapped stream.
type prefixConn struct {
	net.Conn
	head []byte
}

func (c *prefixConn) Read(p []byte) (int, error) {
	if len(c.head) > 0 {
		n := copy(p, c.head)
		c.head = c.head[n:]
		return n, nil
	}
	return c.Conn.Read(p)
}

// streamListener adapts routed yamux streams back into the net.Listener shape
// grpc.Server.Serve wants to own.
type streamListener struct {
	addr    net.Addr
	streams chan net.Conn
	closed  chan struct{}
	once    sync.Once
}

func newStreamListener(addr net.Addr) *streamListener {
	return &streamListener{addr: addr, streams: make(chan net.Conn), closed: make(chan struct{})}
}

// deliver hands a stream to the gRPC server; false means the listener is
// closed and the caller keeps ownership.
func (l *streamListener) deliver(c net.Conn) bool {
	select {
	case l.streams <- c:
		return true
	case <-l.closed:
		return false
	}
}

func (l *streamListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.streams:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *streamListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *streamListener) Addr() net.Addr { return l.addr }
