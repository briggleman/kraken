package alerts

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/briggleman/kraken/internal/panel/push"
	"github.com/briggleman/kraken/internal/panel/rbac"
	"github.com/briggleman/kraken/internal/panel/store"
)

// Store is what the dispatcher reads and writes: the devices, the user and role
// behind each, the two device marks a delivery leaves, and the audit log.
type Store interface {
	AuditAppender
	ListDevices(ctx context.Context) ([]*store.Device, error)
	GetUser(ctx context.Context, id string) (*store.User, error)
	GetRole(ctx context.Context, id string) (*rbac.Role, error)
	MarkDeviceTokenInvalid(ctx context.Context, userID, id, token string, at time.Time) (bool, error)
	TouchDeviceSent(ctx context.Context, userID, id string, at time.Time) error
}

// Sender hands one sealed delivery to the relay; *push.Client is one. It
// blocks for as long as its retries take, up to push.MaxAge past the
// delivery's CreatedAt.
type Sender interface {
	Send(ctx context.Context, d push.Delivery) push.Result
}

// The dispatcher's bounds.
const (
	// maxDeliveries is how many deliveries talk to the relay at once. Each
	// one may sit in its retry loop for two minutes while a relay restarts,
	// so the bound is on concurrency, not on queue length: a delivery waiting
	// for a slot keeps its event time, and the relay client drops it as
	// out of time if the wait ate its window.
	maxDeliveries = 32
	// maxPendingEvents is how many events may be in the dispatcher at once
	// before a new one is refused outright. Reaching it means the relay has
	// been failing for minutes with a fleet in trouble; every event past it
	// would only be dropped later anyway.
	maxPendingEvents = 1024
	// testTimeout bounds POST /devices/{id}/test, which answers synchronously:
	// long enough for a retry or two, short enough to answer before a proxy in
	// front of the Panel gives up on the request.
	testTimeout = 20 * time.Second
)

// Dispatcher evaluates each event against every registered device and sends
// the alert to the devices that should get it. A Dispatcher without a Sender
// (no relay configured) accepts events and does nothing with them.
type Dispatcher struct {
	st     Store
	send   Sender
	log    *slog.Logger
	now    func() time.Time
	base   context.Context
	slots  chan struct{}
	events atomic.Int64
	wg     sync.WaitGroup
}

// Option adjusts a Dispatcher.
type Option func(*Dispatcher)

// WithClock replaces the clock for the marks a delivery leaves on a device. The
// default is time.Now in UTC, as every other time the Panel stores is.
func WithClock(now func() time.Time) Option { return func(d *Dispatcher) { d.now = now } }

// WithContext sets the context deliveries run under; cancelling it (the Panel
// shutting down) ends their retries. Default context.Background().
func WithContext(ctx context.Context) Option { return func(d *Dispatcher) { d.base = ctx } }

// NewDispatcher returns a dispatcher that sends through sender. A nil sender
// makes it a no-op: push is off.
func NewDispatcher(st Store, sender Sender, logger *slog.Logger, opts ...Option) *Dispatcher {
	d := &Dispatcher{
		st: st, send: sender, log: logger, now: func() time.Time { return time.Now().UTC() }, base: context.Background(),
		slots: make(chan struct{}, maxDeliveries),
	}
	for _, o := range opts {
		o(d)
	}
	return d
}

// Enabled reports whether a relay is configured.
func (d *Dispatcher) Enabled() bool { return d != nil && d.send != nil }

// Dispatch takes an event and returns at once: the evaluation and the sends
// run on their own goroutines, so a slow relay never holds the reconciler that
// noticed the event.
func (d *Dispatcher) Dispatch(e Event) {
	if !d.Enabled() {
		return
	}
	if d.events.Add(1) > maxPendingEvents {
		d.events.Add(-1)
		d.log.Warn("push: alert refused — too many alerts already waiting on the relay",
			"event", e.Name, "subject", e.subject())
		return
	}
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		defer d.events.Add(-1)
		d.fanOut(e)
	}()
}

// Wait blocks until every event handed to Dispatch so far has been sent,
// refused or dropped. The Panel never needs it; tests do.
func (d *Dispatcher) Wait() {
	if d != nil {
		d.wg.Wait()
	}
}

// audience is one event's view of the users and roles behind the devices.
// It lives for one event only, so a permission or a disable that lands
// between two events applies to the second.
type audience struct {
	st    Store
	ctx   context.Context
	users map[string]*store.User
	roles map[string]*rbac.Role
}

func (a *audience) user(id string) (*store.User, *rbac.Role) {
	u, ok := a.users[id]
	if !ok {
		u, _ = a.st.GetUser(a.ctx, id)
		a.users[id] = u
	}
	if u == nil {
		return nil, nil
	}
	r, ok := a.roles[u.RoleID]
	if !ok {
		r, _ = a.st.GetRole(a.ctx, u.RoleID)
		a.roles[u.RoleID] = r
	}
	return u, r
}

