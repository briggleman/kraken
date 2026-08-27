package agent_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"

	"github.com/briggleman/kraken/internal/agent"
	"github.com/briggleman/kraken/internal/shared/agentpb"
	"github.com/briggleman/kraken/internal/shared/mtls"
)

// writePEM drops a PEM blob into dir and returns its path.
func writePEM(t *testing.T, dir, name string, pem []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, pem, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// enrollAgent mints an agent cert the way the Panel's /agents/enroll does.
func enrollAgent(t *testing.T, dir, prefix string, caCert, caKey []byte, hosts []string) (certFile, keyFile string) {
	t.Helper()
	keyPEM, csrPEM, err := mtls.NewAgentKeyAndCSR(hosts)
	if err != nil {
		t.Fatal(err)
	}
	certPEM, err := mtls.SignAgentCSRWithIdentity(caCert, caKey, csrPEM, mtls.DefaultAgentCertTTL, "id-"+prefix)
	if err != nil {
		t.Fatal(err)
	}
	return writePEM(t, dir, prefix+".pem", certPEM), writePEM(t, dir, prefix+"-key.pem", keyPEM)
}

// startVictimAgent brings up an agent gRPC listener exactly as cmd/agent does.
func startVictimAgent(t *testing.T, certFile, keyFile, caFile string) string {
	t.Helper()
	tlsCfg, err := mtls.ServerTLS(certFile, keyFile, caFile)
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(tlsCfg)))
	agentpb.RegisterNodeServiceServer(srv, agent.NewService(agent.NewFakeRuntime("victim", "linux", false, "test")))
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return lis.Addr().String()
}

