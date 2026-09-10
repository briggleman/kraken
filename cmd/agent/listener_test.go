package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// occupied binds a loopback port and returns its address plus a release func.
// Real sockets, not a fake: the whole point of the guard is the OS's answer to
// a contended bind, and that answer differs per platform (Windows says "Only
// one usage of each socket address…", Linux "address already in use").
func occupied(t *testing.T) (addr string, release func()) {
	t.Helper()
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("hold a loopback port: %v", err)
	}
	closed := false
	return held.Addr().String(), func() {
		if !closed {
			closed = true
			_ = held.Close()
		}
	}
}

// TestListenGuardReportsAndRecovers is the #235 scenario in miniature: the port
// the agent wants is already held, so the bind fails and the reason has to reach
// NodeInfo instead of taking the process down; when the holder goes away the
// guard binds and serves without anyone restarting it.
func TestListenGuardReportsAndRecovers(t *testing.T) {
	addr, release := occupied(t)
	defer release()

	g := newListenGuard("grpc", addr, quietLogger())
	g.interval = 10 * time.Millisecond // real retries, test-scale cadence

	if lis, err := g.listen(); err == nil {
		_ = lis.Close()
		t.Fatal("bind succeeded on a port another listener holds")
	}
	status := g.status()
	if !strings.HasPrefix(status, "grpc "+addr+": ") {
		t.Fatalf("status %q does not name the listener and address", status)
	}
	// The OS phrasing differs per platform; what must survive is that the
	// syscall's own reason is in there rather than a summary we invented.
	if !strings.Contains(status, "bind:") {
		t.Fatalf("status %q dropped the bind reason", status)
	}
	if agg := listenStatus(g, nil); agg != status {
		t.Fatalf("listenStatus = %q, want the single guard's status %q", agg, status)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	served := make(chan net.Listener, 1)
	done := make(chan error, 1)
	go func() {
		// lis nil = the caller degraded rather than exited, which is what tunnel
		// mode does. serve blocks like grpcServer.Serve until the listener closes.
		done <- g.serveWithRetry(ctx, nil, func(l net.Listener) error {
			served <- l
			for {
				c, err := l.Accept()
				if err != nil {
					return nil
				}
				_ = c.Close()
			}
		})
	}()

	// The retry has to keep failing while the port is held. Sampling status
	// rather than counting ticks: a loaded CI box can run any number of them.
	time.Sleep(5 * g.interval)
	if s := g.status(); s == "" {
		t.Fatal("guard cleared its error while the port was still held")
	}
	select {
	case l := <-served:
		t.Fatalf("guard served %s while the port was held", l.Addr())
	default:
	}

	release()

	// Recovery, waited for with a generous deadline and a tight poll instead of
	// a fixed number of sleeps — the assertion is "recovers on its own", and a
	// deadline can only make this test slower, never flaky.
	var bound net.Listener
	select {
	case bound = <-served:
	case <-time.After(5 * time.Second):
		t.Fatalf("guard never rebound %s after the port was freed (status %q)", addr, g.status())
	}
	if got := bound.Addr().String(); got != addr {
		t.Fatalf("served %s, want the configured %s", got, addr)
	}
	if s := g.status(); s != "" {
		t.Fatalf("status %q not cleared after a successful bind", s)
	}
	if agg := listenStatus(g, nil); agg != "" {
		t.Fatalf("listenStatus = %q, want empty once every listener is up", agg)
	}

	// Closing the listener is what ends grpcServer.Serve too, so the guard's
	// return path is exercised the way run() exercises it.
	_ = bound.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serveWithRetry: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serveWithRetry did not return after the serve func stopped")
	}
}

// TestListenGuardServesGivenListener covers the healthy path: the caller's own
// bind succeeded, so serveWithRetry must use that listener immediately rather
// than waiting out a retry interval.
func TestListenGuardServesGivenListener(t *testing.T) {
	g := newListenGuard("grpc", "127.0.0.1:0", quietLogger())
	g.interval = time.Hour // a wait here would be a bug, so make it obvious

	lis, err := g.listen()
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	if s := g.status(); s != "" {
		t.Fatalf("status %q on a successful bind", s)
	}

	want := errors.New("serve stopped")
	got := make(chan error, 1)
	go func() {
		got <- g.serveWithRetry(context.Background(), lis, func(l net.Listener) error {
			_ = l.Close()
			return want
		})
	}()
	select {
	case err := <-got:
		if !errors.Is(err, want) {
			t.Fatalf("serveWithRetry = %v, want the serve func's error %v", err, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serveWithRetry did not use the listener it was handed")
	}
}

// TestListenGuardCancelBeforeBind: a shutdown while the port is still held is
// not a failure — run() must not report an error for it.
func TestListenGuardCancelBeforeBind(t *testing.T) {
	addr, release := occupied(t)
	defer release()

	g := newListenGuard("sftp", addr, quietLogger())
	g.interval = 10 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- g.serveWithRetry(ctx, nil, func(net.Listener) error {
			t.Error("serve ran on a port that was never free")
			return nil
		})
	}()
	time.Sleep(3 * g.interval)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serveWithRetry = %v, want nil for a shutdown", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serveWithRetry ignored ctx cancellation")
	}
}

// TestListenStatusAggregates locks the wire format the node card renders.
func TestListenStatusAggregates(t *testing.T) {
	grpcG := newListenGuard("grpc", ":9090", quietLogger())
	sftpG := newListenGuard("sftp", ":2022", quietLogger())
	grpcG.setStatus(errors.New("bind: port in use"))
	sftpG.setStatus(errors.New("bind: port in use"))

	want := "grpc :9090: bind: port in use; sftp :2022: bind: port in use"
	if got := listenStatus(grpcG, sftpG); got != want {
		t.Fatalf("listenStatus = %q, want %q", got, want)
	}
	grpcG.setStatus(nil)
	if got := listenStatus(grpcG, sftpG); got != "sftp :2022: bind: port in use" {
		t.Fatalf("listenStatus = %q, want only the still-down listener", got)
	}
	sftpG.setStatus(nil)
	if got := listenStatus(grpcG, sftpG); got != "" {
		t.Fatalf("listenStatus = %q, want empty", got)
	}
}
