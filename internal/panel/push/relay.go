package push

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/briggleman/kraken/internal/shared/version"
)

// MaxAge is how old a delivery may get before it is dropped instead of sent.
// An alert that arrives late is worse than one that honestly didn't: "your
// node is offline" ten minutes after it came back is noise that teaches the
// operator to ignore the next one.
const MaxAge = 2 * time.Minute

// RequestTimeout bounds one POST to the relay. It is generous for one small
// JSON body, and short enough that a hung relay costs a retry rather than the
// whole two-minute window.
const RequestTimeout = 10 * time.Second

// backoff is the wait after each failed attempt, in order; once it runs out
// every further wait is backoffTail. With a 30-second tail the two-minute
// window allows seven attempts at most, which is plenty for a relay that is
// restarting and not so many that a relay in trouble is hammered.
var backoff = []time.Duration{1 * time.Second, 3 * time.Second, 9 * time.Second, 27 * time.Second}

const backoffTail = 30 * time.Second

// Environment values for Delivery.Environment: which APNs gateway the token
// belongs to. A development build of the app registers a sandbox token.
const (
	EnvironmentProduction = "production"
	EnvironmentSandbox    = "sandbox"
)

// Delivery is one sealed alert for one device.
type Delivery struct {
	// Token is the device's APNs token, hex, as it registered.
	Token string
	// Environment is EnvironmentProduction or EnvironmentSandbox.
	Environment string
	// Envelope is what Seal returned for this device.
	Envelope string
	// CreatedAt is when the alert was made. The two-minute window runs from
	// here, not from when Send was called, so a delivery that sat in a queue
	// is not handed a fresh two minutes it never had.
	CreatedAt time.Time
}

// Outcome is how a delivery ended. Each one maps onto an audit action in the
// design doc: Sent → push.sent, Rejected → push.failed, Dropped →
// push.dropped; TokenDead is a failure that also retires the device's token.
type Outcome int

const (
	// Sent means the relay accepted the alert with a 2xx.
	Sent Outcome = iota
	// TokenDead means the relay answered 410: APNs no longer knows the token,
	// so the device should get nothing more until it registers again.
	TokenDead
	// Rejected means the relay refused the request in a way a retry cannot
	// fix: a 400 or another 4xx, a redirect, or a delivery that was malformed
	// before it left.
	Rejected
	// Dropped means the alert ran out of time — every attempt met a retryable
	// failure until the two-minute window closed — or the caller cancelled.
	Dropped
)

func (o Outcome) String() string {
	switch o {
	case Sent:
		return "sent"
	case TokenDead:
		return "token_dead"
	case Rejected:
		return "rejected"
	case Dropped:
		return "dropped"
	default:
		return fmt.Sprintf("outcome(%d)", int(o))
	}
}

// Result is what Send reports. Reason is a sentence fit for the audit log; it
// never contains the token or the envelope.
type Result struct {
	Outcome Outcome
	Reason  string
	// Status is the HTTP status of the last response, or 0 when the last
	// attempt got no response at all (or no attempt was made).
	Status int
	// Attempts is how many requests were made.
	Attempts int
}

// Client sends deliveries to one relay. It is safe for concurrent use; each
// Send is independent and does its own retrying.
type Client struct {
	endpoint   string
	panelID    string
	relayToken string
	http       *http.Client
	now        func() time.Time
	sleep      func(context.Context, time.Duration) error
	log        *slog.Logger
}

// Option adjusts a Client. The clock and sleep options exist so tests can run
// a two-minute retry schedule in no time at all.
type Option func(*Client)

// WithHTTPClient replaces the HTTP client. The default one refuses redirects
// (see NewClient); a replacement is used exactly as given.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// WithClock replaces time.Now for the age check.
func WithClock(now func() time.Time) Option { return func(c *Client) { c.now = now } }

// WithSleep replaces the wait between attempts. It must return early with the
// context's error when the context ends.
func WithSleep(sleep func(context.Context, time.Duration) error) Option {
	return func(c *Client) { c.sleep = sleep }
}

// WithLogger sets where retry diagnostics go (Debug level). Default slog.Default().
func WithLogger(l *slog.Logger) Option { return func(c *Client) { c.log = l } }

// NewClient builds a client for the relay at baseURL (KRAKEN_PUSH_RELAY_URL).
// panelID is the Panel's install id, sent on every request so a relay can tell
// Panels apart; relayToken is KRAKEN_PUSH_RELAY_TOKEN and may be empty, in
// which case no Authorization header is sent at all.
func NewClient(baseURL, panelID, relayToken string, opts ...Option) (*Client, error) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("push: relay URL %q is not an absolute http(s) URL", baseURL)
	}
	if strings.TrimSpace(panelID) == "" {
		return nil, errors.New("push: the Panel install id is empty")
	}
	c := &Client{
		// JoinPath keeps a path prefix and a query string on the base, so a
		// relay mounted under /kraken, or the stub's ?answer=503, both work.
		endpoint:   u.JoinPath("v1", "push").String(),
		panelID:    panelID,
		relayToken: strings.TrimSpace(relayToken),
		http: &http.Client{
			// A redirect is refused rather than followed. Following one would
			// turn the POST into a GET on a 301 or 302 and carry the Panel id
			// to whatever host the redirect names; a relay that has moved
			// should be configured at its new address, and the Rejected reason
			// says where it pointed.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		now:   time.Now,
		sleep: sleepCtx,
		log:   slog.Default(),
	}
	for _, o := range opts {
		o(c)
	}
	return c, nil
}

// relayRequest is the body the relay reads.
type relayRequest struct {
	Token       string `json:"token"`
	Environment string `json:"environment"`
	Payload     string `json:"payload"`
}

