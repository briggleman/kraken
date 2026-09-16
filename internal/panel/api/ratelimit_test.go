package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// fixedClock drives the limiter's window without sleeping through it.
type fixedClock struct{ t time.Time }

func (c *fixedClock) now() time.Time      { return c.t }
func (c *fixedClock) add(d time.Duration) { c.t = c.t.Add(d) }

func testLimiter(perMinute float64, burst int) (*rateLimiter, *fixedClock) {
	clk := &fixedClock{t: time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)}
	l := newRateLimiter(perMinute, burst)
	l.now = clk.now
	return l, clk
}

// The burst is what a real client uses — a page that fires a few downloads in a
// row — so it is admitted in full, and the request after it is not.
func TestRateLimiterAdmitsTheBurstThenRefuses(t *testing.T) {
	l, _ := testLimiter(30, 10)
	for i := range 10 {
		if ok, _ := l.allow("198.51.100.7"); !ok {
			t.Fatalf("request %d of the burst was refused", i+1)
		}
	}
	ok, retry := l.allow("198.51.100.7")
	if ok {
		t.Fatal("the request past the burst was admitted")
	}
	if retry <= 0 {
		t.Fatalf("Retry-After delay = %s, want a positive wait the caller can act on", retry)
	}
}

// A refusal is a pause, not a ban: once the bucket has refilled the same client
// is served again. And a refused request must not consume future capacity, or a
// client that kept knocking would never get back in.
func TestRateLimiterRecoversAfterTheWindow(t *testing.T) {
	l, clk := testLimiter(30, 2) // one token every two seconds
	for range 2 {
		if ok, _ := l.allow("198.51.100.8"); !ok {
			t.Fatal("the burst was refused")
		}
	}
	if ok, _ := l.allow("198.51.100.8"); ok {
		t.Fatal("admitted past the burst")
	}
	// Keep knocking while refused — this must not push recovery further out.
	for range 5 {
		_, _ = l.allow("198.51.100.8")
	}
	clk.add(2 * time.Second)
	if ok, _ := l.allow("198.51.100.8"); !ok {
		t.Fatal("still refused after the bucket had refilled")
	}
}

// One noisy address must not cost everyone else their downloads.
func TestRateLimiterIsolatesClients(t *testing.T) {
	l, _ := testLimiter(30, 3)
	for range 3 {
		if ok, _ := l.allow("198.51.100.9"); !ok {
			t.Fatal("the burst was refused")
		}
	}
	if ok, _ := l.allow("198.51.100.9"); ok {
		t.Fatal("admitted past the burst")
	}
	if ok, _ := l.allow("203.0.113.4"); !ok {
		t.Fatal("a different client was refused for someone else's traffic")
	}
}

// The table is keyed by attacker-chosen input, so it is swept: entries nobody
// has used in a while go away rather than accumulating for the life of the
// process.
func TestRateLimiterSweepsIdleEntries(t *testing.T) {
	l, clk := testLimiter(30, 3)
	for _, ip := range []string{"198.51.100.1", "198.51.100.2", "198.51.100.3"} {
		if ok, _ := l.allow(ip); !ok {
			t.Fatalf("%s was refused on its first request", ip)
		}
	}
	if n := l.size(); n != 3 {
		t.Fatalf("table holds %d entries, want 3", n)
	}
	clk.add(rateLimiterIdleTTL + rateLimiterSweepEvery)
	if ok, _ := l.allow("203.0.113.9"); !ok {
		t.Fatal("a fresh client was refused")
	}
	if n := l.size(); n != 1 {
		t.Fatalf("table holds %d entries after the sweep, want just the live one", n)
	}
}

// A refusal answers in the shape every other refusal does — the JSON error
// envelope — plus the Retry-After a client needs to behave.
func TestRateLimiterMiddlewareAnswers429WithRetryAfter(t *testing.T) {
	l, _ := testLimiter(30, 1)
	h := l.middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	call := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/servers/s1/files/raw", nil)
		r.RemoteAddr = "198.51.100.42:51000"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	if rec := call(); rec.Code != http.StatusOK {
		t.Fatalf("first request: got %d, want 200", rec.Code)
	}
	rec := call()
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second request: got %d, want 429", rec.Code)
	}
	if ra := rec.Header().Get("Retry-After"); ra == "" || ra == "0" {
		t.Fatalf("Retry-After = %q, want a positive number of seconds", ra)
	}
	if ct := rec.Header().Get("Content-Type"); ct == "" {
		t.Fatal("a 429 went out with no content type — it must be the same JSON envelope as every other refusal")
	}
}
