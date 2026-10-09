package alerts

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/briggleman/kraken/internal/panel/push"
	"github.com/briggleman/kraken/internal/panel/rbac"
	"github.com/briggleman/kraken/internal/panel/store"
	"github.com/briggleman/kraken/internal/panel/store/memory"
)

// recordingSender plays the relay: it opens every envelope with the device's
// private key (the test holds them all) and answers with whatever outcome the
// test set for that token.
type recordingSender struct {
	t        *testing.T
	mu       sync.Mutex
	keys     map[string][]byte // token → device private key
	answer   map[string]push.Outcome
	received map[string][]push.Payload // token → opened payloads
}

func (s *recordingSender) Send(_ context.Context, d push.Delivery) push.Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := push.Open(s.keys[d.Token], d.Envelope)
	if err != nil {
		s.t.Errorf("the envelope for %s does not open with its device's key: %v", d.Token, err)
		return push.Result{Outcome: push.Rejected, Reason: "unopenable"}
	}
	s.received[d.Token] = append(s.received[d.Token], p)
	o := s.answer[d.Token]
	return push.Result{Outcome: o, Reason: "relay says " + o.String(), Status: map[push.Outcome]int{
		push.Sent: 200, push.TokenDead: 410, push.Rejected: 400}[o]}
}

func (s *recordingSender) got(token string) []push.Payload {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]push.Payload(nil), s.received[token]...)
}

type fixture struct {
	t    *testing.T
	st   *memory.Store
	send *recordingSender
	d    *Dispatcher
	now  time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	st := memory.New()
	ctx := context.Background()
	for _, r := range rbac.BuiltinRoles() {
		r := r
		if err := st.UpsertRole(ctx, &r); err != nil {
			t.Fatal(err)
		}
	}
	send := &recordingSender{t: t, keys: map[string][]byte{}, answer: map[string]push.Outcome{}, received: map[string][]push.Payload{}}
	f := &fixture{t: t, st: st, send: send, now: t0}
	f.d = NewDispatcher(st, send, slog.New(slog.NewTextHandler(io.Discard, nil)), WithClock(func() time.Time { return f.now }))
	return f
}

func (f *fixture) user(id, role string) {
	f.t.Helper()
	if err := f.st.CreateUser(context.Background(), &store.User{ID: id, Username: id, RoleID: role}); err != nil {
		f.t.Fatal(err)
	}
}

// device registers a device for user and returns its token.
func (f *fixture) device(user, id string, edit func(*store.Device)) string {
	f.t.Helper()
	priv, pub, err := push.GenerateKeyPair()
	if err != nil {
		f.t.Fatal(err)
	}
	token := strings.Repeat(id[:1], 64)
	d := &store.Device{
		ID: id, UserID: user, Platform: store.DevicePlatformIOS, APNsToken: token,
		APNsEnvironment: store.APNsProduction, PublicKey: pub, Rules: store.DefaultDeviceRules(),
		CreatedAt: f.now, LastSeenAt: f.now,
	}
	if edit != nil {
		edit(d)
	}
	if _, err := f.st.UpsertDevice(context.Background(), d); err != nil {
		f.t.Fatal(err)
	}
	f.send.mu.Lock()
	f.send.keys[token] = priv
	f.send.mu.Unlock()
	return token
}

func (f *fixture) dispatch(e Event) {
	f.d.Dispatch(e)
	f.d.Wait()
}

func (f *fixture) audit() []*store.AuditEntry {
	es, _ := f.st.ListAudit(context.Background(), 100)
	return es
}

var dw = ServerRef{ID: "s1", Name: "dragonwilds-01", OwnerID: "bob", NodeID: "n1"}

