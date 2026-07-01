// Package nodeclient manages the Panel's outbound gRPC connections to node
// Agents. It caches one connection per Agent address and hands out typed
// NodeService clients. Connections are created lazily and reused.
package nodeclient

import (
	"crypto/tls"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// Pool is a concurrency-safe cache of Agent gRPC connections keyed by address.
type Pool struct {
	mu       sync.Mutex
	conns    map[string]*grpc.ClientConn
	dialOpts []grpc.DialOption
}

// NewInsecurePool returns a Pool that dials Agents without TLS. For local
// development only.
func NewInsecurePool() *Pool {
	return &Pool{
		conns:    make(map[string]*grpc.ClientConn),
		dialOpts: []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())},
	}
}

// NewTLSPool dials Agents over mutual TLS using the given client config.
func NewTLSPool(cfg *tls.Config) *Pool {
	return &Pool{
		conns:    make(map[string]*grpc.ClientConn),
		dialOpts: []grpc.DialOption{grpc.WithTransportCredentials(credentials.NewTLS(cfg))},
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
