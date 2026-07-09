// Package nodeclient manages the Panel's outbound gRPC connections to node
// Agents. It caches one connection per Agent address and hands out typed
// NodeService clients. Connections are created lazily and reused.
package nodeclient

import (
	"context"
	"crypto/tls"
	"log/slog"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// Pool is a concurrency-safe cache of Agent gRPC connections keyed by address.
type Pool struct {
	mu       sync.Mutex
	conns    map[string]*grpc.ClientConn
	dialOpts []grpc.DialOption

	logger  *slog.Logger
	lastLog map[string]time.Time // per-address throttle for transport-failure logs
}

// Option customizes a Pool at construction time.
type Option func(*Pool)

// WithLogger enables transport-failure logging: RPCs that fail with
// codes.Unavailable (dial refused, TLS handshake rejected, …) are logged with
// the gRPC error detail, throttled to one line per address per 30s so the
// reconciler's 4s polling doesn't flood the log.
func WithLogger(l *slog.Logger) Option {
	return func(p *Pool) { p.logger = l }
}

func newPool(creds grpc.DialOption, opts ...Option) *Pool {
	p := &Pool{
		conns:   make(map[string]*grpc.ClientConn),
		lastLog: make(map[string]time.Time),
	}
	for _, o := range opts {
		o(p)
	}
	p.dialOpts = []grpc.DialOption{
		creds,
		grpc.WithChainUnaryInterceptor(p.unaryFailureLogger()),
		grpc.WithChainStreamInterceptor(p.streamFailureLogger()),
	}
	return p
}

// NewInsecurePool returns a Pool that dials Agents without TLS. For local
// development only.
func NewInsecurePool(opts ...Option) *Pool {
	return newPool(grpc.WithTransportCredentials(insecure.NewCredentials()), opts...)
}

// NewTLSPool dials Agents over mutual TLS using the given client config.
func NewTLSPool(cfg *tls.Config, opts ...Option) *Pool {
	return newPool(grpc.WithTransportCredentials(credentials.NewTLS(cfg)), opts...)
}

// logTransportFailure logs err for target if it is a transport-level failure
// (codes.Unavailable — includes TLS handshake rejections, whose x509 reason
// gRPC embeds in the error text) and the address hasn't been logged recently.
func (p *Pool) logTransportFailure(target, method string, err error) {
	if p.logger == nil || err == nil {
		return
	}
	s, ok := status.FromError(err)
	if !ok || s.Code() != codes.Unavailable {
		return
	}
	p.mu.Lock()
	last := p.lastLog[target]
	now := time.Now()
	if now.Sub(last) < 30*time.Second {
		p.mu.Unlock()
		return
	}
	p.lastLog[target] = now
	p.mu.Unlock()
	p.logger.Warn("agent RPC transport failure (logged at most once per 30s per node)",
		"target", target, "method", method, "err", err)
}

func (p *Pool) unaryFailureLogger() grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		err := invoker(ctx, method, req, reply, cc, opts...)
		p.logTransportFailure(cc.Target(), method, err)
		return err
	}
}

func (p *Pool) streamFailureLogger() grpc.StreamClientInterceptor {
	return func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		cs, err := streamer(ctx, desc, cc, method, opts...)
		p.logTransportFailure(cc.Target(), method, err)
		return cs, err
	}
}

// Client returns a NodeService client for the Agent at addr, creating and
// caching the connection on first use.
func (p *Pool) Client(addr string) (agentpb.NodeServiceClient, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if conn, ok := p.conns[addr]; ok {
		return agentpb.NewNodeServiceClient(conn), nil
	}
	conn, err := grpc.NewClient(addr, p.dialOpts...)
	if err != nil {
		return nil, err
	}
	p.conns[addr] = conn
	return agentpb.NewNodeServiceClient(conn), nil
}

// Close tears down every cached connection.
func (p *Pool) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	var firstErr error
	for addr, conn := range p.conns {
		if err := conn.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		delete(p.conns, addr)
	}
	return firstErr
}
