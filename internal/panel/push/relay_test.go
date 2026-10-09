package push

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/briggleman/kraken/internal/shared/version"
)

// fakeTime is a clock that only moves when the client sleeps, so a two-minute
// retry schedule runs in microseconds and the waits it asked for are recorded
// exactly.
type fakeTime struct {
	mu    sync.Mutex
	t     time.Time
	waits []time.Duration
}

func newFakeTime() *fakeTime { return &fakeTime{t: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)} }

func (f *fakeTime) now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}

func (f *fakeTime) sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.t = f.t.Add(d)
	f.waits = append(f.waits, d)
	return nil
}

func (f *fakeTime) recorded() []time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Duration(nil), f.waits...)
}

const testToken = "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90"

func delivery(created time.Time) Delivery {
	return Delivery{Token: testToken, Environment: EnvironmentProduction, Envelope: "ZW52ZWxvcGU=", CreatedAt: created}
}

// relay is an httptest relay that answers each request with the next status
// in its script, repeating the last one once the script runs out.
type relay struct {
	*httptest.Server
	hits    atomic.Int32
	mu      sync.Mutex
	headers []http.Header
	bodies  [][]byte
	paths   []string
}

func newRelay(t *testing.T, script ...int) *relay {
	t.Helper()
	r := &relay{}
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		n := int(r.hits.Add(1)) - 1
		body, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.headers = append(r.headers, req.Header.Clone())
		r.bodies = append(r.bodies, body)
		r.paths = append(r.paths, req.Method+" "+req.URL.RequestURI())
		r.mu.Unlock()
		w.WriteHeader(script[min(n, len(script)-1)])
	}))
	t.Cleanup(r.Close)
	return r
}

// The accessors take the lock because the race detector cannot see the
// ordering a socket gives the handler's writes and the test's reads.
func (r *relay) path(i int) string        { r.mu.Lock(); defer r.mu.Unlock(); return r.paths[i] }
func (r *relay) header(i int) http.Header { r.mu.Lock(); defer r.mu.Unlock(); return r.headers[i] }
func (r *relay) body(i int) []byte        { r.mu.Lock(); defer r.mu.Unlock(); return r.bodies[i] }

func newTestClient(t *testing.T, base, relayToken string, ft *fakeTime) *Client {
	t.Helper()
	c, err := NewClient(base, "panel-install-1", relayToken, WithClock(ft.now), WithSleep(ft.sleep))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestSendSendsTheContractRequest(t *testing.T) {
	r := newRelay(t, http.StatusAccepted)
	ft := newFakeTime()
	res := newTestClient(t, r.URL, "relay-secret", ft).Send(context.Background(), delivery(ft.now()))
	if res.Outcome != Sent || res.Attempts != 1 || res.Status != http.StatusAccepted {
		t.Fatalf("got %+v, want one accepted attempt", res)
	}
	if r.path(0) != "POST /v1/push" {
		t.Fatalf("request line %q, want POST /v1/push", r.path(0))
	}
	h := r.header(0)
	for name, want := range map[string]string{
		"Content-Type":      "application/json",
		"User-Agent":        "kraken-panel/" + version.Version,
		"X-Kraken-Panel-Id": "panel-install-1",
		"Authorization":     "Bearer relay-secret",
	} {
		if got := h.Get(name); got != want {
			t.Errorf("%s: %q, want %q", name, got, want)
		}
	}
	var body map[string]string
	if err := json.Unmarshal(r.body(0), &body); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"token": testToken, "environment": "production", "payload": "ZW52ZWxvcGU="}
	if !reflect.DeepEqual(body, want) {
		t.Fatalf("body %v, want exactly %v", body, want)
	}
}

func TestSendWithoutARelayTokenSendsNoAuthorization(t *testing.T) {
	r := newRelay(t, http.StatusOK)
	ft := newFakeTime()
	newTestClient(t, r.URL, "", ft).Send(context.Background(), delivery(ft.now()))
	if _, ok := r.header(0)["Authorization"]; ok {
		t.Fatalf("Authorization sent without a relay token: %q", r.header(0).Get("Authorization"))
	}
}

func TestSendKeepsABasePathAndQuery(t *testing.T) {
	r := newRelay(t, http.StatusOK)
	ft := newFakeTime()
	newTestClient(t, r.URL+"/kraken/?answer=503", "", ft).Send(context.Background(), delivery(ft.now()))
	if r.path(0) != "POST /kraken/v1/push?answer=503" {
		t.Fatalf("request line %q", r.path(0))
	}
}

