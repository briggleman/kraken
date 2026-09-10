package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"
)

// retryInterval is how often a listenGuard re-attempts a bind it lost. Slow on
// purpose: the conflict it exists for (another process holding the port) is
// resolved by an operator or by that process exiting, neither of which happens
// on a sub-minute timescale, and the retry log line must not become noise in
// agent.log.
const retryInterval = 30 * time.Second

// listenGuard owns the lifecycle of one of the agent's inbound listeners: it
// binds, publishes why it couldn't when it can't, keeps retrying until the port
// frees up, and hands the listener to a serve func once one lands.
//
// It exists because a tunnel-mode agent needs no inbound port at all — the
// Panel reaches it over the reverse tunnel the agent dials out. #235 was an
// 11-hour outage where a Windows agent crash-looped on a :9090 held by a WSL
// agent on the same host (mirrored networking makes the two share one port
// space) until the SCM gave up, all while its tunnel would have served the
// Panel fine and the reason lived only in the node's own agent.log.
type listenGuard struct {
	name     string        // "grpc" | "sftp" — labels the reported error
	addr     string        // listen address, as configured
	interval time.Duration // retry cadence; shortened by tests
	logger   *slog.Logger

	mu     sync.Mutex
	reason string // bind failure, "" while the listener is up
}

func newListenGuard(name, addr string, logger *slog.Logger) *listenGuard {
	return &listenGuard{name: name, addr: addr, interval: retryInterval, logger: logger}
}

// listen makes one bind attempt and records the outcome for status.
func (g *listenGuard) listen() (net.Listener, error) {
	lis, err := net.Listen("tcp", g.addr)
	g.mu.Lock()
	defer g.mu.Unlock()
	if err != nil {
		g.reason = bindReason(err)
		return nil, err
	}
	g.reason = ""
	return lis, nil
}

// status reports the listener's problem as "<name> <addr>: <reason>", or "" when
// it is serving. Read at NodeInfo-poll time, so it must stay cheap.
func (g *listenGuard) status() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.reason == "" {
		return ""
	}
	return g.name + " " + g.addr + ": " + g.reason
}

// setStatus records a non-bind failure (a serve func that refused to start) so
// it reaches the Panel the same way a bind conflict does.
func (g *listenGuard) setStatus(err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err == nil {
		g.reason = ""
		return
	}
	g.reason = bindReason(err)
}

// serveWithRetry hands lis to serve, retrying the bind every interval when lis
// is nil (the caller's own attempt failed and it chose to degrade rather than
// exit). serve takes ownership of the listener; its error is returned verbatim.
// A ctx that ends before any bind succeeds returns nil — that is a shutdown,
// not a failure.
func (g *listenGuard) serveWithRetry(ctx context.Context, lis net.Listener, serve func(net.Listener) error) error {
	for lis == nil {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(g.interval):
		}
		var err error
		if lis, err = g.listen(); err != nil {
			g.logger.Warn("inbound listener still unavailable — retrying",
				"listener", g.name, "addr", g.addr, "err", err, "retry_in", g.interval)
		}
	}
	return serve(lis)
}

// listenStatus aggregates the guards' problems into the one string NodeInfo
// carries, in the order given. Empty means every configured listener is up.
func listenStatus(guards ...*listenGuard) string {
	var down []string
	for _, g := range guards {
		if g == nil {
			continue
		}
		if s := g.status(); s != "" {
			down = append(down, s)
		}
	}
	return strings.Join(down, "; ")
}

// bindReason reduces a listen error to its cause. net.Listen wraps it as
// "listen tcp <addr>: bind: …", and the guard prints the address itself — the
// aggregated status would otherwise repeat it twice per listener.
func bindReason(err error) string {
	if err == nil {
		return ""
	}
	var op *net.OpError
	if errors.As(err, &op) && op.Err != nil {
		return op.Err.Error()
	}
	return err.Error()
}
