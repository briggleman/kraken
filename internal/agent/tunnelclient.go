package agent

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"math/rand/v2"
	"net"
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
	logger  *slog.Logger
}

// NewTunnelClient builds a tunnel client from the Agent's enrolled cert
// bundle. addr is the Panel's tunnel endpoint (host:port).
func NewTunnelClient(addr, certFile, keyFile, caFile string, svc agentpb.NodeServiceServer, logger *slog.Logger) (*TunnelClient, error) {
	tlsCfg, err := mtls.ClientTLS(certFile, keyFile, caFile, mtls.PanelServerName)
	if err != nil {
		return nil, err
	}
	return &TunnelClient{addr: addr, tlsCfg: tlsCfg, service: svc, logger: logger}, nil
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
// gRPC serve over the session's streams.
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

	done := make(chan error, 1)
	go func() { done <- grpcServer.Serve(mux) }()

	select {
	case <-ctx.Done():
		grpcServer.Stop()
		_ = mux.Close()
		<-done
		return ctx.Err()
	case err := <-done:
		_ = mux.Close()
		if err == nil || errors.Is(err, yamux.ErrSessionShutdown) {
			return errors.New("session closed")
		}
		return err
	}
}