func TestDispatchClassToggles(t *testing.T) {
	f := newFixture(t)
	f.user("ann", rbac.RoleOwner)
	all := f.device("ann", "a-all", nil)
	noAttend := f.device("ann", "b-noattend", func(d *store.Device) { d.Rules.Attend = false })
	noHealed := f.device("ann", "c-nohealed", func(d *store.Device) { d.Rules.Healed = false })
	noAlive := f.device("ann", "d-noalive", func(d *store.Device) { d.Rules.Alive = false })

	f.dispatch(ServerCrashed(dw, 0, false, t0))
	f.dispatch(WatchdogRestart(dw, t0))
	f.dispatch(PlayersJoined(dw, []string{"Kestrel"}, 1, t0))

	want := map[string][]string{
		all:      {push.EventServerCrashed, push.EventWatchdogRestart, push.EventPlayerJoined},
		noAttend: {push.EventWatchdogRestart, push.EventPlayerJoined},
		noHealed: {push.EventServerCrashed, push.EventPlayerJoined},
		noAlive:  {push.EventServerCrashed, push.EventWatchdogRestart},
	}
	for token, events := range want {
		got := f.send.got(token)
		var names []string
		for _, p := range got {
			names = append(names, p.Event)
		}
		if strings.Join(names, ",") != strings.Join(events, ",") {
			t.Errorf("device %s got %v, want %v", token[:1], names, events)
		}
	}
	// What arrived is the event, intact.
	p := f.send.got(all)[0]
	if p.Class != push.ClassAttend || p.Title != "dragonwilds-01" || p.ServerID != "s1" || p.Body != "dragonwilds-01 stopped unexpectedly" {
		t.Errorf("opened payload = %+v", p)
	}
}

func TestDispatchAliveMutes(t *testing.T) {
	f := newFixture(t)
	f.user("ann", rbac.RoleOwner)
	muted := f.device("ann", "a-muted", func(d *store.Device) { d.Rules.AliveMutedServers = []string{"s1"} })
	f.dispatch(PlayersJoined(dw, []string{"Kestrel"}, 1, t0))
	f.dispatch(ServerCrashed(dw, 0, false, t0))
	other := ServerRef{ID: "s9", Name: "other", OwnerID: "bob"}
	f.dispatch(PlayersJoined(other, []string{"Wren"}, 1, t0))
	var names []string
	for _, p := range f.send.got(muted) {
		names = append(names, p.Event+"@"+p.ServerID)
	}
	// A mute silences that server's players only: its crash still arrives,
	// and another server's players still do.
	if strings.Join(names, ",") != "server_crashed@s1,player_joined@s9" {
		t.Fatalf("muted device got %v", names)
	}
}

// RBAC is read at the moment of sending: a user who loses a server between two
// events stops hearing about it on the second, with no device change.
func TestDispatchRBACAtSendTime(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.user("bob", rbac.RoleOperator) // no server.any: his own servers only
	f.user("carl", rbac.RoleReadOnly)
	bob := f.device("bob", "b-bob", nil)
	carl := f.device("carl", "c-carl", nil)

	f.dispatch(ServerCrashed(dw, 0, false, t0)) // bob owns s1
	if len(f.send.got(bob)) != 1 || len(f.send.got(carl)) != 0 {
		t.Fatalf("owner rule: bob %d, carl %d; want 1 and 0", len(f.send.got(bob)), len(f.send.got(carl)))
	}
	// bob's role loses server.view.
	if err := f.st.UpsertRole(ctx, &rbac.Role{ID: "limited", Name: "Limited", Permissions: []rbac.Permission{rbac.PermNodeView}}); err != nil {
		t.Fatal(err)
	}
	u, _ := f.st.GetUser(ctx, "bob")
	u.RoleID = "limited"
	_ = f.st.UpdateUser(ctx, u)
	f.dispatch(ServerCrashed(dw, 0, false, t0))
	if len(f.send.got(bob)) != 1 {
		t.Fatalf("bob got %d alerts after losing server.view, want still 1", len(f.send.got(bob)))
	}
	// Node events need node.view, which "limited" and Read-only have.
	f.dispatch(NodeOffline("n1", "abyss-win", t0))
	if len(f.send.got(bob)) != 2 || len(f.send.got(carl)) != 1 {
		t.Fatalf("node event: bob %d, carl %d; want 2 and 1", len(f.send.got(bob)), len(f.send.got(carl)))
	}
	f.user("nonode", "nonode")
	_ = f.st.UpsertRole(ctx, &rbac.Role{ID: "nonode", Name: "No nodes", Permissions: []rbac.Permission{rbac.PermServerView, rbac.PermServerAny}})
	nn := f.device("nonode", "n-nonode", nil)
	f.dispatch(NodeOffline("n1", "abyss-win", t0))
	f.dispatch(ServerCrashed(dw, 0, false, t0))
	if got := f.send.got(nn); len(got) != 1 || got[0].Event != push.EventServerCrashed {
		t.Fatalf("a role without node.view got %+v, want only the server event", got)
	}
}

