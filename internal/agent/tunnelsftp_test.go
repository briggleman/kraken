package agent_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/briggleman/kraken/internal/agent"
	"github.com/briggleman/kraken/internal/panel/nodeclient"
	paneltunnel "github.com/briggleman/kraken/internal/panel/tunnel"
	"github.com/briggleman/kraken/internal/shared/agentpb"
	"github.com/briggleman/kraken/internal/shared/mtls"
)

// TestTunnelSFTPBridge proves the demux both ways at once: an SFTP-typed
// stream opened via DialSFTP reaches a stand-in "SFTP" server behind the agent
// (a line-echo server on the agent's sftpAddr) with byte fidelity, while a
// gRPC RPC over the same session still answers. This is the #90 proxy path
// minus the real SSH server — the bridge is protocol-agnostic, so an echo
// server exercises exactly the bytes-in-bytes-out contract SSH relies on.
func TestTunnelSFTPBridge(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	caCert, caKey, err := mtls.GenerateCA()
	if err != nil {
		t.Fatal(err)
	}
	panelCert, panelKey, err := mtls.IssuePanelServerCert(caCert, caKey, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	serverTLS, err := mtls.ServerTLSFromBytes(panelCert, panelKey, caCert)
	if err != nil {
		t.Fatal(err)
	}
	agentKey, csr, err := mtls.NewAgentKeyAndCSR(nil)
	if err != nil {
		t.Fatal(err)
	}
	agentCert, err := mtls.SignAgentCSRWithIdentity(caCert, caKey, csr, time.Hour, "ident-sftp")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certFile, keyFile, caFile := filepath.Join(dir, "a.pem"), filepath.Join(dir, "a-key.pem"), filepath.Join(dir, "ca.pem")
	for f, b := range map[string][]byte{certFile: agentCert, keyFile: agentKey, caFile: caCert} {
		if err := os.WriteFile(f, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// Stand-in for the agent's local SFTP server: a line-echo TCP server.
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = echo.Close() })
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				sc := bufio.NewScanner(c)
				for sc.Scan() {
					fmt.Fprintf(c, "echo:%s\n", sc.Text())
				}
			}(c)
		}
	}()

	resolve := func(_ context.Context, identity string) (string, error) {
		if identity != "ident-sftp" {
			return "", os.ErrNotExist
		}
		return "node-sftp", nil
	}
	srv := paneltunnel.New(serverTLS, resolve, logger)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Serve(ctx, "127.0.0.1:0") }()
	t.Cleanup(func() { _ = srv.Close() })
	deadline := time.Now().Add(3 * time.Second)
	for srv.Addr() == nil {
		if time.Now().After(deadline) {
			t.Fatal("tunnel listener never bound")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Agent dials out, advertising the echo server as its "SFTP" address.
	rt := agent.NewFakeRuntime("abyss-node-01", "linux", true, "test")
	tc, err := agent.NewTunnelClient(srv.Addr().String(), certFile, keyFile, caFile, echo.Addr().String(), agent.NewService(rt), logger)
	if err != nil {
		t.Fatal(err)
	}
	go tc.Run(ctx)
	for !srv.Connected("node-sftp") {
		if time.Now().After(deadline) {
			t.Fatal("agent never connected its tunnel")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// SFTP-typed stream → bridged to the echo server behind the agent.
	stream, err := srv.DialSFTP("node-sftp")
	if err != nil {
		t.Fatalf("DialSFTP: %v", err)
	}
	defer func() { _ = stream.Close() }()
	if _, err := io.WriteString(stream, "hello\n"); err != nil {
		t.Fatalf("write to bridged stream: %v", err)
	}
	line, err := bufio.NewReader(stream).ReadString('\n')
	if err != nil {
		t.Fatalf("read from bridged stream: %v", err)
	}
	if line != "echo:hello\n" {
		t.Fatalf("bridge byte fidelity: got %q", line)
	}

	// gRPC over the same session still works — the demux routed the SFTP
	// stream without disturbing the gRPC path.
	pool := nodeclient.NewInsecurePool(nodeclient.WithTunnelDialer(srv.DialContext))
	t.Cleanup(func() { _ = pool.Close() })
	client, err := pool.Client("tunnel:node-sftp")
	if err != nil {
		t.Fatal(err)
	}
	rctx, rcancel := context.WithTimeout(ctx, 5*time.Second)
	defer rcancel()
	info, err := client.GetNodeInfo(rctx, &agentpb.GetNodeInfoRequest{})
	if err != nil {
		t.Fatalf("GetNodeInfo after SFTP bridge: %v", err)
	}
	if info.NodeId != "abyss-node-01" {
		t.Fatalf("node info: %+v", info)
	}
}
