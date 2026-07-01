package agent_test

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/briggleman/kraken/internal/agent"
	"github.com/briggleman/kraken/internal/shared/agentpb"
)

func newClient(t *testing.T) agentpb.NodeServiceClient {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	rt := agent.NewFakeRuntime("abyss-node-01", "linux", true, "test")
	agentpb.RegisterNodeServiceServer(srv, agent.NewService(rt))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return agentpb.NewNodeServiceClient(conn)
}

func TestGetNodeInfo(t *testing.T) {
	c := newClient(t)
	info, err := c.GetNodeInfo(context.Background(), &agentpb.GetNodeInfoRequest{})
	if err != nil {
		t.Fatalf("GetNodeInfo: %v", err)
	}
	if info.NodeId != "abyss-node-01" || info.Os != "linux" || !info.WineEnabled {
		t.Fatalf("unexpected node info: %+v", info)
	}
}

func TestInstallStreamThenPower(t *testing.T) {
	c := newClient(t)
	ctx := context.Background()

	stream, err := c.InstallServer(ctx, &agentpb.InstallServerRequest{ServerId: "s1", Image: "img/linux"})
	if err != nil {
		t.Fatalf("InstallServer: %v", err)
	}
	var sawLog, sawCompleted bool
	for {
		ev, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("install recv: %v", err)
		}
		switch ev.Event.(type) {
		case *agentpb.InstallEvent_LogLine:
			sawLog = true
		case *agentpb.InstallEvent_Completed:
			sawCompleted = true
		case *agentpb.InstallEvent_Failed:
			t.Fatalf("install failed: %s", ev.GetFailed())
		}
	}
	if !sawLog || !sawCompleted {
		t.Fatalf("install stream incomplete: sawLog=%v sawCompleted=%v", sawLog, sawCompleted)
	}

	// After install, server is offline.
	st, err := c.GetServerStatus(ctx, &agentpb.GetServerStatusRequest{ServerId: "s1"})
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st.State != agentpb.ServerState_SERVER_STATE_OFFLINE {
		t.Fatalf("expected OFFLINE after install, got %v", st.State)
	}

	// Start it → RUNNING.
	pr, err := c.PowerAction(ctx, &agentpb.PowerActionRequest{ServerId: "s1", Action: agentpb.PowerAction_POWER_ACTION_START})
	if err != nil {
		t.Fatalf("power start: %v", err)
	}
	if pr.State != agentpb.ServerState_SERVER_STATE_RUNNING {
		t.Fatalf("expected RUNNING after start, got %v", pr.State)
	}
	st, _ = c.GetServerStatus(ctx, &agentpb.GetServerStatusRequest{ServerId: "s1"})
	if st.State != agentpb.ServerState_SERVER_STATE_RUNNING {
		t.Fatalf("status should be RUNNING, got %v", st.State)
	}
}

func TestStreamConsoleCancels(t *testing.T) {
	c := newClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stream, err := c.StreamConsole(ctx, &agentpb.StreamConsoleRequest{ServerId: "s1", TailLines: 2})
	if err != nil {
		t.Fatalf("StreamConsole: %v", err)
	}
	// The two replayed lines should arrive promptly.
	for i := 0; i < 2; i++ {
		line, err := stream.Recv()
		if err != nil {
			t.Fatalf("console recv %d: %v", i, err)
		}
		if line.ServerId != "s1" || line.Text == "" {
			t.Fatalf("unexpected console line: %+v", line)
		}
	}
	cancel() // client cancels the stream
	// Subsequent Recv should error out (context cancelled) rather than hang.
	done := make(chan struct{})
	go func() {
		for {
			if _, err := stream.Recv(); err != nil {
				close(done)
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("console stream did not terminate after cancel")
	}
}

func TestStreamStatsCancels(t *testing.T) {
	c := newClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stream, err := c.StreamStats(ctx, &agentpb.StreamStatsRequest{ServerId: "s1", IntervalMs: 100})
	if err != nil {
		t.Fatalf("StreamStats: %v", err)
	}
	stat, err := stream.Recv()
	if err != nil {
		t.Fatalf("stats recv: %v", err)
	}
	if stat.MemoryLimitMb == 0 {
		t.Fatalf("expected stats payload, got %+v", stat)
	}
	cancel()
}

func TestSendCommand(t *testing.T) {
	c := newClient(t)
	if _, err := c.SendCommand(context.Background(), &agentpb.SendCommandRequest{ServerId: "s1", Command: "say hi"}); err != nil {
		t.Fatalf("SendCommand: %v", err)
	}
}