// Send delivers d, retrying what the contract says to retry, and returns how
// it ended. It blocks for as long as that takes — up to MaxAge past
// d.CreatedAt — so the caller runs it off whatever path noticed the event.
func (c *Client) Send(ctx context.Context, d Delivery) Result {
	if d.Token == "" || d.Envelope == "" {
		return Result{Outcome: Rejected, Reason: "the delivery has no device token or no envelope"}
	}
	if d.Environment != EnvironmentProduction && d.Environment != EnvironmentSandbox {
		return Result{Outcome: Rejected, Reason: fmt.Sprintf("the device's APNs environment %q is neither production nor sandbox", d.Environment)}
	}
	body, err := json.Marshal(relayRequest{Token: d.Token, Environment: d.Environment, Payload: d.Envelope})
	if err != nil {
		return Result{Outcome: Rejected, Reason: "could not encode the relay request"}
	}

	res := Result{}
	lastFailure := "no attempt was made"
	for {
		if err := ctx.Err(); err != nil {
			res.Outcome = Dropped
			res.Reason = fmt.Sprintf("the send was cancelled after %d attempt(s): %v", res.Attempts, err)
			return res
		}
		remaining := MaxAge - c.now().Sub(d.CreatedAt)
		if remaining <= 0 {
			res.Outcome = Dropped
			res.Reason = fmt.Sprintf("the alert reached two minutes old before the relay took it (%s)", lastFailure)
			return res
		}

		res.Attempts++
		status, reason, retry := c.attempt(ctx, body, d, min(RequestTimeout, remaining))
		res.Status = status
		if ctx.Err() != nil {
			res.Outcome = Dropped
			res.Reason = fmt.Sprintf("the send was cancelled after %d attempt(s): %v", res.Attempts, ctx.Err())
			return res
		}
		switch {
		case status >= 200 && status < 300:
			res.Outcome = Sent
			res.Reason = fmt.Sprintf("the relay accepted it (%d)", status)
			return res
		case status == http.StatusGone:
			res.Outcome = TokenDead
			res.Reason = "the relay reports the device token is no longer valid (410)"
			return res
		case !retry:
			res.Outcome = Rejected
			res.Reason = reason
			return res
		}
		lastFailure = reason

		wait := backoffTail
		if res.Attempts <= len(backoff) {
			wait = backoff[res.Attempts-1]
		}
		// If the next attempt would land past the window there is nothing to
		// wait for: drop now, so the audit entry says so two minutes sooner
		// than a sleep that ends in the same place.
		if c.now().Add(wait).Sub(d.CreatedAt) >= MaxAge {
			res.Outcome = Dropped
			res.Reason = fmt.Sprintf("the alert would be two minutes old before another attempt, after %d attempt(s) (%s)", res.Attempts, reason)
			return res
		}
		c.log.Debug("push relay: retrying", "attempt", res.Attempts, "status", status, "reason", reason, "wait", wait)
		if err := c.sleep(ctx, wait); err != nil {
			res.Outcome = Dropped
			res.Reason = fmt.Sprintf("the send was cancelled after %d attempt(s): %v", res.Attempts, err)
			return res
		}
	}
}

// attempt makes one POST. It returns the status (0 for no response), a reason
// sentence for a failure, and whether that failure is worth another try.
func (c *Client) attempt(ctx context.Context, body []byte, d Delivery, timeout time.Duration) (status int, reason string, retry bool) {
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, "could not build the relay request", false
	}
	req.Header.Set("Content-Type", "application/json")
	// Not decoration: Cloudflare in front of a relay refuses Go's default
	// agent with 403 error code 1010.
	req.Header.Set("User-Agent", "kraken-panel/"+version.Version)
	req.Header.Set("X-Kraken-Panel-Id", c.panelID)
	if c.relayToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.relayToken)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		// The error text from net/http quotes the URL, which is the
		// operator's own configuration and safe to repeat; the body with the
		// token and envelope is never part of it.
		if errors.Is(rctx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			return 0, fmt.Sprintf("the relay did not answer within %s", timeout), true
		}
		return 0, fmt.Sprintf("could not reach the relay: %v", err), true
	}
	defer func() { _ = resp.Body.Close() }()
	// Read a little of the body for the reason, then drain the rest so the
	// connection can be reused for the next alert.
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))

	s := resp.StatusCode
	switch {
	case s >= 200 && s < 300, s == http.StatusGone:
		return s, "", false
	case s == http.StatusRequestTimeout, s == http.StatusTooManyRequests, s >= 500:
		return s, fmt.Sprintf("the relay answered %d%s", s, relayMessage(snippet, d)), true
	case s >= 300 && s < 400:
		return s, fmt.Sprintf("the relay answered %d, a redirect to %q; set KRAKEN_PUSH_RELAY_URL to the address it points at", s, resp.Header.Get("Location")), false
	default:
		return s, fmt.Sprintf("the relay refused the request with %d%s", s, relayMessage(snippet, d)), false
	}
}

// relayMessage turns the start of a relay's error body into something safe to
// put in an audit row: one line, printable, short, and with the device token
// and envelope blanked out in case the relay echoed the request back.
func relayMessage(b []byte, d Delivery) string {
	msg := string(b)
	for _, secret := range []string{d.Token, d.Envelope} {
		if secret != "" {
			msg = strings.ReplaceAll(msg, secret, "[redacted]")
		}
	}
	msg = strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) {
			return r
		}
		return ' '
	}, msg)
	msg = strings.Join(strings.Fields(msg), " ")
	if msg == "" {
		return ""
	}
	if r := []rune(msg); len(r) > 160 {
		msg = string(r[:160]) + "…"
	}
	return ": " + msg
}

// sleepCtx waits d or until ctx ends, whichever is first.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