func TestSendTokenDeadIsNotRetried(t *testing.T) {
	r := newRelay(t, http.StatusGone)
	ft := newFakeTime()
	res := newTestClient(t, r.URL, "", ft).Send(context.Background(), delivery(ft.now()))
	if res.Outcome != TokenDead || res.Attempts != 1 || r.hits.Load() != 1 {
		t.Fatalf("got %+v after %d hits, want TokenDead after one", res, r.hits.Load())
	}
}

func TestSendRefusalsAreNotRetried(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 413, 422} {
		r := newRelay(t, status)
		ft := newFakeTime()
		res := newTestClient(t, r.URL, "", ft).Send(context.Background(), delivery(ft.now()))
		if res.Outcome != Rejected || res.Attempts != 1 || res.Status != status || r.hits.Load() != 1 {
			t.Errorf("%d: got %+v after %d hits, want Rejected after one", status, res, r.hits.Load())
		}
		if len(ft.recorded()) != 0 {
			t.Errorf("%d: waited %v before refusing", status, ft.recorded())
		}
	}
}

func TestSendRetriesTransientFailuresUntilSent(t *testing.T) {
	for _, status := range []int{408, 429, 500, 502, 503, 504} {
		r := newRelay(t, status, status, http.StatusOK)
		ft := newFakeTime()
		res := newTestClient(t, r.URL, "", ft).Send(context.Background(), delivery(ft.now()))
		if res.Outcome != Sent || res.Attempts != 3 {
			t.Errorf("%d: got %+v, want Sent on the third attempt", status, res)
		}
		if got, want := ft.recorded(), []time.Duration{time.Second, 3 * time.Second}; !reflect.DeepEqual(got, want) {
			t.Errorf("%d: waited %v, want %v", status, got, want)
		}
	}
}

// The full schedule against a relay that never recovers: 1s, 3s, 9s, 27s,
// then 30s, attempts at 0, 1, 4, 13, 40, 70 and 100 seconds, and a drop at
// 100s because the next attempt (130s) would be past two minutes.
func TestSendBackoffScheduleAndTheTwoMinuteDrop(t *testing.T) {
	r := newRelay(t, http.StatusServiceUnavailable)
	ft := newFakeTime()
	start := ft.now()
	res := newTestClient(t, r.URL, "", ft).Send(context.Background(), delivery(start))
	if res.Outcome != Dropped || res.Attempts != 7 || res.Status != 503 {
		t.Fatalf("got %+v, want Dropped after 7 attempts", res)
	}
	want := []time.Duration{1 * time.Second, 3 * time.Second, 9 * time.Second, 27 * time.Second, 30 * time.Second, 30 * time.Second}
	if got := ft.recorded(); !reflect.DeepEqual(got, want) {
		t.Fatalf("waited %v, want %v", got, want)
	}
	if elapsed := ft.now().Sub(start); elapsed >= MaxAge {
		t.Fatalf("dropped at %v, after the window had already closed", elapsed)
	}
	if !strings.Contains(res.Reason, "503") {
		t.Fatalf("the drop reason does not say what kept failing: %q", res.Reason)
	}
}

// The window runs from CreatedAt, so a delivery that waited in a queue gets
// only what is left of its two minutes.
func TestSendMeasuresAgeFromCreatedAt(t *testing.T) {
	r := newRelay(t, http.StatusServiceUnavailable)
	ft := newFakeTime()
	res := newTestClient(t, r.URL, "", ft).Send(context.Background(), delivery(ft.now().Add(-115*time.Second)))
	// Attempts at 115s, 116s and 119s; the next would be at 128s.
	if res.Outcome != Dropped || res.Attempts != 3 {
		t.Fatalf("got %+v, want Dropped after 3 attempts", res)
	}

	stale := newRelay(t, http.StatusOK)
	res = newTestClient(t, stale.URL, "", ft).Send(context.Background(), delivery(ft.now().Add(-MaxAge)))
	if res.Outcome != Dropped || res.Attempts != 0 || stale.hits.Load() != 0 {
		t.Fatalf("a delivery already two minutes old was attempted: %+v", res)
	}
}

func TestSendRetriesConnectionErrors(t *testing.T) {
	// A port that was listening a moment ago and is not now.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	ft := newFakeTime()
	res := newTestClient(t, "http://"+addr, "", ft).Send(context.Background(), delivery(ft.now()))
	if res.Outcome != Dropped || res.Attempts != 7 || res.Status != 0 {
		t.Fatalf("got %+v, want Dropped after 7 unanswered attempts", res)
	}
	if !strings.Contains(res.Reason, "could not reach the relay") {
		t.Fatalf("reason %q", res.Reason)
	}
}

