package updatecheck

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	grpcstatus "google.golang.org/grpc/status"

	"github.com/briggleman/kraken/internal/agent"
	"github.com/briggleman/kraken/internal/panel/cluster"
	"github.com/briggleman/kraken/internal/panel/store"
	"github.com/briggleman/kraken/internal/panel/store/memory"
	"github.com/briggleman/kraken/internal/shared/agentpb"
	"github.com/briggleman/kraken/internal/shared/spec"
)

const palworldApp = "2394010"

// harness is a memory store, fake Agents behind real gRPC servers, and a
// client source that counts the GetAppBuilds calls the Checker makes — the
// grouping promise is "one call per (image, platform)", and the count is how
// a test holds it to that.
type harness struct {
	t       *testing.T
	st      *memory.Store
	clients *countingClients
	offline map[string]bool // node ids the liveness probe calls down
	checker *Checker
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t, st: memory.New(), clients: &countingClients{conns: map[string]agentpb.NodeServiceClient{}}, offline: map[string]bool{}}
	live := func(_ context.Context, n *cluster.Node) error {
		if h.offline[n.ID] {
			return errors.New("no route to host")
		}
		return nil
	}
	h.checker = New(h.st, h.clients, live, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return h
}

// node starts a fake Agent seeded with Palworld at build 100 and registers it
// as a node.
func (h *harness) node(id string, opts ...agent.FakeOption) *agent.FakeRuntime {
	h.t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		h.t.Fatal(err)
	}
	opts = append([]agent.FakeOption{agent.WithFakeAppBuilds(map[string]agent.FakeAppBuild{palworldApp: {BuildID: "100", TimeUpdated: 1759921187}})}, opts...)
	rt := agent.NewFakeRuntime(id, "linux", true, "test", opts...)
	srv := grpc.NewServer(agent.ServerOptions()...)
	agentpb.RegisterNodeServiceServer(srv, agent.NewService(rt))
	go func() { _ = srv.Serve(lis) }()
	h.t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { _ = conn.Close() })
	h.clients.conns[lis.Addr().String()] = agentpb.NewNodeServiceClient(conn)
	if err := h.st.CreateNode(context.Background(), &cluster.Node{ID: id, Name: id, Address: lis.Addr().String(), OS: cluster.OSLinux, Status: cluster.NodeOnline}); err != nil {
		h.t.Fatal(err)
	}
	return rt
}

// spec stores a spec; script decides whether the build check is derived.
func (h *harness) spec(id, image, script string, uc *spec.UpdateCheck) {
	h.t.Helper()
	sp := &spec.Spec{
		ID: id, Name: id, Slug: id,
		SteamAppIDs: map[string]int{"linux": 2394010, "windows": 2394010},
		Platforms:   []spec.Platform{{Kind: spec.LinuxNative, Image: image}},
		Install:     spec.Install{Script: script, UpdateCheck: uc},
	}
	if err := h.st.CreateSpec(context.Background(), sp); err != nil {
		h.t.Fatal(err)
	}
}

// server stores a server on a node and, when installed, has the fake Agent
// install it — which writes the manifest with the build of the day.
func (h *harness) server(id, specID, nodeID string, rt *agent.FakeRuntime, state store.ServerState, installed bool) {
	h.t.Helper()
	ctx := context.Background()
	if err := h.st.CreateServer(ctx, &store.Server{ID: id, Name: id, SpecID: specID, NodeID: nodeID, Kind: spec.LinuxNative, State: state}); err != nil {
		h.t.Fatal(err)
	}
	if !installed {
		return
	}
	if err := rt.Create(ctx, &agentpb.ServerSpec{ServerId: id}); err != nil {
		h.t.Fatal(err)
	}
	if err := rt.Install(ctx, &agentpb.InstallServerRequest{ServerId: id, InstallScript: "steamcmd", Env: map[string]string{"APP_ID": palworldApp}},
		func(*agentpb.InstallEvent) error { return nil }); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) row(id string) store.ServerBuild {
	h.t.Helper()
	sv, err := h.st.GetServer(context.Background(), id)
	if err != nil {
		h.t.Fatal(err)
	}
	return sv.Build
}

const steamScript = "steamcmd +force_install_dir /data +login anonymous +app_update {{APP_ID}} validate +quit"

type countingClients struct {
	conns map[string]agentpb.NodeServiceClient
	calls atomic.Int32
	// gate, when set, holds every GetAppBuilds until it is closed.
	gate chan struct{}
}

func (c *countingClients) Client(target string) (agentpb.NodeServiceClient, error) {
	conn, ok := c.conns[target]
	if !ok {
		return nil, errors.New("no such agent " + target)
	}
	return countingClient{NodeServiceClient: conn, owner: c}, nil
}