// A disabled user gets nothing, whatever their devices say: a registration can
// race the disable that revoked the rest.
func TestDispatchSkipsDisabledUsers(t *testing.T) {
	f := newFixture(t)
	f.user("ann", rbac.RoleOwner)
	tok := f.device("ann", "a-ann", nil)
	u, _ := f.st.GetUser(context.Background(), "ann")
	u.Disabled = true
	_ = f.st.UpdateUser(context.Background(), u)
	f.dispatch(NodeOffline("n1", "abyss-win", t0))
	if len(f.send.got(tok)) != 0 {
		t.Fatal("a disabled user's device was sent an alert")
	}
}

func TestDispatchSkipsADeadToken(t *testing.T) {
	f := newFixture(t)
	f.user("ann", rbac.RoleOwner)
	tok := f.device("ann", "a-ann", nil)
	// Marked the way the relay's 410 marks it.
	if ok, _ := f.st.MarkDeviceTokenInvalid(context.Background(), "ann", "a-ann", tok, t0); !ok {
		t.Fatal("could not mark the token")
	}
	f.dispatch(NodeOffline("n1", "abyss-win", t0))
	if len(f.send.got(tok)) != 0 {
		t.Fatal("a device whose token is dead was sent an alert")
	}
}

func TestDispatchOutcomesMarkTheDeviceAndTheAudit(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.user("ann", rbac.RoleOwner)
	ok := f.device("ann", "a-ok", nil)
	dead := f.device("ann", "b-dead", nil)
	f.send.answer[dead] = push.TokenDead
	f.now = t0.Add(time.Minute)

	f.dispatch(NodeOffline("n1", "abyss-win", t0))

	a, _ := f.st.GetDevice(ctx, "ann", "a-ok")
	if a.LastSentAt == nil || !a.LastSentAt.Equal(f.now) || a.TokenInvalidAt != nil {
		t.Fatalf("sent device = last_sent %v token_invalid %v", a.LastSentAt, a.TokenInvalidAt)
	}
	b, _ := f.st.GetDevice(ctx, "ann", "b-dead")
	if b.TokenInvalidAt == nil || b.LastSentAt != nil {
		t.Fatalf("dead device = last_sent %v token_invalid %v", b.LastSentAt, b.TokenInvalidAt)
	}
	// The next event skips it.
	f.dispatch(NodePartial("n1", "abyss-win", t0))
	if len(f.send.got(dead)) != 1 || len(f.send.got(ok)) != 2 {
		t.Fatalf("after the 410: dead %d, ok %d; want 1 and 2", len(f.send.got(dead)), len(f.send.got(ok)))
	}

	var sent, failed int
	for _, e := range f.audit() {
		if e.Actor != SystemActor || e.TargetType != "device" || e.Method != "" || e.Path != "" || e.IP != "" {
			t.Errorf("audit entry shape: %+v", e)
		}
		if strings.Contains(e.Action, ok) || strings.Contains(e.Action, dead) {
			t.Errorf("an audit entry holds a token: %q", e.Action)
		}
		switch {
		case strings.HasPrefix(e.Action, AuditSent+" — node_offline (node abyss-win) to ann") && e.TargetID == "a-ok":
			sent++
		case strings.HasPrefix(e.Action, AuditFailed+" — node_offline (node abyss-win) to ann: relay says token_dead") && e.TargetID == "b-dead":
			failed++
		}
	}
	if sent != 1 || failed != 1 {
		t.Fatalf("audit: %d sent, %d failed; want 1 each: %+v", sent, failed, f.audit())
	}
}

