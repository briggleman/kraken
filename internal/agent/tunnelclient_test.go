package agent_test

import (
	"context"
	"io"
	"log/slog"
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

// TestTunnelEndToEnd proves the whole reverse path: the agent's TunnelClient
// dials the Panel's tunnel listener with an identity-bearing cert, the Panel
// binds the session to a node, and a NodeService RPC routed through
// DialContext (exactly what the nodeclient pool does) reaches the agent's
// service and answers.
func TestTunnelEndToEnd(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// PKI: CA, panel server cert, agent cert bound to "ident-e2e".
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
	agentCert, err := mtls.SignAgentCSRWithIdentity(caCert, caKey, csr, time.Hour, "ident-e2e")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certFile, keyFile, caFile := filepath.Join(dir, "agent.pem"), filepath.Join(dir, "agent-key.pem"), filepath.Join(dir, "ca.pem")
	for f, b := range map[string][]byte{certFile: agentCert, keyFile: agentKey, caFile: caCert} {
		if err := os.WriteFile(f, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// Panel side: tunnel server binding ident-e2e -> node-e2e.
	resolve := func(_ context.Context, identity string) (string, error) {
		if identity != "ident-e2e" {
			return "", os.ErrNotExist
		}
		return "node-e2e", nil
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

	// Agent side: TunnelClient serving the fake runtime's NodeService.
	rt := agent.NewFakeRuntime("abyss-node-01", "linux", true, "test")
	tc, err := agent.NewTunnelClient(srv.Addr().String(), certFile, keyFile, caFile, agent.NewService(rt), logger)
	if err != nil {
		t.Fatal(err)
	}
	go tc.Run(ctx)

	for !srv.Connected("node-e2e") {
		if time.Now().After(deadline) {
			t.Fatal("agent never connected its tunnel")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Panel→Agent RPC through the tunnel, exactly as the nodeclient pool
	// routes it: a plaintext gRPC connection whose transport is one stream.
	pool := nodeclient.NewInsecurePool(nodeclient.WithTunnelDialer(srv.DialContext))
	t.Cleanup(func() { _ = pool.Close() })
	client, err := pool.Client("tunnel:node-e2e")
	if err != nil {
		t.Fatal(err)
	}
	rctx, rcancel := context.WithTimeout(ctx, 5*time.Second)
	defer rcancel()
	info, err := client.GetNodeInfo(rctx, &agentpb.GetNodeInfoRequest{})
	if err != nil {
		t.Fatalf("GetNodeInfo over tunnel: %v", err)
	}
	if info.NodeId != "abyss-node-01" {
		t.Fatalf("node info over tunnel: %+v", info)
	}

	// And a node without a live tunnel fails fast, not with a hang.
	if _, err := pool.Client("tunnel:node-ghost"); err != nil {
		t.Fatalf("pool should build the client lazily: %v", err)
	}
	ghost, _ := pool.Client("tunnel:node-ghost")
	gctx, gcancel := context.WithTimeout(ctx, 3*time.Second)
	defer gcancel()
	if _, err := ghost.GetNodeInfo(gctx, &agentpb.GetNodeInfoRequest{}); err == nil {
		t.Fatal("RPC to a tunnel-less node should fail")
	}
}