type countingClient struct {
	agentpb.NodeServiceClient
	owner *countingClients
}

func (c countingClient) GetAppBuilds(ctx context.Context, in *agentpb.GetAppBuildsRequest, opts ...grpc.CallOption) (*agentpb.GetAppBuildsResponse, error) {
	c.owner.calls.Add(1)
	if g := c.owner.gate; g != nil {
		<-g
	}
	return c.NodeServiceClient.GetAppBuilds(ctx, in, opts...)
}

// A fresh install is current; Steam shipping a build makes it available, with
// both build ids on the row.
func TestCheckServer_CurrentThenAvailable(t *testing.T) {
	h := newHarness(t)
	rt := h.node("n1")
	h.spec("pal", "steam-base", steamScript, nil)
	h.server("sv1", "pal", "n1", rt, store.StateOffline, true)
	ctx := context.Background()

	r, err := h.checker.CheckServer(ctx, "sv1")
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != StatusCurrent || r.InstalledBuild != "100" || r.AvailableBuild != "100" || r.Error != "" || r.CheckedAt == nil {
		t.Fatalf("fresh install: %+v", r)
	}
	if r.AvailableBuildAt == nil || r.AvailableBuildAt.Unix() != 1759921187 {
		t.Fatalf("available_build_at should carry Steam's timeupdated: %+v", r.AvailableBuildAt)
	}
	if b := h.row("sv1"); b.InstalledBuild != "100" || b.AvailableBuild != "100" || b.CheckedAt == nil {
		t.Fatalf("row not written: %+v", b)
	}

	rt.SetAppBuild(palworldApp, agent.FakeAppBuild{BuildID: "101"})
	r, err = h.checker.CheckServer(ctx, "sv1")
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != StatusAvailable || r.InstalledBuild != "100" || r.AvailableBuild != "101" {
		t.Fatalf("after Steam's bump: %+v", r)
	}
	if r.AvailableBuildAt != nil {
		t.Fatalf("a build Steam gave no time for must not keep the old build's: %v", r.AvailableBuildAt)
	}
}

// An Agent that predates GetAppBuilds answers Unimplemented: unknown, with a
// reason that says so — and the installed build is still read and kept.
func TestCheckServer_OldAgentIsUnknown(t *testing.T) {
	h := newHarness(t)
	rt := h.node("n1")
	h.spec("pal", "steam-base", steamScript, nil)
	h.server("sv1", "pal", "n1", rt, store.StateOffline, true)
	rt.SetAppBuildsError(grpcstatus.Error(codes.Unimplemented, "unknown method GetAppBuilds"))

	r, err := h.checker.CheckServer(context.Background(), "sv1")
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != StatusUnknown || !strings.Contains(r.Error, "predates the build check") || r.InstalledBuild != "100" {
		t.Fatalf("old agent: %+v", r)
	}
	if b := h.row("sv1"); !strings.Contains(b.CheckError, "predates") || b.CheckedAt == nil {
		t.Fatalf("row should record the failed check: %+v", b)
	}
}

// The other ways a check cannot compare: no manifest yet, the node down, an
// app Steam does not know.
func TestCheckServer_UnknownReasons(t *testing.T) {
	h := newHarness(t)
	rt := h.node("n1")
	h.spec("pal", "steam-base", steamScript, nil)
	h.spec("other", "steam-base", steamScript, &spec.UpdateCheck{Method: spec.UpdateCheckSteam, AppID: 999})
	h.server("never-installed", "pal", "n1", rt, store.StateOffline, false)
	h.server("unknown-app", "other", "n1", rt, store.StateOffline, true)
	ctx := context.Background()

	r, _ := h.checker.CheckServer(ctx, "never-installed")
	if r.Status != StatusUnknown || !strings.Contains(r.Error, "appmanifest_2394010.acf") {
		t.Errorf("missing manifest: %+v", r)
	}
	r, _ = h.checker.CheckServer(ctx, "unknown-app")
	if r.Status != StatusUnknown || !strings.Contains(r.Error, "steam:") {
		t.Errorf("per-app error: %+v", r)
	}

	h.offline["n1"] = true
	r, _ = h.checker.CheckServer(ctx, "never-installed")
	if r.Status != StatusUnknown || !strings.Contains(r.Error, "offline") {
		t.Errorf("node offline: %+v", r)
	}
}

