package api_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"

	"github.com/briggleman/kraken/internal/agent"
	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// rpcCounts counts the unary RPCs a fake Agent served, by method name — how a
// test sees that a skipped pass still re-pushed the spec, and that a fresh
// check did not ask Steam again.
type rpcCounts struct {
	mu sync.Mutex
	n  map[string]int
}

func (c *rpcCounts) get(method string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n[method]
}

// startPlanNode is a registered, online fake Agent whose SteamCMD reports
// build 100 for the test specs' app (730), with its unary RPCs counted.
func startPlanNode(t *testing.T, h http.Handler, token, name string) (string, *agent.FakeRuntime, *rpcCounts) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	rt := agent.NewFakeRuntime(name, "linux", true, "test",
		agent.WithFakeAppBuilds(map[string]agent.FakeAppBuild{"730": {BuildID: "100"}}))
	counts := &rpcCounts{n: map[string]int{}}
	count := func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		counts.mu.Lock()
		counts.n[info.FullMethod[strings.LastIndex(info.FullMethod, "/")+1:]]++
		counts.mu.Unlock()
		return handler(ctx, req)
	}
	srv := grpc.NewServer(append(agent.ServerOptions(), grpc.ChainUnaryInterceptor(count))...)
	agentpb.RegisterNodeServiceServer(srv, agent.NewService(rt))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	nodeID := registerNode(t, h, token, lis.Addr().String())
	if rec := do(t, h, http.MethodGet, "/api/v1/nodes/"+nodeID+"/info", token, nil); rec.Code != http.StatusOK {
		t.Fatalf("node info: status %d, body %s", rec.Code, rec.Body.String())
	}
	return nodeID, rt, counts
}

// createSteamSpecWithConfig is a Steam spec (app_update, app 730) with one
// rendered config file, so a test can see the config applied on a start that
// skipped the pass.
func createSteamSpecWithConfig(t *testing.T, h http.Handler, token, slug string) string {
	t.Helper()
	rec := do(t, h, http.MethodPost, "/api/v1/specs", token, map[string]any{
		"name": "Plan Target", "slug": slug,
		"steam_app_ids": map[string]int{"linux": 730},
		"platforms":     []map[string]string{{"kind": "linux-native", "image": "registry/kraken/steam-base:latest"}},
		"install":       map[string]any{"script": "steamcmd +login anonymous +app_update {{APP_ID}} validate +quit"},
		"startup": map[string]any{
			"command": "./srv -port {{PORT_GAME}}",
			"stop":    map[string]string{"type": "signal", "value": "SIGINT"},
		},
		"ports": []map[string]any{{"name": "game", "protocol": "udp", "default": 27015, "required": true}},
		"settings": map[string]any{"groups": []map[string]any{{
			"id": "world", "label": "World",
			"fields": []map[string]any{{"key": "world_name", "label": "World name", "type": "string", "default": "Midgard"}},
		}}},
		"config_files": []map[string]any{{"path": "/data/server.cfg", "format": "source-cvar",
			"bindings": map[string]any{"servername": "world_name"}}},
		"resources": map[string]int{"min_memory_mb": 256},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create spec: %d %s", rec.Code, rec.Body.String())
	}
	var sp struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &sp)
	return sp.ID
}

