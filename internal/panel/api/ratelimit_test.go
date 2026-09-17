package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fixedClock drives the limiter's window without sleeping through it.
type fixedClock struct{ t time.Time }

func (c *fixedClock) now() time.Time      { return c.t }
func (c *fixedClock) add(d time.Duration) { c.t = c.t.Add(d) }

func testLimiter(perMinute float64, burst int) (*rateLimiter, *fixedClock) {
	clk := &fixedClock{t: time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)}
	l := newRateLimiter("test", perMinute, burst, true)
	l.now = clk.now
	return l, clk
}

// The burst is what a real client uses — a page that fires a few downloads in a
// row — so it is admitted in full, and the request after it is not.
func TestRateLimiterAdmitsTheBurstThenRefuses(t *testing.T) {
	l, _ := testLimiter(30, 10)
	for i := range 10 {
		if ok, _, _ := l.allow("198.51.100.7"); !ok {
			t.Fatalf("request %d of the burst was refused", i+1)
		}
	}
	ok, retry, first := l.allow("198.51.100.7")
	if ok {
		t.Fatal("the request past the burst was admitted")
	}
	if retry <= 0 {
		t.Fatalf("Retry-After delay = %s, want a positive wait the caller can act on", retry)
	}
	if !first {
		t.Fatal("the first refusal for this key did not raise the first-trip signal")
	}
	// A flood must leave one trace, not one per packet.
	if _, _, again := l.allow("198.51.100.7"); again {
		t.Fatal("a second refusal in the same window raised the signal again")
	}
}

// A refusal is a pause, not a ban: once the bucket has refilled the same client
// is served again. And a refused request must not consume future capacity, or a
// client that kept knocking would never get back in.
func TestRateLimiterRecoversAfterTheWindow(t *testing.T) {
	l, clk := testLimiter(30, 2) // one token every two seconds
	for range 2 {
		if ok, _, _ := l.allow("198.51.100.8"); !ok {
			t.Fatal("the burst was refused")
		}
	}
	if ok, _, _ := l.allow("198.51.100.8"); ok {
		t.Fatal("admitted past the burst")
	}
	// Keep knocking while refused — this must not push recovery further out.
	for range 5 {
		l.allow("198.51.100.8")
	}
	clk.add(2 * time.Second)
	if ok, _, _ := l.allow("198.51.100.8"); !ok {
		t.Fatal("still refused after the bucket had refilled")
	}
}

// One noisy address must not cost everyone else their downloads.
func TestRateLimiterIsolatesClients(t *testing.T) {
	l, _ := testLimiter(30, 3)
	for range 3 {
		if ok, _, _ := l.allow("198.51.100.9"); !ok {
			t.Fatal("the burst was refused")
		}
	}
	if ok, _, _ := l.allow("198.51.100.9"); ok {
		t.Fatal("admitted past the burst")
	}
	if ok, _, _ := l.allow("203.0.113.4"); !ok {
		t.Fatal("a different client was refused for someone else's traffic")
	}
}

// IPv6 is limited per /64, not per address. A single subscriber is routinely
// delegated a whole /64, so keying on the full address would hand one attacker
// 2^64 independent buckets — a limiter that limits nothing. IPv4 stays per
// address, where one address is the unit anyone actually gets.
func TestRateLimiterAggregatesIPv6ToTheRoutedPrefix(t *testing.T) {
	l, _ := testLimiter(30, 2)
	if ok, _, _ := l.allow("2001:db8:1:2::1"); !ok {
		t.Fatal("first address in the prefix was refused")
	}
	if ok, _, _ := l.allow("2001:db8:1:2::2"); !ok {
		t.Fatal("second address in the prefix was refused")
	}
	// Third request from the same /64, from a "different" address.
	if ok, _, _ := l.allow("2001:db8:1:2:ffff::dead"); ok {
		t.Fatal("hopping addresses inside one /64 bought a fresh bucket")
	}
	// A different /64 is a different client.
	if ok, _, _ := l.allow("2001:db8:1:3::1"); !ok {
		t.Fatal("a separate /64 was refused for its neighbour's traffic")
	}
	if got := limiterKey("2001:db8:1:2::1"); got != "2001:db8:1:2::/64" {
		t.Fatalf("limiterKey = %q, want the /64", got)
	}
	if got := limiterKey("198.51.100.7"); got != "198.51.100.7" {
		t.Fatalf("limiterKey(v4) = %q, want the address itself", got)
	}
}

// The table is keyed by attacker-chosen input, so it is swept: entries nobody
// has used in a while go away rather than accumulating for the life of the
// process.
func TestRateLimiterSweepsIdleEntries(t *testing.T) {
	l, clk := testLimiter(30, 3)
	for _, ip := range []string{"198.51.100.1", "198.51.100.2", "198.51.100.3"} {
		if ok, _, _ := l.allow(ip); !ok {
			t.Fatalf("%s was refused on its first request", ip)
		}
	}
	if n := l.size(); n != 3 {
		t.Fatalf("table holds %d entries, want 3", n)
	}
	clk.add(rateLimiterIdleTTL + rateLimiterSweepEvery)
	if ok, _, _ := l.allow("203.0.113.9"); !ok {
		t.Fatal("a fresh client was refused")
	}
	if n := l.size(); n != 1 {
		t.Fatalf("table holds %d entries after the sweep, want just the live one", n)
	}
}