// fanOut decides which devices get the event and sends to each in parallel:
// sequentially, the last device of a long list could see its two minutes run
// out behind the others' retries.
func (d *Dispatcher) fanOut(e Event) {
	ctx := d.base
	devices, err := d.st.ListDevices(ctx)
	if err != nil {
		d.log.Warn("push: could not list devices; the alert is not sent", "event", e.Name, "err", err)
		return
	}
	aud := &audience{st: d.st, ctx: ctx, users: map[string]*store.User{}, roles: map[string]*rbac.Role{}}
	var wg sync.WaitGroup
	for _, dev := range devices {
		user, role := aud.user(dev.UserID)
		if !Allowed(e, dev, user, role) {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case d.slots <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-d.slots }()
			d.deliver(ctx, e, dev, user.Username)
		}()
	}
	wg.Wait()
}

// Allowed is the whole per-device decision, evaluated at send time:
//
//   - a device whose token the relay reported dead gets nothing until it
//     registers again;
//   - a user who is gone or disabled gets nothing, whatever their devices say
//     (a registration can race the disable that revoked it);
//   - the device's own rules: the class toggle, and for alive the mutes;
//   - RBAC: a server event needs server.view and the API's ownership rule
//     (store.MayAccessServer), a node event needs node.view.
func Allowed(e Event, dev *store.Device, user *store.User, role *rbac.Role) bool {
	if dev.TokenInvalidAt != nil || user == nil || role == nil || user.Disabled {
		return false
	}
	switch e.Class {
	case push.ClassAttend:
		if !dev.Rules.Attend {
			return false
		}
	case push.ClassHealed:
		if !dev.Rules.Healed {
			return false
		}
	case push.ClassAlive:
		if !dev.Rules.Alive || slices.Contains(dev.Rules.AliveMutedServers, e.Server.ID) {
			return false
		}
	default:
		return false
	}
	switch {
	case e.serverEvent():
		return role.Has(rbac.PermServerView) && store.MayAccessServer(user, role, e.Server.OwnerID)
	case e.nodeEvent():
		return role.Has(rbac.PermNodeView)
	default:
		return false // the test alert goes through SendTest, to one device
	}
}

// deliver seals the event to one device, sends it, and records the outcome on
// the device and in the audit log.
func (d *Dispatcher) deliver(ctx context.Context, e Event, dev *store.Device, username string) push.Result {
	detail := e.Name + " (" + e.subject() + ") to " + username
	envelope, err := push.Seal(dev.PublicKey, e.Payload())
	if err != nil {
		res := push.Result{Outcome: push.Rejected, Reason: "the alert could not be sealed to the device's key: " + err.Error()}
		d.audit(ctx, AuditFailed, detail+": "+res.Reason, dev, 0)
		return res
	}
	res := d.send.Send(ctx, push.Delivery{
		Token: dev.APNsToken, Environment: dev.APNsEnvironment, Envelope: envelope, CreatedAt: e.At,
	})
	switch res.Outcome {
	case push.Sent:
		if err := d.st.TouchDeviceSent(ctx, dev.UserID, dev.ID, d.now()); err != nil && !errors.Is(err, store.ErrNotFound) {
			d.log.Warn("push: could not record the send on the device", "device", dev.ID, "err", err)
		}
		d.audit(ctx, AuditSent, detail, dev, res.Status)
	case push.TokenDead:
		// Marked with the token that was sent: a phone that registered a new
		// one while this was in flight keeps it.
		if _, err := d.st.MarkDeviceTokenInvalid(ctx, dev.UserID, dev.ID, dev.APNsToken, d.now()); err != nil {
			d.log.Warn("push: could not mark the device's token dead", "device", dev.ID, "err", err)
		}
		d.audit(ctx, AuditFailed, detail+": "+res.Reason, dev, res.Status)
	case push.Rejected:
		d.audit(ctx, AuditFailed, detail+": "+res.Reason, dev, res.Status)
	default:
		d.audit(ctx, AuditDropped, detail+": "+res.Reason, dev, res.Status)
	}
	return res
}

func (d *Dispatcher) audit(ctx context.Context, action, detail string, dev *store.Device, status int) {
	if err := SystemAudit(ctx, d.st, action, detail, "device", dev.ID, status); err != nil {
		d.log.Warn("push: audit append failed", "action", action, "err", err)
	}
}

// TestOutcome is what POST /devices/{id}/test answers.
type TestOutcome struct {
	// Outcome is push.Outcome's name (sent, token_dead, rejected, dropped),
	// or "disabled" when no relay is configured.
	Outcome string `json:"outcome"`
	Reason  string `json:"reason,omitempty"`
}

// Disabled is the test outcome when push is off.
const Disabled = "disabled"

// SendTest sends the test alert to one device and waits for the outcome. It
// goes to the device whatever its rules and its token mark say: it is how a
// person checks the path end to end, and a dead token is exactly what it should
// be able to show. The outcome is recorded like any other delivery's.
func (d *Dispatcher) SendTest(ctx context.Context, dev *store.Device, username, panelHost string) TestOutcome {
	if !d.Enabled() {
		return TestOutcome{Outcome: Disabled, Reason: "no push relay is configured on this panel (KRAKEN_PUSH_RELAY_URL)"}
	}
	ctx, cancel := context.WithTimeout(ctx, testTimeout)
	defer cancel()
	res := d.deliver(ctx, Test(panelHost, d.now()), dev, username)
	return TestOutcome{Outcome: res.Outcome.String(), Reason: res.Reason}
}