// A spec with no build to check (Factorio's shape: no app_update, or an
// explicit method none) is unsupported, asks no Agent anything and writes
// nothing.
func TestCheckServer_Unsupported(t *testing.T) {
	h := newHarness(t)
	rt := h.node("n1")
	h.spec("factorio", "steam-base", "curl -sSL https://factorio.com/get-download/stable/headless/linux64 | tar -xJ", nil)
	h.spec("optout", "steam-base", steamScript, &spec.UpdateCheck{Method: spec.UpdateCheckNone})
	h.server("f1", "factorio", "n1", rt, store.StateOffline, false)
	h.server("o1", "optout", "n1", rt, store.StateOffline, true)

	for _, id := range []string{"f1", "o1"} {
		r, err := h.checker.CheckServer(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if r.Status != StatusUnsupported {
			t.Errorf("%s: %+v, want unsupported", id, r)
		}
		if b := h.row(id); b.CheckedAt != nil {
			t.Errorf("%s: an unsupported check wrote the row: %+v", id, b)
		}
	}
	if n := h.clients.calls.Load(); n != 0 {
		t.Errorf("GetAppBuilds calls = %d, want none", n)
	}
}

// A fleet pass asks once per (image, platform) however many servers share it,
// checks each of them, and leaves alone the servers it must not read: one
// installing (its manifest may be mid-write) and one with no build to check.
func TestCheckAll_GroupsAndSkips(t *testing.T) {
	h := newHarness(t)
	rt := h.node("n1")
	h.spec("pal", "steam-base", steamScript, nil)
	h.spec("factorio", "steam-base", "./get-factorio.sh", nil)
	h.server("a", "pal", "n1", rt, store.StateRunning, true)
	h.server("b", "pal", "n1", rt, store.StateOffline, true)
	h.server("busy", "pal", "n1", rt, store.StateInstalling, true)
	h.server("f", "factorio", "n1", rt, store.StateOffline, false)

	if err := h.checker.CheckAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := h.clients.calls.Load(); n != 1 {
		t.Fatalf("GetAppBuilds calls = %d, want 1 for two servers of one spec", n)
	}
	for _, id := range []string{"a", "b"} {
		if b := h.row(id); b.InstalledBuild != "100" || b.AvailableBuild != "100" || b.CheckedAt == nil || b.CheckError != "" {
			t.Errorf("%s: %+v, want current at 100", id, b)
		}
	}
	for _, id := range []string{"busy", "f"} {
		if b := h.row(id); b.CheckedAt != nil {
			t.Errorf("%s was checked: %+v", id, b)
		}
	}
}

// A group asks the next of its nodes when the first cannot answer — down, or
// running an Agent that predates the RPC partway through a fleet upgrade — so
// one old node does not leave every server of the game unknown.
func TestCheckAll_TriesTheGroupsOtherNodes(t *testing.T) {
	h := newHarness(t)
	oldAgent := h.node("n1")
	newAgent := h.node("n2")
	oldAgent.SetAppBuildsError(grpcstatus.Error(codes.Unimplemented, "unknown method"))
	h.spec("pal", "steam-base", steamScript, nil)
	h.server("on-old", "pal", "n1", oldAgent, store.StateOffline, true)
	h.server("on-new", "pal", "n2", newAgent, store.StateOffline, true)

	if err := h.checker.CheckAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"on-old", "on-new"} {
		if b := h.row(id); b.AvailableBuild != "100" || b.CheckError != "" {
			t.Errorf("%s: %+v, want the build from the node that could answer", id, b)
		}
	}
}

// A second CheckAll while one runs joins it: one pass, one GetAppBuilds, and
// both callers return when it ends.
func TestCheckAll_SingleFlight(t *testing.T) {
	h := newHarness(t)
	rt := h.node("n1")
	h.spec("pal", "steam-base", steamScript, nil)
	h.server("a", "pal", "n1", rt, store.StateOffline, true)
	h.clients.gate = make(chan struct{})

	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(1)
	go func() { defer wg.Done(); errs[0] = h.checker.CheckAll(context.Background()) }()
	waitFor(t, func() bool { return h.clients.calls.Load() == 1 })
	if !h.checker.Running() {
		t.Fatal("Running() = false with a pass in flight")
	}
	wg.Add(1)
	go func() { defer wg.Done(); errs[1] = h.checker.CheckAll(context.Background()) }()
	time.Sleep(50 * time.Millisecond) // long enough for a second pass to have asked, were it to
	close(h.clients.gate)
	wg.Wait()

	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("errors: %v", errs)
	}
	if n := h.clients.calls.Load(); n != 1 {
		t.Fatalf("GetAppBuilds calls = %d, want 1 (the second caller joins)", n)
	}
	if h.checker.Running() {
		t.Fatal("Running() = true after the pass ended")
	}
}