// startAndSettle starts the server through the update path and waits for it
// to run, returning the install log's lines as text.
func startAndSettle(t *testing.T, h http.Handler, token, id, want string) []string {
	t.Helper()
	rec := do(t, h, http.MethodPost, "/api/v1/servers/"+id+"/power", token, map[string]string{"action": "start"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("start: got %d, want 202; body %s", rec.Code, rec.Body.String())
	}
	waitForState(t, h, token, id, want)
	var out []string
	for _, l := range getInstallLog(t, h, token, id).Lines {
		out = append(out, l.Text)
	}
	return out
}

func hasLine(lines []string, prefix string) bool {
	for _, l := range lines {
		if strings.HasPrefix(l, prefix) {
			return true
		}
	}
	return false
}

// The speed-up itself (#392): a server whose installed build is Steam's
// current one starts without the install pass. It is still stopped first,
// its spec is still re-pushed and its config re-rendered, and the console
// says what was decided and how long the check took.
func TestStart_CurrentBuildSkipsThePass(t *testing.T) {
	h, st := newTestServerStore(t)
	token := login(t, h)
	nodeID, rt, counts := startPlanNode(t, h, token, "node-current")
	specID := createSteamSpecWithConfig(t, h, token, "plan-current")
	sv := seedOfflineServer(t, st, "sv-current", nodeID, specID, nil)
	installOnFake(t, rt, sv.ID, "730")
	passes := len(rt.InstallScripts(sv.ID))
	pushes := counts.get("CreateServer")

	lines := startAndSettle(t, h, token, sv.ID, "running")

	if n := len(rt.InstallScripts(sv.ID)); n != passes {
		t.Fatalf("an install pass ran on a current build (%d → %d)", passes, n)
	}
	for _, want := range []string{
		"[panel] checking the build against Steam",
		"[panel] build check took ",
		"[panel] build 100 is current, skipping the update pass",
		"[panel] applying config and starting sv-current",
		"[panel] start took ",
	} {
		if !hasLine(lines, want) {
			t.Errorf("missing %q; lines: %q", want, lines)
		}
	}
	if hasLine(lines, "[panel] update pass took") {
		t.Errorf("an update-pass timing line on a start that ran none: %q", lines)
	}
	if counts.get("CreateServer") <= pushes {
		t.Error("a skipped pass must still re-push the spec")
	}
	cfg, _, _, _, err := rt.ReadFile(context.Background(), sv.ID, "server.cfg", 1<<20)
	if err != nil || !strings.Contains(string(cfg), "Midgard") {
		t.Errorf("config not applied on the skip path: %q, %v", cfg, err)
	}
	if u := getUpdate(t, h, token, sv.ID); u.Status != "current" || u.InstalledBuild != "100" {
		t.Errorf("update after the start: %+v", u)
	}
}

// Steam shipped a build since: the pass runs, and the row then records it.
func TestStart_NewBuildRunsThePass(t *testing.T) {
	h, st := newTestServerStore(t)
	token := login(t, h)
	nodeID, rt, _ := startPlanNode(t, h, token, "node-new")
	specID := createSteamSpecWithConfig(t, h, token, "plan-new")
	sv := seedOfflineServer(t, st, "sv-new", nodeID, specID, nil)
	installOnFake(t, rt, sv.ID, "730")
	rt.SetAppBuild("730", agent.FakeAppBuild{BuildID: "101"})
	passes := len(rt.InstallScripts(sv.ID))

	lines := startAndSettle(t, h, token, sv.ID, "running")

	if n := len(rt.InstallScripts(sv.ID)); n != passes+1 {
		t.Fatalf("install passes %d → %d, want one", passes, n)
	}
	for _, want := range []string{"[panel] build 100 → 101, running the update pass", "[panel] update pass took ", "[panel] installed build 101"} {
		if !hasLine(lines, want) {
			t.Errorf("missing %q; lines: %q", want, lines)
		}
	}
	if u := getUpdate(t, h, token, sv.ID); u.Status != "current" || u.InstalledBuild != "101" {
		t.Errorf("update after the pass: %+v", u)
	}
}

// An Agent that predates GetAppBuilds cannot be asked, which is not a failed
// check: the start takes the pass, exactly as before #392.
func TestStart_OldAgentRunsThePass(t *testing.T) {
	h, st := newTestServerStore(t)
	token := login(t, h)
	nodeID, rt, _ := startPlanNode(t, h, token, "node-old-agent")
	specID := createSteamSpecWithConfig(t, h, token, "plan-old")
	sv := seedOfflineServer(t, st, "sv-old", nodeID, specID, nil)
	installOnFake(t, rt, sv.ID, "730")
	rt.SetAppBuildsError(grpcstatus.Error(codes.Unimplemented, "unknown method GetAppBuilds"))
	passes := len(rt.InstallScripts(sv.ID))

	lines := startAndSettle(t, h, token, sv.ID, "running")

	if n := len(rt.InstallScripts(sv.ID)); n != passes+1 {
		t.Fatalf("install passes %d → %d, want one (the legacy path)", passes, n)
	}
	if !hasLine(lines, "[panel] update check unavailable (the agent on node abyss-node-01 predates the build check") {
		t.Errorf("the reason for the legacy path is not on the console: %q", lines)
	}
}

// A check the node's Agent could not answer (Unavailable: SteamCMD could not
// run there) starts on the installed tree and never lands install_failed:
// running the pass is what stranded servers during the 2026-09-25 Steam
// outage. The failure stays on the row, so the build reads unknown.
func TestStart_UnreachableCheckStartsOnTheInstalledTree(t *testing.T) {
	h, st := newTestServerStore(t)
	token := login(t, h)
	nodeID, rt, _ := startPlanNode(t, h, token, "node-failed")
	specID := createSteamSpecWithConfig(t, h, token, "plan-failed")

	// Steam unreachable: the installed build is known.
	down := seedOfflineServer(t, st, "sv-steam-down", nodeID, specID, nil)
	installOnFake(t, rt, down.ID, "730")
	rt.SetAppBuildsError(grpcstatus.Error(codes.Unavailable, "steam is down"))
	passes := len(rt.InstallScripts(down.ID))
	lines := startAndSettle(t, h, token, down.ID, "running")
	if n := len(rt.InstallScripts(down.ID)); n != passes {
		t.Fatalf("a pass ran although the check failed (%d → %d)", passes, n)
	}
	if !hasLine(lines, "[panel] update check failed (") ||
		!strings.Contains(strings.Join(lines, "\n"), "), starting on installed build 100") {
		t.Errorf("missing the failed-check line; lines: %q", lines)
	}
	if u := getUpdate(t, h, token, down.ID); u.Status != "unknown" || !strings.Contains(u.Error, "steam is down") {
		t.Errorf("the failure should stay on the row: %+v", u)
	}

}

// A check that failed for a reason about the server itself — here, no
// manifest to read — runs the pass as before #392: skipping would leave a
// server that never updates again. The pass then records the build, so the
// next check has one.
func TestStart_ManifestMissingRunsThePass(t *testing.T) {
	h, st := newTestServerStore(t)
	token := login(t, h)
	nodeID, rt, _ := startPlanNode(t, h, token, "node-bare")
	specID := createSteamSpecWithConfig(t, h, token, "plan-bare")
	bare := seedOfflineServer(t, st, "sv-no-manifest", nodeID, specID, nil)

	lines := startAndSettle(t, h, token, bare.ID, "running")
	if len(rt.InstallScripts(bare.ID)) != 1 {
		t.Fatalf("install passes: %d, want one", len(rt.InstallScripts(bare.ID)))
	}
	for _, want := range []string{
		"[panel] update check could not read the installed build (no steamapps/appmanifest_730.acf",
		"[panel] installed build 100",
	} {
		if !hasLine(lines, want) {
			t.Errorf("missing %q; lines: %q", want, lines)
		}
	}
	if u := getUpdate(t, h, token, bare.ID); u.Status != "current" || u.InstalledBuild != "100" {
		t.Errorf("update after the pass: %+v", u)
	}
}

// A check from the last few minutes stands in for a new one: the start does
// not ask Steam again. The Settings tab says the next start skips the pass,
// and when the build was checked.
func TestStart_FreshCurrentCheckIsNotAskedAgain(t *testing.T) {
	h, st := newTestServerStore(t)
	token := login(t, h)
	nodeID, rt, counts := startPlanNode(t, h, token, "node-fresh")
	specID := createSteamSpecWithConfig(t, h, token, "plan-fresh")
	sv := seedOfflineServer(t, st, "sv-fresh", nodeID, specID, nil)
	installOnFake(t, rt, sv.ID, "730")
	if u := postUpdateCheck(t, h, token, sv.ID); u.Status != "current" {
		t.Fatalf("check: %+v", u)
	}
	asked := counts.get("GetAppBuilds")

	var settings struct {
		NextStartUpdates bool    `json:"next_start_updates"`
		UpdateSkipReason string  `json:"update_skip_reason"`
		UpdateCheckedAt  *string `json:"update_checked_at"`
	}
	rec := do(t, h, http.MethodGet, "/api/v1/servers/"+sv.ID+"/settings", token, nil)
	_ = json.Unmarshal(rec.Body.Bytes(), &settings)
	if settings.NextStartUpdates || settings.UpdateSkipReason != "current_build" || settings.UpdateCheckedAt == nil {
		t.Fatalf("settings before the start: %s", rec.Body.String())
	}

	lines := startAndSettle(t, h, token, sv.ID, "running")
	if counts.get("GetAppBuilds") != asked {
		t.Error("a fresh check was asked again")
	}
	if !hasLine(lines, "[panel] build checked ") || !hasLine(lines, "[panel] build 100 is current, skipping the update pass") {
		t.Errorf("lines: %q", lines)
	}
}

// Editing a launch variable owes the pass whatever the build says — the
// install script is rendered from the variables — and the pass clears the
// debt.
func TestStart_VariableEditOwesThePass(t *testing.T) {
	h, st := newTestServerStore(t)
	token := login(t, h)
	nodeID, rt, _ := startPlanNode(t, h, token, "node-vars")
	specID := createSpec(t, h, token, "plan-vars") // declares MAX_PLAYERS
	sv := seedOfflineServer(t, st, "sv-vars", nodeID, specID, nil)
	installOnFake(t, rt, sv.ID, "730")
	if u := postUpdateCheck(t, h, token, sv.ID); u.Status != "current" {
		t.Fatalf("check: %+v", u)
	}
	if rec := do(t, h, http.MethodPut, "/api/v1/servers/"+sv.ID+"/settings", token,
		map[string]any{"values": map[string]string{}, "variables": map[string]string{"MAX_PLAYERS": "32"}}); rec.Code != http.StatusOK {
		t.Fatalf("variable edit: %d %s", rec.Code, rec.Body.String())
	}
	var settings struct {
		UpdateSkipReason string `json:"update_skip_reason"`
	}
	rec := do(t, h, http.MethodGet, "/api/v1/servers/"+sv.ID+"/settings", token, nil)
	_ = json.Unmarshal(rec.Body.Bytes(), &settings)
	if settings.UpdateSkipReason != "" {
		t.Fatalf("a variable edit must re-arm the pass; skip reason %q", settings.UpdateSkipReason)
	}
	passes := len(rt.InstallScripts(sv.ID))

	lines := startAndSettle(t, h, token, sv.ID, "running")
	if n := len(rt.InstallScripts(sv.ID)); n != passes+1 {
		t.Fatalf("install passes %d → %d, want one", passes, n)
	}
	if !hasLine(lines, "[panel] launch variables changed since the last install, running the update pass") {
		t.Errorf("lines: %q", lines)
	}
	if got, _ := st.GetServer(context.Background(), sv.ID); got.UpdatePassOwed {
		t.Error("the pass ran but update_pass_owed is still set")
	}
}
