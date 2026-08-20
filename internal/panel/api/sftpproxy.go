package api

import (
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/briggleman/kraken/internal/panel/cluster"
)

// sftpProxy fronts tunnel-mode nodes' per-server SFTP servers, which accept
// nothing inbound on their own network. It listens on one Panel-side TCP port
// per tunneled node and pipes each accepted connection over a raw SFTP-typed
// tunnel stream to that node's agent, which bridges it to its local SFTP
// server. The Panel never terminates SSH: credentials and host keys stay
// agent-side end to end, so this is a byte pump, not an SSH server.
//
// Raw SSH carries no routing header a pass-through could read, so nodes can't
// share one port — each tunneled node gets its own, allocated upward from the
// configured base and persisted on the node record so the operator's saved
// SFTP config survives Panel restarts.
type sftpProxy struct {
	dial     func(nodeID string) (net.Conn, error)
	host     string
	basePort int
	logger   logger

	mu        sync.Mutex
	listeners map[string]*sftpListener // node id → its listener
	usedPorts map[int]string           // port → node id (allocation bookkeeping)
	closed    bool
}

// logger is the slim slice of *slog.Logger the proxy needs (keeps this file's
// signatures short and testable).
type logger interface {
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
}

type sftpListener struct {
	nodeID string
	port   int
	lis    net.Listener
}

func newSFTPProxy(dial func(nodeID string) (net.Conn, error), host string, basePort int, lg logger) *sftpProxy {
	return &sftpProxy{
		dial:      dial,
		host:      host,
		basePort:  basePort,
		logger:    lg,
		listeners: map[string]*sftpListener{},
		usedPorts: map[int]string{},
	}
}

// port returns the Panel-side SFTP port currently serving a node, or 0 if the
// proxy isn't fronting it.
func (p *sftpProxy) port(nodeID string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if l := p.listeners[nodeID]; l != nil {
		return l.port
	}
	return 0
}

// reconcile brings the set of live listeners in line with the node list: every
// tunnel-mode node with a live session gets a listener (reusing its persisted
// port when free), and listeners for nodes that are gone, went direct, or lost
// their tunnel are torn down. Returns the map of node id → assigned port so the
// caller can persist any newly-allocated ports on the node records.
func (p *sftpProxy) reconcile(nodes []*cluster.Node, connected func(nodeID string) bool) map[string]int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}

	want := map[string]*cluster.Node{}
	for _, n := range nodes {
		if n.Tunneled() && connected(n.ID) {
			want[n.ID] = n
		}
	}

	// Tear down listeners no longer wanted.
	for id, l := range p.listeners {
		if _, ok := want[id]; !ok {
			_ = l.lis.Close()
			delete(p.usedPorts, l.port)
			delete(p.listeners, id)
			p.logger.Info("sftp-proxy: stopped listener", "node", id, "port", l.port)
		}
	}

	assigned := map[string]int{}
	for id, n := range want {
		if l := p.listeners[id]; l != nil {
			assigned[id] = l.port
			continue
		}
		port := p.allocatePortLocked(n.SFTPProxyPort)
		if port == 0 {
			p.logger.Warn("sftp-proxy: no free port for node", "node", id)
			continue
		}
		lis, err := net.Listen("tcp", net.JoinHostPort(p.bindHost(), strconv.Itoa(port)))
		if err != nil {
			p.logger.Warn("sftp-proxy: could not bind listener", "node", id, "port", port, "err", err)
			continue
		}
		l := &sftpListener{nodeID: id, port: port, lis: lis}
		p.listeners[id] = l
		p.usedPorts[port] = id
		assigned[id] = port
		go p.serve(l)
		p.logger.Info("sftp-proxy: started listener", "node", id, "port", port)
	}
	return assigned
}

// allocatePortLocked picks a port for a node: its persisted preference when
// free, otherwise the lowest free port at or above the base. 0 = none free
// within a sane window. Caller holds p.mu.
func (p *sftpProxy) allocatePortLocked(preferred int) int {
	if preferred >= p.basePort {
		if _, taken := p.usedPorts[preferred]; !taken {
			return preferred
		}
	}
	for port := p.basePort; port < p.basePort+1000; port++ {
		if _, taken := p.usedPorts[port]; !taken {
			return port
		}
	}
	return 0
}

func (p *sftpProxy) bindHost() string {
	// Bind all interfaces by default; p.host is only the advertised host shown
	// to operators, not necessarily a local bind address.
	return ""
}

func (p *sftpProxy) serve(l *sftpListener) {
	for {
		conn, err := l.lis.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			p.logger.Warn("sftp-proxy: accept failed", "node", l.nodeID, "err", err)
			return
		}
		go p.handle(l.nodeID, conn)
	}
}

// handle bridges one accepted TCP connection to a fresh SFTP-typed tunnel
// stream and pipes bytes both ways until either side closes.
func (p *sftpProxy) handle(nodeID string, client net.Conn) {
	defer func() { _ = client.Close() }()
	stream, err := p.dial(nodeID)
	if err != nil {
		p.logger.Warn("sftp-proxy: could not open tunnel stream", "node", nodeID, "err", err)
		return
	}
	defer func() { _ = stream.Close() }()
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(stream, client); _ = stream.Close(); done <- struct{}{} }()
	go func() { _, _ = io.Copy(client, stream); _ = client.Close(); done <- struct{}{} }()
	<-done
	<-done
}

// close tears down every listener. Idempotent.
func (p *sftpProxy) close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.closed = true
	for _, l := range p.listeners {
		_ = l.lis.Close()
	}
	p.listeners = map[string]*sftpListener{}
	p.usedPorts = map[int]string{}
}

// startSFTPProxyReconciler runs the proxy's periodic reconcile against the live
// node list, persisting any newly-allocated ports. Cheap and idempotent, so it
// rides the same cadence as the node reconciler.
func (s *Server) startSFTPProxyReconciler(ctx context.Context) {
	if s.sftpProxy == nil {
		return
	}
	go func() {
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		s.reconcileSFTPProxy(ctx) // once up front
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.reconcileSFTPProxy(ctx)
			}
		}
	}()
}

func (s *Server) reconcileSFTPProxy(ctx context.Context) {
	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		return
	}
	assigned := s.sftpProxy.reconcile(nodes, s.tunnel.Connected)
	// Persist any port that changed so the operator's saved SFTP config is
	// stable across Panel restarts.
	for _, n := range nodes {
		port := assigned[n.ID]
		if port != 0 && n.SFTPProxyPort != port {
			n.SFTPProxyPort = port
			_ = s.store.UpdateNode(ctx, n)
		}
	}
}
