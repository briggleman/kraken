package api

import (
	"math"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Rate limits. Deliberately generous: these exist so an unauthenticated
// stranger cannot hammer a route for free, not to police operators. Several
// people behind one NAT (a homelab, an office, a VPN exit) share an IP, and
// locking them out of their own Panel would be a far worse failure than the
// abuse being prevented.
const (
	// downloadRedeemPerMinute / Burst guard the two GET download routes, which
	// are reachable with no credentials at all (?token= is the whole
	// authority). A browser takes ONE of these per click, so ten in a row is
	// already well past normal use.
	downloadRedeemPerMinute = 30
	downloadRedeemBurst     = 10
	// loginPerMinute / Burst guard POST /auth/login. A human logging in takes
	// one request; twenty a minute from one address leaves room for a whole
	// team behind a single NAT, a fumbled password and a page reload, while
	// still putting a ceiling on scripted guessing.
	loginPerMinute = 20
	loginBurst     = 20
)

// Limiter table bounds. The table is keyed by client IP — attacker-chosen
// input — so it is swept and capped rather than allowed to grow.
const (
	// rateLimiterIdleTTL is how long an untouched entry survives a sweep. Well
	// past the time any bucket needs to refill, so evicting an idle entry can
	// never hand its owner a fresh burst it had not earned.
	rateLimiterIdleTTL = 10 * time.Minute
	// rateLimiterSweepEvery is the minimum gap between sweeps; the sweep runs
	// inline on an admission, so there is no goroutine to own or stop.
	rateLimiterSweepEvery = time.Minute
	// rateLimiterMaxEntries caps the table outright. Past it, the
	// least-recently-seen entries are evicted so a flood of distinct source
	// addresses cannot grow the map without bound.
	rateLimiterMaxEntries = 8192
)

// rateLimiterEntry is one client's token bucket plus when it was last used.
type rateLimiterEntry struct {
	lim  *rate.Limiter
	seen time.Time
}

// rateLimiter is a per-key (per-IP) token-bucket limiter over a bounded map.
// No new dependency and no background goroutine: x/time/rate does the bucket,
// and the table is swept on the way through.
type rateLimiter struct {
	limit rate.Limit
	burst int

	mu        sync.Mutex
	entries   map[string]*rateLimiterEntry
	lastSweep time.Time

	// now is the clock, injectable so a test can drive the window rather than
	// sleep through it.
	now func() time.Time
}

// newRateLimiter builds a limiter admitting perMinute requests per key per
// minute, with burst available immediately.
func newRateLimiter(perMinute float64, burst int) *rateLimiter {
	return &rateLimiter{
		limit:   rate.Limit(perMinute / 60),
		burst:   burst,
		entries: map[string]*rateLimiterEntry{},
		now:     time.Now,
	}
}

// allow reports whether this key may proceed and, when it may not, how long it
// should wait. The wait comes from the bucket itself rather than a guessed
// constant, so the Retry-After a caller is handed is the truth.
func (l *rateLimiter) allow(key string) (bool, time.Duration) {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweepLocked(now)
	e, ok := l.entries[key]
	if !ok {
		e = &rateLimiterEntry{lim: rate.NewLimiter(l.limit, l.burst)}
		l.entries[key] = e
	}
	e.seen = now
	res := e.lim.ReserveN(now, 1)
	if !res.OK() { // burst of 0 — nothing is ever admitted
		return false, time.Second
	}
	if d := res.DelayFrom(now); d > 0 {
		// Hand the token back: a refused request must not also consume future
		// capacity, or a client hammering the door would never get back in.
		res.CancelAt(now)
		return false, d
	}
	return true, 0
}

// sweepLocked drops entries nobody has used lately, and — if the table is still
// over its cap — the least-recently-seen of what is left. Caller holds l.mu.
func (l *rateLimiter) sweepLocked(now time.Time) {
	if now.Sub(l.lastSweep) < rateLimiterSweepEvery && len(l.entries) < rateLimiterMaxEntries {
		return
	}
	l.lastSweep = now
	for k, e := range l.entries {
		if now.Sub(e.seen) > rateLimiterIdleTTL {
			delete(l.entries, k)
		}
	}
	if len(l.entries) <= rateLimiterMaxEntries {
		return
	}
	keys := make([]string, 0, len(l.entries))
	for k := range l.entries {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return l.entries[keys[i]].seen.Before(l.entries[keys[j]].seen)
	})
	for _, k := range keys[:len(l.entries)-rateLimiterMaxEntries] {
		delete(l.entries, k)
	}
}

// size reports how many keys the table holds (the sweep's test-facing check).
func (l *rateLimiter) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.entries)
}

// middleware applies the limiter per client IP, answering 429 with the same
// JSON error envelope as every other refusal plus a Retry-After the caller can
// act on.
//
// The key is clientIP(), which is the real TCP peer: the Panel deliberately
// does not trust X-Forwarded-For / X-Real-IP anywhere (middleware.RealIP was
// removed for exactly that reason — see routes()), and a limiter keyed on a
// spoofable header is a limiter an attacker can step around one fake address
// at a time.
func (l *rateLimiter) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ok, retry := l.allow(clientIP(r)); !ok {
			secs := int(math.Ceil(retry.Seconds()))
			if secs < 1 {
				secs = 1
			}
			w.Header().Set("Retry-After", strconv.Itoa(secs))
			writeError(w, http.StatusTooManyRequests, "too many requests")
			return
		}
		next.ServeHTTP(w, r)
	})
}
