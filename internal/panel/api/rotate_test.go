package api

import (
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/briggleman/kraken/internal/agent"
	"github.com/briggleman/kraken/internal/panel/cluster"
	"github.com/briggleman/kraken/internal/panel/config"
	"github.com/briggleman/kraken/internal/panel/store/memory"
	"github.com/briggleman/kraken/internal/shared/agentpb"
	"github.com/briggleman/kraken/internal/shared/mtls"
)

// TestReconcileRotatesExpiringAgentCert exercises the full rotation loop over
// real gRPC + mutual TLS: an agent serving with a nearly-expired cert is
// reconciled by the Panel, which drives Begin/CompleteCertRotation and leaves
// the agent hot-swapped onto a fresh 90-day cert — no restart, no token.
func TestReconcileRotatesExpiringAgentCert(t *testing.T) {
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// CA + an agent bundle with only ~5 days of validity left (< rotateBefore).
	caCert, caKey, err := mtls.GenerateCA()
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	keyPEM, csrPEM, err := mtls.NewAgentKeyAndCSR([]string{"127.0.0.1"})
	if err != nil {
		t.Fatalf("NewAgentKeyAndCSR: %v", err)
	}
	certPEM, err := mtls.SignAgentCSR(caCert, caKey, csrPEM, 5*24*time.Hour)
	if err != nil {
		t.Fatalf("SignAgentCSR: %v", err)
	}
	certFile := filepath.Join(dir, "agent.pem")
	keyFile := filepath.Join(dir, "agent-key.pem")
	caFile := filepath.Join(dir, "ca.pem")
	for p, b := range map[string][]byte{certFile: certPEM, keyFile: keyPEM, caFile: caCert} {
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}

	// Real agent gRPC server: mTLS with the CertManager serving the cert.
	cm, err := agent.NewCertManager(certFile, keyFile, caFile, logger)
	if err != nil {
		t.Fatalf("NewCertManager: %v", err)
	}
	tlsCfg, err := mtls.ServerTLS(certFile, keyFile, caFile)
	if err != nil {
		t.Fatalf("ServerTLS: %v", err)
	}
	tlsCfg.Certificates = nil
	tlsCfg.GetCertificate = cm.GetCertificate
	grpcServer := grpc.NewServer(grpc.Creds(credentials.NewTLS(tlsCfg)))
	rt := agent.NewFakeRuntime("rotate-node", "linux", true, "test")
	agentpb.RegisterNodeServiceServer(grpcServer, agent.NewService(rt, agent.WithCertManager(cm)))
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = grpcServer.Serve(lis) }()
	t.Cleanup(grpcServer.Stop)

	// Panel with the same CA and an auto-issued client cert (mirrors prod boot).
	pCert, pKey, err := mtls.IssuePanelClientCert(caCert, caKey, time.Hour)
	if err != nil {
		t.Fatalf("IssuePanelClientCert: %v", err)
	}
	st := memory.New()
	s := New(&config.Config{Env: "test", SessionTTL: time.Hour}, st, logger,
		WithCA(caCert, caKey),
		WithClientTLSBytes(pCert, pKey, caCert),
	)
	t.Cleanup(func() { _ = s.Close() })

	n := &cluster.Node{
		ID:      "node-rotate-test",
		Name:    "rotate-node",
		OS:      cluster.OSLinux,
		Status:  cluster.NodeOffline,
		Address: lis.Addr().String(),
		Ports:   cluster.NewPortPool(),
	}
	if err := st.CreateNode(context.Background(), n); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}

	oldNotAfter := cm.NotAfter()
	info, err := s.reconcileNode(context.Background(), n)
	if err != nil {
		t.Fatalf("reconcileNode over mTLS: %v", err)
	}
	if info.CertNotAfterUnix == 0 {
		t.Fatal("NodeInfo did not report cert expiry")
	}

	// Rotation happened synchronously inside reconcile: the agent now serves
	// a fresh long-lived cert.
	newNotAfter := cm.NotAfter()
	if !newNotAfter.After(oldNotAfter.Add(24 * time.Hour)) {
		t.Fatalf("cert was not rotated: notAfter still %v", newNotAfter)
	}
	if time.Until(newNotAfter) < 80*24*time.Hour {
		t.Fatalf("rotated cert validity too short: %v", time.Until(newNotAfter))
	}

	// A second reconcile must NOT rotate again (fresh cert + per-node throttle).
	if _, err := s.reconcileNode(context.Background(), n); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if !cm.NotAfter().Equal(newNotAfter) {
		t.Fatal("cert rotated again despite fresh validity")
	}
}