// A relay that hangs costs one per-request timeout and is retried. The
// timeout is capped by what is left of the window, which is how this test
// reaches it without waiting the full ten seconds.
func TestSendTimesOutAHungRelay(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(func() { close(release); srv.Close() })
	ft := newFakeTime()
	res := newTestClient(t, srv.URL, "", ft).Send(context.Background(), delivery(ft.now().Add(-(MaxAge - 50*time.Millisecond))))
	if res.Outcome != Dropped || res.Attempts != 1 || !strings.Contains(res.Reason, "did not answer within") {
		t.Fatalf("got %+v, want a timeout and then a drop", res)
	}
}

func TestSendHonoursCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The relay fails once and the caller gives up while the client waits
	// to retry.
	r := newRelay(t, http.StatusServiceUnavailable)
	ft := newFakeTime()
	sleep := func(ctx context.Context, d time.Duration) error {
		cancel()
		<-ctx.Done()
		return ctx.Err()
	}
	c, _ := NewClient(r.URL, "p", "", WithClock(ft.now), WithSleep(sleep))
	res := c.Send(ctx, delivery(ft.now()))
	if res.Outcome != Dropped || res.Attempts != 1 || !strings.Contains(res.Reason, "cancelled") {
		t.Fatalf("got %+v, want Dropped as cancelled after one attempt", res)
	}

	// Already cancelled: the one attempt fails at once and nothing is retried.
	r2 := newRelay(t, http.StatusOK)
	done, cancel2 := context.WithCancel(context.Background())
	cancel2()
	res = newTestClient(t, r2.URL, "", ft).Send(done, delivery(ft.now()))
	if res.Outcome != Dropped || !strings.Contains(res.Reason, "cancelled") || r2.hits.Load() != 0 {
		t.Fatalf("got %+v with %d hits, want a cancelled drop and no request", res, r2.hits.Load())
	}
}

func TestSendDoesNotFollowRedirects(t *testing.T) {
	target := newRelay(t, http.StatusOK)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/v1/push", http.StatusPermanentRedirect)
	}))
	t.Cleanup(srv.Close)
	ft := newFakeTime()
	res := newTestClient(t, srv.URL, "", ft).Send(context.Background(), delivery(ft.now()))
	if res.Outcome != Rejected || res.Status != http.StatusPermanentRedirect || target.hits.Load() != 0 {
		t.Fatalf("got %+v with %d hits on the target, want a refused redirect", res, target.hits.Load())
	}
}

// A relay that echoes the request back in its error must not get the device
// token or the envelope into the reason, which the audit log keeps.
func TestSendReasonNeverCarriesTheTokenOrEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("bad request:\n" + string(body)))
	}))
	t.Cleanup(srv.Close)
	ft := newFakeTime()
	d := delivery(ft.now())
	res := newTestClient(t, srv.URL, "", ft).Send(context.Background(), d)
	if res.Outcome != Rejected {
		t.Fatalf("got %+v", res)
	}
	if strings.Contains(res.Reason, d.Token) || strings.Contains(res.Reason, d.Envelope) || strings.Contains(res.Reason, "\n") {
		t.Fatalf("reason leaks the request: %q", res.Reason)
	}
	if !strings.Contains(res.Reason, "[redacted]") {
		t.Fatalf("reason %q should show the relay's message, redacted", res.Reason)
	}
}

func TestSendRejectsAMalformedDeliveryWithoutARequest(t *testing.T) {
	r := newRelay(t, http.StatusOK)
	ft := newFakeTime()
	c := newTestClient(t, r.URL, "", ft)
	for name, d := range map[string]Delivery{
		"no token":    {Environment: EnvironmentSandbox, Envelope: "x", CreatedAt: ft.now()},
		"no envelope": {Token: "t", Environment: EnvironmentSandbox, CreatedAt: ft.now()},
		"bad env":     {Token: "t", Environment: "staging", Envelope: "x", CreatedAt: ft.now()},
	} {
		if res := c.Send(context.Background(), d); res.Outcome != Rejected || res.Attempts != 0 {
			t.Errorf("%s: got %+v", name, res)
		}
	}
	if r.hits.Load() != 0 {
		t.Fatalf("a malformed delivery reached the relay")
	}
}

func TestNewClientValidates(t *testing.T) {
	for _, bad := range []string{"", "relay.example", "ftp://relay.example", "https://", "://x"} {
		if _, err := NewClient(bad, "p", ""); err == nil {
			t.Errorf("NewClient accepted %q", bad)
		}
	}
	if _, err := NewClient("https://relay.example", " ", ""); err == nil {
		t.Error("NewClient accepted an empty Panel id")
	}
}
