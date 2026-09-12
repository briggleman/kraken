package api

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"

	"github.com/briggleman/kraken/internal/panel/cluster"
	"github.com/briggleman/kraken/internal/panel/config"
	"github.com/briggleman/kraken/internal/panel/store"
	"github.com/briggleman/kraken/internal/panel/store/memory"
	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// statusAgent is an Agent that answers Status with whatever the test sets. The
// shared fake never crashes on its own, and a crash is the whole subject here.
type statusAgent struct {
	agentpb.UnimplementedNodeServiceServer
	status *agentpb.ServerStatus
}

func (a *statusAgent) GetServerStatus(_ context.Context, req *agentpb.GetServerStatusRequest) (*agentpb.ServerStatus, error) {
	st := *a.status
	st.ServerId = req.ServerId
	return &st, nil
}

func startStatusAgent(t *testing.T, initial *agentpb.ServerStatus) (*statusAgent, string) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	a := &statusAgent{status: initial}
	srv := grpc.NewServer()
	agentpb.RegisterNodeServiceServer(srv, a)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return a, lis.Addr().String()
}

// reconcileFixture wires a Panel against one node whose Agent reports what the
// test tells it to, plus one server in the given state.
func reconcileFixture(t *testing.T, state store.ServerState, initial *agentpb.ServerStatus) (*Server, *memory.Store, *statusAgent) {
	t.Helper()
	st := memory.New()
	agent, addr := startStatusAgent(t, initial)
	ctx := context.Background()
	if err := st.CreateNode(ctx, &cluster.Node{
		ID: "node-1", Name: "abyss-lnx", OS: cluster.OSLinux, Status: cluster.NodeOnline,
		Address: addr, TotalMemoryMB: 16384,
	}); err != nil {
		t.Fatalf("create node: %v", err)
	}
	if err := st.CreateServer(ctx, &store.Server{
		ID: "sv-1", Name: "dragonwilds-01", NodeID: "node-1", State: state, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create server: %v", err)
	}
	s := New(&config.Config{Env: "test", SessionTTL: time.Hour}, st,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	return s, st, agent
}

// The crash exit code has to reach the record, or it stays where it was in
// #280 — the agent's journal, which the operator never reads. 3221225781 is
// 0xC0000135, STATUS_DLL_NOT_FOUND: the code that would have named the
// Dragonwilds problem on the first attempt instead of the fourth.
func TestReconcile_CrashCarriesTheExitCodeOntoTheRecord(t *testing.T) {
	s, st, _ := reconcileFixture(t, store.StateRunning, &agentpb.ServerStatus{
		State: agentpb.ServerState_SERVER_STATE_CRASHED, LastExitCode: 3221225781, ExitCodeKnown: true,
	})
	s.reconcileOnce(context.Background())

	sv, err := st.GetServer(context.Background(), "sv-1")
	if err != nil {
		t.Fatalf("get server: %v", err)
	}
	if sv.State != store.StateCrashed {
		t.Fatalf("state = %q, want crashed", sv.State)
	}
	if !sv.LastExitCodeKnown {
		t.Fatal("the exit code never reached the record")
	}
	if sv.LastExitCode != 3221225781 {
		t.Errorf("last_exit_code = %d, want 3221225781 (0xC0000135)", sv.LastExitCode)
	}
}

// An exit code of 0 is a real answer — a process that ended on its own without
// an error code — and must not be mistaken for "no code reported".
func TestReconcile_ZeroExitCodeIsStillKnown(t *testing.T) {
	s, st, _ := reconcileFixture(t, store.StateRunning, &agentpb.ServerStatus{
		State: agentpb.ServerState_SERVER_STATE_CRASHED, LastExitCode: 0, ExitCodeKnown: true,
	})
	s.reconcileOnce(context.Background())

	sv, _ := st.GetServer(context.Background(), "sv-1")
	if !sv.LastExitCodeKnown || sv.LastExitCode != 0 {
		t.Fatalf("last_exit_code = %d known=%v, want 0 and known", sv.LastExitCode, sv.LastExitCodeKnown)
	}
}

// The code describes the run that ended. A server that comes back up must not
// keep explaining itself with a crash it has recovered from.
func TestReconcile_RecoveryClearsTheExitCode(t *testing.T) {
	s, st, agent := reconcileFixture(t, store.StateRunning, &agentpb.ServerStatus{
		State: agentpb.ServerState_SERVER_STATE_CRASHED, LastExitCode: 134, ExitCodeKnown: true,
	})
	ctx := context.Background()
	s.reconcileOnce(ctx)
	if sv, _ := st.GetServer(ctx, "sv-1"); !sv.LastExitCodeKnown {
		t.Fatal("setup: the crash code was not recorded")
	}

	agent.status = &agentpb.ServerStatus{
		State: agentpb.ServerState_SERVER_STATE_RUNNING, LastExitCode: 134, ExitCodeKnown: true,
	}
	s.reconcileOnce(ctx)

	sv, _ := st.GetServer(ctx, "sv-1")
	if sv.State != store.StateRunning {
		t.Fatalf("state = %q, want running", sv.State)
	}
	if sv.LastExitCodeKnown || sv.LastExitCode != 0 {
		t.Errorf("a running server still carries exit %d (known=%v)", sv.LastExitCode, sv.LastExitCodeKnown)
	}
}