// At the cap the table is cut back to a watermark rather than to the cap
// itself. Evicting exactly one entry per admission would leave every later
// request doing a full scan and sort under the mutex; freeing a quarter of the
// table amortises that work over the entries it bought.
func TestRateLimiterEvictsDownToTheWatermark(t *testing.T) {
	l, _ := testLimiter(60, 2)
	for i := range rateLimiterMaxEntries + 1 {
		l.allow(fmt.Sprintf("10.%d.%d.%d", i>>16&0xff, i>>8&0xff, i&0xff))
	}
	// One entry past the watermark: the eviction runs when the table reaches
	// the cap, and the admission that triggered it is then inserted.
	if n := l.size(); n > rateLimiterWatermark+1 {
		t.Fatalf("table holds %d entries, want it cut back to the %d watermark", n, rateLimiterWatermark)
	}
}

// Eviction must never forgive a penalty. A client in the middle of being
// limited has a partly-spent bucket; dropping its entry would hand it a fresh
// burst, which makes filling the table the cheapest way past the limiter.
func TestRateLimiterEvictionKeepsAPenalisedClient(t *testing.T) {
	l, clk := testLimiter(60, 2)
	const victim = "198.51.100.77"
	// Spend the victim's bucket, then let it go idle so it sorts oldest-first
	// and is the very first candidate the eviction pass considers.
	for range 3 {
		l.allow(victim)
	}
	clk.add(time.Nanosecond) // enough to sort oldest-first, not enough to refill
	for i := range rateLimiterMaxEntries + 1 {
		l.allow(fmt.Sprintf("10.%d.%d.%d", i>>16&0xff, i>>8&0xff, i&0xff))
	}
	if ok, _, _ := l.allow(victim); ok {
		t.Fatal("a table flood bought a penalised client a fresh bucket — eviction forgave the penalty")
	}
}

// A refusal answers in the shape every other refusal does — the JSON error
// envelope — plus the Retry-After a client needs to behave.
func TestRateLimiterMiddlewareAnswers429WithRetryAfter(t *testing.T) {
	s := testAPI(nil)
	l, _ := testLimiter(30, 1)
	h := s.limit(l)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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

// peek answers the same question allow does and leaves the bucket alone. It is
// what makes a failures-only limiter possible: every request is measured
// against the budget, and only a failure takes anything out of it.
func TestRateLimiterPeekMeasuresWithoutSpending(t *testing.T) {
	l, _ := testLimiter(10, 3)
	for i := range 50 {
		if ok, _, _ := l.peek("alice"); !ok {
			t.Fatalf("peek %d was refused although nothing had been spent", i+1)
		}
	}
	for i := range 3 {
		l.charge("alice")
		if i == 2 {
			break // the budget is spent; the refusal is asserted below
		}
		if ok, _, _ := l.peek("alice"); !ok {
			t.Fatalf("peek refused after %d of 3 charges", i+1)
		}
	}
	ok, retry, first := l.peek("alice")
	if ok {
		t.Fatal("peek admitted with the budget spent")
	}
	if retry <= 0 {
		t.Fatalf("Retry-After delay = %s, want a positive wait", retry)
	}
	if !first {
		t.Fatal("the first refusal did not raise the first-trip signal")
	}
	// A spent budget cannot go further into the red: charging a refused key is
	// a no-op, so knocking does not push recovery out.
	for range 20 {
		l.charge("alice")
	}
	if _, retryAgain, _ := l.peek("alice"); retryAgain > retry {
		t.Fatalf("charging a spent budget pushed the wait out: %s → %s", retry, retryAgain)
	}
	// And it is per key.
	if ok, _, _ := l.peek("bob"); !ok {
		t.Fatal("a second key was refused for the first one's failures")
	}
}

// The username limiter's keys are usernames, not addresses: nothing is folded
// to a /64, and a key an unauthenticated caller chose cannot be arbitrarily
// long.
func TestUsernameLimiterKeysOnTheRawString(t *testing.T) {
	l := newUsernameLimiter("test_user", 10, 1, true)
	if got := l.key("2001:db8:1:2::1"); got != "2001:db8:1:2::1" {
		t.Fatalf("key = %q, want the username verbatim — an operator named like an address is not a /64", got)
	}
	long := strings.Repeat("a", rateLimiterMaxKeyLen*4)
	if got := l.key(long); len(got) != rateLimiterMaxKeyLen {
		t.Fatalf("key kept %d bytes of an oversized username, want %d", len(got), rateLimiterMaxKeyLen)
	}
	if got := normalizeUsername("  AdMiN  "); got != "admin" {
		t.Fatalf("normalizeUsername = %q, want the trimmed lowercase form", got)
	}
}

// KRAKEN_RATE_LIMITS=off is a real off switch, not a looser limit: an operator
// who has their own edge rate limiting (or is debugging one) gets the Panel out
// of the way entirely.
func TestRateLimiterDisabledAdmitsEverything(t *testing.T) {
	l := newRateLimiter("test", 1, 1, false)
	for i := range 50 {
		if ok, _, _ := l.allow("198.51.100.5"); !ok {
			t.Fatalf("request %d was refused although limiting is off", i+1)
		}
	}
	if n := l.size(); n != 0 {
		t.Fatalf("a disabled limiter kept %d entries; it should not even build a table", n)
	}
}