// TestAgentRejectsPeerAgentCertificate is the exploit: node A's own enrolled
// certificate must NOT authenticate it to node B's gRPC listener. Agent certs
// carry ClientAuth (they need it to dial the reverse tunnel), so without a
// peer-identity check any agent — or anyone who redeems a single enrollment
// token — can drive the full NodeService on every other node in the fleet,
// UpdateAgent (arbitrary binary → RCE) included.
func TestAgentRejectsPeerAgentCertificate(t *testing.T) {
	dir := t.TempDir()
	caCert, caKey, err := mtls.GenerateCA()
	if err != nil {
		t.Fatal(err)
	}
	caFile := writePEM(t, dir, "ca.pem", caCert)

	victimCert, victimKey := enrollAgent(t, dir, "victim", caCert, caKey, []string{"127.0.0.1"})
	addr := startVictimAgent(t, victimCert, victimKey, caFile)

	// The attacker holds a *legitimately issued* agent cert — stolen from a
	// compromised node, or minted by redeeming one leaked bootstrap token.
	attackerCert, attackerKey := enrollAgent(t, dir, "attacker", caCert, caKey, nil)

	clientCfg, err := mtls.ClientTLS(attackerCert, attackerKey, caFile, mtls.AgentServerName)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(credentials.NewTLS(clientCfg)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	info, err := agentpb.NewNodeServiceClient(conn).GetNodeInfo(ctx, &agentpb.GetNodeInfoRequest{})
	if err == nil {
		t.Fatalf("EXPLOITED: an agent certificate authenticated to a peer agent; "+
			"attacker read node info: node=%q os=%q version=%q", info.NodeId, info.Os, info.AgentVersion)
	}
	t.Logf("rejected as expected: %v", err)
}

// TestAgentAcceptsPanelCertificate is the guardrail on the fix above: the
// Panel's own client cert must still authenticate.
func TestAgentAcceptsPanelCertificate(t *testing.T) {
	dir := t.TempDir()
	caCert, caKey, err := mtls.GenerateCA()
	if err != nil {
		t.Fatal(err)
	}
	caFile := writePEM(t, dir, "ca.pem", caCert)

	victimCert, victimKey := enrollAgent(t, dir, "node", caCert, caKey, []string{"127.0.0.1"})
	addr := startVictimAgent(t, victimCert, victimKey, caFile)

	panelCert, panelKey, err := mtls.IssuePanelClientCert(caCert, caKey, mtls.DefaultPanelClientCertTTL)
	if err != nil {
		t.Fatal(err)
	}
	clientCfg, err := mtls.ClientTLS(
		writePEM(t, dir, "panel.pem", panelCert),
		writePEM(t, dir, "panel-key.pem", panelKey),
		caFile, mtls.AgentServerName)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(credentials.NewTLS(clientCfg)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := agentpb.NewNodeServiceClient(conn).GetNodeInfo(ctx, &agentpb.GetNodeInfoRequest{}); err != nil {
		t.Fatalf("the Panel's own client cert was rejected: %v", err)
	}
}

// TestEnrollmentCannotMintPanelIdentity: the CSR's SANs are attacker-chosen, so
// signing must never let an enrollee request the Panel's logical name — that
// would forge exactly the identity the check above trusts.
func TestEnrollmentCannotMintPanelIdentity(t *testing.T) {
	caCert, caKey, err := mtls.GenerateCA()
	if err != nil {
		t.Fatal(err)
	}
	_, csrPEM, err := mtls.NewAgentKeyAndCSR([]string{mtls.PanelServerName, "evil.example"})
	if err != nil {
		t.Fatal(err)
	}
	certPEM, err := mtls.SignAgentCSRWithIdentity(caCert, caKey, csrPEM, mtls.DefaultAgentCertTTL, "id-evil")
	if err != nil {
		return // refusing to sign is an acceptable outcome
	}
	for _, h := range mtls.SANHosts(certPEM) {
		if h == mtls.PanelServerName {
			t.Fatalf("EXPLOITED: enrollment issued an agent cert carrying the Panel's identity %q", h)
		}
	}
}

// TestAgentRejectsPeerAgentReachingUpdateRPC makes the impact concrete: the
// same peer-agent cert is pointed straight at UpdateAgent — the RPC that
// overwrites the node's own binary and re-execs it (remote code execution).
//
// The victim service here has no self-updater wired, so a *dispatched*
// UpdateAgent returns codes.FailedPrecondition ("self-update is not
// available") — an application-layer answer that only comes back AFTER the
// caller was authenticated and the handler ran. A transport-layer rejection
// (the TLS peer was refused) surfaces as codes.Unavailable instead. That gap
// is the whole test: pre-fix the attacker reaches the RCE handler; post-fix
// the handshake is refused before any handler runs.
func TestAgentRejectsPeerAgentReachingUpdateRPC(t *testing.T) {
	dir := t.TempDir()
	caCert, caKey, err := mtls.GenerateCA()
	if err != nil {
		t.Fatal(err)
	}
	caFile := writePEM(t, dir, "ca.pem", caCert)

	victimCert, victimKey := enrollAgent(t, dir, "victim", caCert, caKey, []string{"127.0.0.1"})
	addr := startVictimAgent(t, victimCert, victimKey, caFile)

	attackerCert, attackerKey := enrollAgent(t, dir, "attacker", caCert, caKey, nil)
	clientCfg, err := mtls.ClientTLS(attackerCert, attackerKey, caFile, mtls.AgentServerName)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(credentials.NewTLS(clientCfg)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := agentpb.NewNodeServiceClient(conn).UpdateAgent(ctx)
	if err == nil {
		err = stream.Send(&agentpb.UpdateAgentChunk{Payload: &agentpb.UpdateAgentChunk_Meta_{
			Meta: &agentpb.UpdateAgentChunk_Meta{Version: "evil", Os: "linux", Arch: "amd64"},
		}})
		if err == nil {
			_, err = stream.CloseAndRecv()
		}
	}
	if status.Code(err) == codes.FailedPrecondition {
		t.Fatalf("EXPLOITED: peer-agent cert reached the UpdateAgent (RCE) handler; got %v", err)
	}
	if status.Code(err) != codes.Unavailable {
		t.Logf("note: expected transport rejection (Unavailable); got code=%s err=%v", status.Code(err), err)
	}
	t.Logf("update RPC rejected at transport as expected: %v", err)
}
