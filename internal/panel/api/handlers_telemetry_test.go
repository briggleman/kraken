package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"google.golang.org/grpc"

	"github.com/briggleman/kraken/internal/agent"
	"github.com/briggleman/kraken/internal/panel"
	"github.com/briggleman/kraken/internal/panel/api"
	"github.com/briggleman/kraken/internal/panel/config"
	"github.com/briggleman/kraken/internal/panel/store/memory"
	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// newTelemetryTestServer is newTestServerStore but keeps the *api.Server, which
// the telemetry tests need in order to drive the poller directly instead of
// waiting on the background ticker.
func newTelemetryTestServer(t *testing.T) (*api.Server, http.Handler) {
	t.Helper()
	st := memory.New()
	cfg := &config.Config{
		Env:                    "test",
		SessionTTL:             time.Hour,
		BootstrapAdminUser:     testAdmin,
		BootstrapAdminPassword: testPass,
		SetupAllowedCIDRs:      []string{"192.0.2.0/24"},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := panel.Seed(context.Background(), st, cfg, logger); err != nil {
		t.Fatalf("seed: %v", err)
	}
	clearMustChangePassword(t, st, testAdmin)
	srv := api.New(cfg, st, logger)
	return srv, srv.Handler()
}

// startAgentWithTelemetry runs a real Agent gRPC server with a host sampler
// wired, so GetNodeTelemetry answers with this machine's actual vitals.
func startAgentWithTelemetry(t *testing.T, nodeID string) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	sampler := agent.NewHostSampler(t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	sampler.Start(ctx)

	rt := agent.NewFakeRuntime(nodeID, "linux", true, "test")
	srv := grpc.NewServer()
	agentpb.RegisterNodeServiceServer(srv, agent.NewService(rt, agent.WithHostSampler(sampler)))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return lis.Addr().String()
}

// startAgentWithoutTelemetry runs an Agent with no sampler — the stand-in for a
// fleet member that predates the telemetry RPC.
func startAgentWithoutTelemetry(t *testing.T, nodeID string) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	rt := agent.NewFakeRuntime(nodeID, "linux", true, "test")
	srv := grpc.NewServer()
	agentpb.RegisterNodeServiceServer(srv, agent.NewService(rt))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return lis.Addr().String()
}

type telemetryResponse struct {
	Nodes map[string]struct {
		TsUnixMs   int64   `json:"ts_unix_ms"`
		CPUPercent float64 `json:"cpu_percent"`
		CPUKnown   bool    `json:"cpu_known"`
		MemTotalMB int64   `json:"mem_total_mb"`
		MemUsedMB  int64   `json:"mem_used_mb"`
		MemKnown   bool    `json:"mem_known"`
		DiskKnown  bool    `json:"disk_known"`
		TempKnown  bool    `json:"temp_known"`
	} `json:"nodes"`
}

func getTelemetry(t *testing.T, h http.Handler, token string) telemetryResponse {
	t.Helper()
	rec := do(t, h, http.MethodGet, "/api/v1/nodes/telemetry", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("telemetry: status %d, body %s", rec.Code, rec.Body.String())
	}
	var out telemetryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("telemetry decode: %v", err)
	}
	return out
}

// waitForTelemetry polls the endpoint until the node shows up, since the sweep
// runs in the background.
func waitForTelemetry(t *testing.T, h http.Handler, token, nodeID string) telemetryResponse {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		out := getTelemetry(t, h, token)
		if _, ok := out.Nodes[nodeID]; ok {
			return out
		}
		if time.Now().After(deadline) {
			t.Fatalf("node %s never appeared in the telemetry payload", nodeID)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestNodeTelemetryReachesTheAPI(t *testing.T) {
	srv, h := newTelemetryTestServer(t)
	token := login(t, h)
	addr := startAgentWithTelemetry(t, "telemetry-node")
	id := registerNode(t, h, token, addr)
	pollNode(t, h, token, id) // reconcile it online so the sweep will dial it

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.StartNodeTelemetryPoller(ctx)

	out := waitForTelemetry(t, h, token, id)
	got := out.Nodes[id]
	if got.TsUnixMs <= 0 {
		t.Errorf("telemetry should be timestamped, got %d", got.TsUnixMs)
	}
	// Memory is instantaneous, so it is available from the sampler's very first
	// reading — no second sample needed.
	if !got.MemKnown {
		t.Error("memory should be known from a real host sampler")
	}
	if got.MemTotalMB <= 0 || got.MemUsedMB > got.MemTotalMB {
		t.Errorf("implausible memory: used %d of %dMB", got.MemUsedMB, got.MemTotalMB)
	}
	if got.CPUKnown && (got.CPUPercent < 0 || got.CPUPercent > 100) {
		t.Errorf("cpu percent %.2f outside 0..100", got.CPUPercent)
	}
}

// An Agent too old to serve the RPC must simply have no telemetry — and must
// not be marked unhealthy for it. A fleet mid-upgrade is a normal state.
func TestNodeTelemetryAbsentForOldAgent(t *testing.T) {
	srv, h := newTelemetryTestServer(t)
	token := login(t, h)
	addr := startAgentWithoutTelemetry(t, "old-node")
	id := registerNode(t, h, token, addr)
	pollNode(t, h, token, id)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.StartNodeTelemetryPoller(ctx)

	// Give the sweep time to run and fail the way it will in production.
	time.Sleep(500 * time.Millisecond)
	out := getTelemetry(t, h, token)
	if _, ok := out.Nodes[id]; ok {
		t.Error("a node whose agent lacks the RPC must be absent, not present with zeroes")
	}

	status, _ := nodeStatusAndReason(t, h, token, id)
	if status != "online" {
		t.Errorf("node status = %q, want online — missing telemetry is not ill health", status)
	}
}

// A node the Panel cannot reach has no vitals to report. Absence is the point:
// the UI renders empty instruments rather than the last good numbers.
func TestNodeTelemetryAbsentForUnreachableNode(t *testing.T) {
	srv, h := newTelemetryTestServer(t)
	token := login(t, h)
	// A port nothing is listening on.
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	dead := lis.Addr().String()
	_ = lis.Close()

	id := registerNode(t, h, token, dead)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.StartNodeTelemetryPoller(ctx)

	time.Sleep(500 * time.Millisecond)
	out := getTelemetry(t, h, token)
	if _, ok := out.Nodes[id]; ok {
		t.Error("an unreachable node must not appear in the telemetry payload")
	}
}

func TestNodeTelemetryRequiresAuth(t *testing.T) {
	_, h := newTelemetryTestServer(t)
	rec := do(t, h, http.MethodGet, "/api/v1/nodes/telemetry", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 for an unauthenticated request", rec.Code)
	}
}

// The static /nodes/telemetry route must not be swallowed by /nodes/{id}.
func TestNodeTelemetryRouteNotShadowedByNodeID(t *testing.T) {
	_, h := newTelemetryTestServer(t)
	token := login(t, h)
	rec := do(t, h, http.MethodGet, "/api/v1/nodes/telemetry", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s — expected the telemetry handler, not get-node", rec.Code, rec.Body.String())
	}
	var out telemetryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Nodes == nil {
		t.Error("expected a nodes map, even when empty")
	}
}
