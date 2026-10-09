package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"

	"github.com/briggleman/kraken/internal/agent"
	"github.com/briggleman/kraken/internal/panel/alerts"
	"github.com/briggleman/kraken/internal/panel/cluster"
	"github.com/briggleman/kraken/internal/panel/config"
	"github.com/briggleman/kraken/internal/panel/push"
	"github.com/briggleman/kraken/internal/panel/rbac"
	"github.com/briggleman/kraken/internal/panel/store"
	"github.com/briggleman/kraken/internal/panel/store/memory"
	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// The push-alert pipeline end to end (#348): the real handlers and
// reconcilers against the fake Agent runtime, the real relay client, and a
// relay that opens every envelope with the device's private key — so what is
// asserted is what the phone would read.

// rosterAgent is the fake Agent with one addition: the roster the Docker
// runtime attaches to a running server's status. The fake runtime reports
// none, and a player joining is one of the events under test.
type rosterAgent struct {
	agentpb.NodeServiceServer
	mu    sync.Mutex
	names []string
}

func (a *rosterAgent) setRoster(names ...string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.names = names
}

func (a *rosterAgent) GetServerStatus(ctx context.Context, req *agentpb.GetServerStatusRequest) (*agentpb.ServerStatus, error) {
	st, err := a.NodeServiceServer.GetServerStatus(ctx, req)
	if err != nil || st.State != agentpb.ServerState_SERVER_STATE_RUNNING {
		return st, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	ls := &agentpb.ResourceStats{ServerId: req.ServerId, PlayersKnown: true, Players: int32(len(a.names)), MaxPlayers: 32}
	for _, n := range a.names {
		ls.OnlinePlayers = append(ls.OnlinePlayers, &agentpb.OnlinePlayer{Name: n})
	}
	st.LastStats = ls
	return st, nil
}

// stubRelay is an httptest relay that opens each envelope with the one device
// key it holds and keeps the payloads in arrival order.
type stubRelay struct {
	t       *testing.T
	key     []byte
	panelID string
	mu      sync.Mutex
	got     []push.Payload
	tokens  []string
}

func (r *stubRelay) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.URL.Path != "/v1/push" || req.Method != http.MethodPost {
		http.Error(w, "not here", http.StatusNotFound)
		return
	}
	if !strings.HasPrefix(req.Header.Get("User-Agent"), "kraken-panel/") || req.Header.Get("X-Kraken-Panel-Id") != r.panelID {
		r.t.Errorf("relay request headers: UA %q, panel id %q", req.Header.Get("User-Agent"), req.Header.Get("X-Kraken-Panel-Id"))
	}
	var body struct{ Token, Environment, Payload string }
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	p, err := push.Open(r.key, body.Payload)
	if err != nil {
		r.t.Errorf("the relay could not open an envelope with the device's key: %v", err)
		http.Error(w, "bad envelope", http.StatusBadRequest)
		return
	}
	r.mu.Lock()
	r.got = append(r.got, p)
	r.tokens = append(r.tokens, body.Token)
	r.mu.Unlock()
	w.WriteHeader(http.StatusAccepted)
}

func (r *stubRelay) events() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, p := range r.got {
		out = append(out, p.Event)
	}
	return out
}

func (r *stubRelay) last() push.Payload {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.got[len(r.got)-1]
}

type pipeline struct {
	t      *testing.T
	srv    *Server
	h      http.Handler
	st     *memory.Store
	rt     *agent.FakeRuntime
	roster *rosterAgent
	relay  *stubRelay
	grpc   *grpc.Server
	token  string
}

const (
	pipeServer = "4866d26c-5f6c-4c8f-9f68-2a8a1b1c0d01"
	pipeNode   = "bd70f48c-0e0e-4b8d-8a3c-6f4b0a1d2e02"
	pipeDevice = "e621e1f8-c36c-495a-93fc-0c247a3e6e5f"
	pipeToken  = "740f4707bebcf74f9b7c25d48e3358945f6aa01da5ddb387462c7eaf61bb78ad"
)