// A 410 marks the token that was sent. A phone that registered a fresh token
// while the alert was in flight keeps it.
func TestDispatchTokenDeadMarksOnlyTheSentToken(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.user("ann", rbac.RoleOwner)
	f.device("ann", "a-ann", nil)
	dev, _ := f.st.GetDevice(ctx, "ann", "a-ann")
	// Between the send and the answer the phone re-registers a new token.
	dev2 := *dev
	dev2.APNsToken = strings.Repeat("f", 64)
	_, _ = f.st.UpsertDevice(ctx, &dev2)
	f.send.keys[dev.APNsToken] = f.send.keys[strings.Repeat("a", 64)]
	f.send.answer[dev.APNsToken] = push.TokenDead
	f.d.deliver(ctx, NodeOffline("n1", "x", t0), dev, "ann")
	after, _ := f.st.GetDevice(ctx, "ann", "a-ann")
	if after.TokenInvalidAt != nil {
		t.Fatal("a 410 for the old token silenced the device's new one")
	}
}

// The marks a delivery leaves on a device are UTC, like every other time the
// Panel stores. (Found in the phase 5 drill: token_invalid_at read
// 13:48:08-04:00 beside a created_at in Z.)
func TestDispatchStampsTheDeviceInUTC(t *testing.T) {
	f := newFixture(t)
	f.d = NewDispatcher(f.st, f.send, slog.New(slog.NewTextHandler(io.Discard, nil)))
	f.user("ann", rbac.RoleOwner)
	f.device("ann", "a-ok", nil)
	dead := f.device("ann", "b-dead", nil)
	f.send.answer[dead] = push.TokenDead
	f.dispatch(NodeOffline("n1", "abyss-win", t0))
	ok, _ := f.st.GetDevice(context.Background(), "ann", "a-ok")
	gone, _ := f.st.GetDevice(context.Background(), "ann", "b-dead")
	if ok.LastSentAt == nil || ok.LastSentAt.Location() != time.UTC {
		t.Fatalf("last_sent_at = %v, want UTC", ok.LastSentAt)
	}
	if gone.TokenInvalidAt == nil || gone.TokenInvalidAt.Location() != time.UTC {
		t.Fatalf("token_invalid_at = %v, want UTC", gone.TokenInvalidAt)
	}
}

func TestDispatchDisabledDoesNothing(t *testing.T) {
	st := memory.New()
	d := NewDispatcher(st, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if d.Enabled() {
		t.Fatal("a dispatcher with no sender says it is enabled")
	}
	d.Dispatch(NodeOffline("n1", "x", t0))
	d.Wait()
	if got := d.SendTest(context.Background(), &store.Device{}, "ann", "h"); got.Outcome != Disabled {
		t.Fatalf("SendTest with no relay = %+v", got)
	}
}

func TestSendTestReachesTheDeviceWhateverItsRules(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.user("ann", rbac.RoleOwner)
	tok := f.device("ann", "a-ann", func(d *store.Device) { d.Rules.Attend = false })
	dev, _ := f.st.GetDevice(ctx, "ann", "a-ann")
	got := f.d.SendTest(ctx, dev, "ann", "kraken.example.com")
	if got.Outcome != "sent" {
		t.Fatalf("test outcome = %+v", got)
	}
	if p := f.send.got(tok); len(p) != 1 || p[0].Event != push.EventTest || p[0].Body != "test alert from kraken.example.com" {
		t.Fatalf("test payload = %+v", p)
	}
}