// After an install pass the manifest's build is both the installed and the
// available build: app_update just pulled the branch's current one.
func TestRecordInstalled(t *testing.T) {
	h := newHarness(t)
	rt := h.node("n1")
	h.spec("pal", "steam-base", steamScript, nil)
	h.server("sv1", "pal", "n1", rt, store.StateOffline, true)
	ctx := context.Background()
	sv, _ := h.st.GetServer(ctx, "sv1")
	sp, _ := h.st.GetSpec(ctx, "pal")
	client, _ := h.clients.Client(h.clients.addr())

	// A check from before the install said an update was waiting.
	at := time.Unix(1, 0)
	if err := h.st.UpdateServerBuild(ctx, "sv1", store.ServerBuild{InstalledBuild: "99", AvailableBuild: "100", AvailableBuildAt: &at, CheckError: "stale"}); err != nil {
		t.Fatal(err)
	}
	build, err := h.checker.RecordInstalled(ctx, sv, sp, client)
	if err != nil || build != "100" {
		t.Fatalf("RecordInstalled = %q, %v", build, err)
	}
	b := h.row("sv1")
	if b.InstalledBuild != "100" || b.AvailableBuild != "100" || b.CheckError != "" || b.CheckedAt == nil {
		t.Fatalf("row: %+v", b)
	}
	if b.AvailableBuildAt == nil || !b.AvailableBuildAt.Equal(at) {
		t.Fatalf("Steam's time for the same build should be kept: %v", b.AvailableBuildAt)
	}
	if n := h.clients.calls.Load(); n != 0 {
		t.Fatalf("recording an install must not ask Steam: %d calls", n)
	}

	// No manifest: an error for the caller to log, and the row as it was.
	h.server("bare", "pal", "n1", rt, store.StateOffline, false)
	bare, _ := h.st.GetServer(ctx, "bare")
	if _, err := h.checker.RecordInstalled(ctx, bare, sp, client); err == nil {
		t.Fatal("want an error without a manifest")
	}
	if b := h.row("bare"); b.CheckedAt != nil {
		t.Fatalf("row written without a manifest: %+v", b)
	}
}

// addr is the one Agent's address, for tests with a single node.
func (c *countingClients) addr() string {
	for a := range c.conns {
		return a
	}
	return ""
}

// Evaluate reads the status from the row and the spec at read time.
func TestEvaluate(t *testing.T) {
	now := time.Now()
	steamSpec := &spec.Spec{SteamAppIDs: map[string]int{"linux": 1}, Install: spec.Install{Script: "app_update 1"}}
	noneSpec := &spec.Spec{SteamAppIDs: map[string]int{"linux": 1}, Install: spec.Install{Script: "app_update 1", UpdateCheck: &spec.UpdateCheck{Method: spec.UpdateCheckNone}}}
	cases := []struct {
		name string
		b    store.ServerBuild
		sp   *spec.Spec
		want string
	}{
		{"never checked", store.ServerBuild{}, steamSpec, StatusUnknown},
		{"current", store.ServerBuild{InstalledBuild: "1", AvailableBuild: "1", CheckedAt: &now}, steamSpec, StatusCurrent},
		{"available", store.ServerBuild{InstalledBuild: "1", AvailableBuild: "2", CheckedAt: &now}, steamSpec, StatusAvailable},
		{"failed check", store.ServerBuild{InstalledBuild: "1", AvailableBuild: "1", CheckedAt: &now, CheckError: "x"}, steamSpec, StatusUnknown},
		{"half known", store.ServerBuild{InstalledBuild: "1", CheckedAt: &now}, steamSpec, StatusUnknown},
		// A spec that opted out since the last check reads unsupported at once.
		{"spec opted out", store.ServerBuild{InstalledBuild: "1", AvailableBuild: "1", CheckedAt: &now}, noneSpec, StatusUnsupported},
		{"spec gone", store.ServerBuild{}, nil, StatusUnknown},
	}
	for _, tc := range cases {
		got := Evaluate(&store.Server{Kind: spec.LinuxNative, Build: tc.b}, tc.sp)
		if got.Status != tc.want {
			t.Errorf("%s: status %q, want %q (%+v)", tc.name, got.Status, tc.want, got)
		}
	}
}

func TestJitterStaysWithinTenPercent(t *testing.T) {
	d := 24 * time.Hour
	for range 1000 {
		if j := jitter(d); j < d-d/10 || j > d+d/10 {
			t.Fatalf("jitter(%v) = %v, outside ±10%%", d, j)
		}
	}
	if jitter(0) != 0 {
		t.Fatal("jitter(0) must stay 0")
	}
}

// The timer runs a pass after the first delay and stops when its context
// ends.
func TestRunStopsWithItsContext(t *testing.T) {
	h := newHarness(t)
	rt := h.node("n1")
	h.spec("pal", "steam-base", steamScript, nil)
	h.server("a", "pal", "n1", rt, store.StateOffline, true)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.checker.Run(ctx, time.Millisecond, time.Hour); close(done) }()
	waitFor(t, func() bool { return h.row("a").CheckedAt != nil })
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its context ended")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 5s")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