func newPipeline(t *testing.T) *pipeline {
	t.Helper()
	ctx := context.Background()
	st := memory.New()
	for _, r := range rbac.BuiltinRoles() {
		r := r
		if err := st.UpsertRole(ctx, &r); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.CreateUser(ctx, &store.User{ID: "ann", Username: "ann", RoleID: rbac.RoleOwner, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSession(ctx, &store.Session{Token: "ann-token", UserID: "ann", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}

	// The Agent: the fake runtime behind the real service, plus a roster.
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	rt := agent.NewFakeRuntime("abyss-lnx", "linux", true, "test")
	roster := &rosterAgent{NodeServiceServer: agent.NewService(rt)}
	g := grpc.NewServer(agent.ServerOptions()...)
	agentpb.RegisterNodeServiceServer(g, roster)
	go func() { _ = g.Serve(lis) }()
	t.Cleanup(g.Stop)

	if err := st.CreateNode(ctx, &cluster.Node{
		ID: pipeNode, Name: "abyss-lnx", OS: cluster.OSLinux, Status: cluster.NodeOnline,
		Address: lis.Addr().String(), TotalMemoryMB: 16384,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateServer(ctx, &store.Server{
		ID: pipeServer, Name: "palworld-01", OwnerID: "ann", NodeID: pipeNode,
		State: store.StateRunning, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Power(ctx, pipeServer, agentpb.PowerAction_POWER_ACTION_START); err != nil {
		t.Fatal(err)
	}

	// The device's key pair, and a relay holding its private half.
	priv, pub, err := push.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	panelID, _ := st.PanelID(ctx)
	relay := &stubRelay{t: t, key: priv, panelID: panelID}
	rs := httptest.NewServer(relay)
	t.Cleanup(rs.Close)
	client, err := push.NewClient(rs.URL, panelID, "")
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(&config.Config{Env: "test", SessionTTL: time.Hour}, st, logger,
		WithAlerts(alerts.NewDispatcher(st, client, logger)))
	t.Cleanup(func() { _ = srv.Close() })
	p := &pipeline{t: t, srv: srv, h: srv.Handler(), st: st, rt: rt, roster: roster, relay: relay, grpc: g, token: "ann-token"}

	// Register the phone through the real handler.
	rec := p.do(http.MethodPost, "/api/v1/devices", map[string]any{
		"id": pipeDevice, "platform": "ios", "apns_token": pipeToken, "apns_environment": "sandbox",
		"public_key": base64.StdEncoding.EncodeToString(pub), "name": "Ann's iPhone",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("register device: %d %s", rec.Code, rec.Body.String())
	}
	return p
}

func (p *pipeline) do(method, path string, body any) *httptest.ResponseRecorder {
	p.t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Authorization", "Bearer "+p.token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	p.h.ServeHTTP(rec, req)
	return rec
}

// pass runs one server reconcile pass and waits for the alerts it raised.
func (p *pipeline) pass() {
	p.srv.reconcileOnce(context.Background())
	p.srv.alerts.Wait()
}

func (p *pipeline) command(cmd string) {
	p.t.Helper()
	if err := p.rt.SendCommand(context.Background(), pipeServer, cmd); err != nil {
		p.t.Fatal(err)
	}
}

func (p *pipeline) expect(step string, want ...string) {
	p.t.Helper()
	if got := p.relay.events(); strings.Join(got, ",") != strings.Join(want, ",") {
		p.t.Fatalf("%s: the relay has %v, want %v", step, got, want)
	}
}

func TestPushPipelineEndToEnd(t *testing.T) {
	defer setBackupWatchForTest(5*time.Millisecond, 5*time.Second)()
	p := newPipeline(t)
	p.roster.setRoster("Kestrel")

	// The first pass after a start is a baseline: nothing.
	p.pass()
	p.expect("baseline")

	// A crash: one attend alert, with the exit code as the drill-in reads it.
	// Its players are disconnected; the game comes back empty.
	p.command("crash")
	p.roster.setRoster()
	p.pass()
	p.expect("crash", push.EventServerCrashed)
	if got := p.relay.last(); got.Class != push.ClassAttend || got.Title != "palworld-01" || got.ServerID != pipeServer ||
		got.NodeID != pipeNode || got.Thread != "server:"+pipeServer ||
		got.Body != "palworld-01 stopped unexpectedly — exit 0xC0000135, a DLL the game needs is missing" {
		t.Fatalf("crash payload = %+v", got)
	}
	p.pass()
	p.expect("crash, next pass", push.EventServerCrashed) // a state, not a new event

	// Three watchdog restarts: healed, healed, then the escalation.
	p.command("watchdog")
	p.pass()
	p.expect("first restart", push.EventServerCrashed, push.EventWatchdogRestart)
	if got := p.relay.last(); got.Class != push.ClassHealed || got.Body != "the watchdog restarted palworld-01" {
		t.Fatalf("healed payload = %+v", got)
	}
	p.command("watchdog")
	p.pass()
	p.command("watchdog")
	p.pass()
	p.expect("third restart", push.EventServerCrashed, push.EventWatchdogRestart, push.EventWatchdogRestart, push.EventCrashLoop)
	if got := p.relay.last(); got.Class != push.ClassAttend ||
		got.Body != "palworld-01 is crash-looping: the watchdog has restarted it 3 times in the last hour" {
		t.Fatalf("crash_loop payload = %+v", got)
	}

	// Players come back: one alive alert names them.
	p.roster.setRoster("Kestrel", "Wren")
	p.pass()
	if got := p.relay.last(); got.Event != push.EventPlayerJoined || got.Class != push.ClassAlive ||
		got.Body != "Kestrel and Wren joined palworld-01 · 2 online" {
		t.Fatalf("player payload = %+v", got)
	}

	// A manual backup that lands FAILED after a few polls, through the real
	// handler and the watcher.
	p.rt.SetBackupPending(2)
	p.rt.SetBackupFailure("no space left on device")
	if rec := p.do(http.MethodPost, "/api/v1/servers/"+pipeServer+"/backups", map[string]string{"name": "nightly"}); rec.Code != http.StatusAccepted {
		t.Fatalf("create backup: %d %s", rec.Code, rec.Body.String())
	}
	waitFor(t, func() bool { return len(p.relay.events()) == 6 })
	p.srv.alerts.Wait()
	if got := p.relay.last(); got.Event != push.EventBackupFailed || got.Class != push.ClassAttend ||
		got.Body != "the backup of palworld-01 failed: no space left on device" {
		t.Fatalf("backup payload = %+v", got)
	}

	// The node's Agent goes away: one node_offline, however many passes see it.
	p.srv.ReconcileNodesOnceForTest(context.Background()) // baseline: online
	p.grpc.Stop()
	p.srv.ReconcileNodesOnceForTest(context.Background())
	p.srv.ReconcileNodesOnceForTest(context.Background())
	p.srv.alerts.Wait()
	p.expect("everything", push.EventServerCrashed, push.EventWatchdogRestart, push.EventWatchdogRestart,
		push.EventCrashLoop, push.EventPlayerJoined, push.EventBackupFailed, push.EventNodeOffline)
	if got := p.relay.last(); got.Body != "node abyss-lnx went offline — the panel lost its connection to the agent" ||
		got.NodeID != pipeNode || got.Thread != "node:"+pipeNode {
		t.Fatalf("node payload = %+v", got)
	}

	// Every one went to the device's token, and the device knows it was sent to.
	for _, tok := range p.relay.tokens {
		if tok != pipeToken {
			t.Fatalf("a delivery went to token %q", tok)
		}
	}
	dev, _ := p.st.GetDevice(context.Background(), "ann", pipeDevice)
	if dev.LastSentAt == nil {
		t.Fatal("last_sent_at was not set")
	}
	// One audit entry per alert, none carrying the token.
	sent := 0
	entries, _ := p.st.ListAudit(context.Background(), 500)
	for _, e := range entries {
		if strings.Contains(e.Action, pipeToken) {
			t.Fatalf("an audit entry holds the token: %q", e.Action)
		}
		if strings.HasPrefix(e.Action, alerts.AuditSent+" — ") && e.Actor == alerts.SystemActor && e.TargetID == pipeDevice {
			sent++
		}
	}
	if sent != 7 {
		t.Fatalf("%d push.sent audit entries, want 7", sent)
	}
}

// POST /devices/{id}/test sends to the caller's own device and says how it
// went; without a relay it says so.
func TestDeviceTestEndpoint(t *testing.T) {
	p := newPipeline(t)
	rec := p.do(http.MethodPost, "/api/v1/devices/"+strings.ToUpper(pipeDevice)+"/test", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"outcome":"sent"`) {
		t.Fatalf("test send: %d %s", rec.Code, rec.Body.String())
	}
	if got := p.relay.last(); got.Event != push.EventTest || got.Class != push.ClassAttend || got.Body != "test alert from example.com" {
		t.Fatalf("test payload = %+v", got)
	}
	if rec := p.do(http.MethodPost, "/api/v1/devices/3b241101-e2bb-4255-8caf-4136c566a962/test", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("testing a device the caller does not have: %d", rec.Code)
	}

	// The same Panel without a relay.
	p.srv.alerts = alerts.NewDispatcher(p.st, nil, p.srv.logger)
	rec = p.do(http.MethodPost, "/api/v1/devices/"+pipeDevice+"/test", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"outcome":"disabled"`) {
		t.Fatalf("test send with no relay: %d %s", rec.Code, rec.Body.String())
	}
}

// The backup watcher follows a backup to its end: ready is silence, failed is
// one alert, and one still pending at the deadline is not called failed.
func TestBackupWatcher(t *testing.T) {
	cases := []struct {
		name    string
		pending int
		failure string
		timeout time.Duration
		want    int
	}{
		{"ready", 2, "", 5 * time.Second, 0},
		{"failed", 2, "tar: write error", 5 * time.Second, 1},
		{"timeout", 1 << 20, "tar: write error", 50 * time.Millisecond, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			defer setBackupWatchForTest(5*time.Millisecond, c.timeout)()
			p := newPipeline(t)
			p.rt.SetBackupPending(c.pending)
			p.rt.SetBackupFailure(c.failure)
			sv, _ := p.st.GetServer(context.Background(), pipeServer)
			b, err := p.rt.CreateBackup(context.Background(), pipeServer, "", "scheduled-x", nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			go func() {
				p.srv.followBackup(sv, serverRef(sv), b.GetId(), alerts.BackupScheduled)
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("the watcher did not end")
			}
			p.srv.alerts.Wait()
			if got := len(p.relay.events()); got != c.want {
				t.Fatalf("%d alerts, want %d", got, c.want)
			}
			if c.want == 1 && p.relay.last().Body != "the scheduled backup of palworld-01 failed: tar: write error" {
				t.Fatalf("backup payload = %+v", p.relay.last())
			}
		})
	}
}

// A tunnel-mode node goes offline when its session drops, not when a probe
// fails — the probe afterwards finds it offline already. The fall is caught
// there, once.
func TestTunnelDropIsOneNodeOffline(t *testing.T) {
	p := newPipeline(t)
	ctx := context.Background()
	p.srv.ReconcileNodesOnceForTest(ctx) // baseline: online
	p.grpc.Stop()
	p.srv.onTunnelSession(pipeNode, "", false)
	p.srv.ReconcileNodesOnceForTest(ctx)
	p.srv.alerts.Wait()
	p.expect("tunnel drop", push.EventNodeOffline)
}

// A retire's final backup that failed abandons the retire and is worth an
// alert; one that was skipped never ran, and the retire's note says why.
func TestFinalBackupFailureAlerts(t *testing.T) {
	p := newPipeline(t)
	sv, _ := p.st.GetServer(context.Background(), pipeServer)
	p.srv.noteFinalBackupFailed(sv, finalOutcome{status: store.FinalBackupSkipped, note: "node unreachable"})
	p.srv.noteFinalBackupFailed(sv, finalOutcome{status: store.FinalBackupFailed, note: "tar: write error"})
	p.srv.alerts.Wait()
	p.expect("final backup", push.EventBackupFailed)
	if got := p.relay.last().Body; got != "the final backup of palworld-01 failed, so its retire was abandoned: tar: write error" {
		t.Fatalf("final backup body = %q", got)
	}
}

func setBackupWatchForTest(interval, timeout time.Duration) func() {
	oldInterval, oldTimeout := backupWatchInterval, backupWatchTimeout
	backupWatchInterval = interval
	backupWatchTimeout = func() time.Duration { return timeout }
	return func() { backupWatchInterval, backupWatchTimeout = oldInterval, oldTimeout }
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
