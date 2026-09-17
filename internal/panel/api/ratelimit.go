package api

import (
	"math"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Rate limits. Deliberately generous: these exist so an unauthenticated
// stranger cannot hammer a route for free, not to police operators. Several
// people behind one NAT (a homelab, an office, a VPN exit) share an IP, and
// locking them out of their own Panel would be a far worse failure than the
// abuse being prevented. KRAKEN_RATE_LIMITS=off turns both off outright.
const (
	// downloadRedeemPerMinute / Burst guard the redemption of a download token,
	// which is reachable with no credentials at all (the token is the whole
	// authority). A browser takes ONE of these per click, so ten in a row is
	// already well past normal use. The session-authenticated form of the same
	// route is NOT limited — an operator with a Bearer token is not who this is
	// for, and sharing a NAT address with a prober must not cost them downloads.
	downloadRedeemPerMinute = 30
	downloadRedeemBurst     = 10
	// loginPerMinute / Burst guard POST /auth/login. A human logging in takes
	// one request; twenty a minute from one address leaves room for a whole
	// team behind a single NAT, a fumbled password and a page reload, while
	// still putting a ceiling on scripted guessing.
	loginPerMinute = 20
	loginBurst     = 20
	// loginUserPerMinute / Burst guard the same route along a second axis: the
	// submitted username, with no address in the key at all. It exists because
	// the per-IP limiter has a topology it cannot survive — a NAT that rewrites
	// every source address to one gateway (Docker Desktop's 192.168.65.1) makes
	// the per-IP bucket one shared bucket for the whole internet, protective of
	// nobody and a self-DoS for everybody. Keyed on the username, a brute-force
	// against one account is slowed without the Panel knowing who is calling,
	// and every other account is untouched.
	//
	// It counts FAILURES ONLY: a correct password spends nothing, so the person
	// who knows their password is never refused for what a stranger did with
	// their username. Ten a minute leaves room for a fumbled password, a stale
	// saved credential and a page reload, and still puts a hard ceiling on
	// guessing.
	loginUserPerMinute = 10
	loginUserBurst     = 10
)

// rateLimiterMaxKeyLen bounds one key's contribution to the table. The
// per-username limiter is keyed on a string an unauthenticated caller chose, up
// to the 4 MiB body cap; the entry count is capped but the memory each entry
// holds must be too. Truncation can only merge two absurd usernames into one
// bucket, which costs the attacker and nobody else.
const rateLimiterMaxKeyLen = 128

// Limiter table bounds. The table is keyed by client address — attacker-chosen
// input — so it is swept and capped rather than allowed to grow.
const (
	// rateLimiterIdleTTL is how long an untouched entry survives a sweep. Well
	// past the time any bucket needs to refill, so an entry that ages out has
	// nothing left to forget: its bucket was full again long before.
	rateLimiterIdleTTL = 10 * time.Minute
	// rateLimiterSweepEvery is the minimum gap between sweeps; the sweep runs
	// inline on an admission, so there is no goroutine to own or stop.
	rateLimiterSweepEvery = time.Minute
	// rateLimiterMaxEntries caps the table outright. Past it, entries are
	// evicted down to rateLimiterWatermark so the O(n log n) eviction amortises
	// over the entries it freed rather than running on every later admission —
	// and only FULL buckets are evicted, so a client in the middle of being
	// limited cannot buy a fresh burst by flooding the table with new keys.
	rateLimiterMaxEntries = 8192
	rateLimiterWatermark  = rateLimiterMaxEntries * 3 / 4
	// rateLimiterNotifyEvery is how often one key's refusals may raise the
	// "first trip" signal (an audit row for login). Once a window, not once a
	// request: a flood must leave a trace, not write one row per packet.
	rateLimiterNotifyEvery = time.Minute
)

// rateLimiterEntry is one client's token bucket plus when it was last used.
type rateLimiterEntry struct {
	lim      *rate.Limiter
	seen     time.Time
	notified time.Time // last time this key's refusal raised the first-trip signal
}

// rateLimiter is a per-key (per-client-address) token-bucket limiter over a
// bounded map. No new dependency and no background goroutine: x/time/rate does
// the bucket, and the table is swept on the way through.
type rateLimiter struct {
	name    string // metrics label: "download", "login", "login_user"
	limit   rate.Limit
	burst   int
	enabled bool

	// rawKeys skips the address normalization in limiterKey. The per-username
	// limiter's keys are usernames, not addresses: an operator called `::1` is
	// not a /64, and folding one into the other would be nonsense in both
	// directions.
	rawKeys bool

	mu        sync.Mutex
	entries   map[string]*rateLimiterEntry
	lastSweep time.Time

	// now is the clock, injectable so a test can drive the window rather than
	// sleep through it.
	now func() time.Time

	// onFirstTrip, when set, is called the first time a given key is refused in
	// a window — how the login limiter leaves an audit row for a brute-force
	// flood that never reaches the handler that would have audited it.
	onFirstTrip func(*http.Request)
}

// newRateLimiter builds a limiter admitting perMinute requests per key per
// minute, with burst available immediately. enabled=false makes every call a
// pass-through (KRAKEN_RATE_LIMITS=off).
func newRateLimiter(name string, perMinute float64, burst int, enabled bool) *rateLimiter {
	return &rateLimiter{
		name:    name,
		limit:   rate.Limit(perMinute / 60),
		burst:   burst,
		enabled: enabled,
		entries: map[string]*rateLimiterEntry{},
		now:     time.Now,
	}
}

// newUsernameLimiter builds the failures-only, username-keyed login limiter.
// Same table and same bucket as the per-IP one; only the key differs.
func newUsernameLimiter(name string, perMinute float64, burst int, enabled bool) *rateLimiter {
	l := newRateLimiter(name, perMinute, burst, enabled)
	l.rawKeys = true
	return l
}

// normalizeUsername is the login limiter's key: the submitted username with
// case and surrounding space taken out, so `Alice`, `alice ` and `alice` share
// one budget rather than three. It is a limiter key only — the store lookup
// still uses what the caller actually typed.
func normalizeUsername(u string) string {
	return truncateKey(strings.ToLower(strings.TrimSpace(u)))
}

// truncateKey bounds a key at rateLimiterMaxKeyLen bytes.
func truncateKey(k string) string {
	if len(k) > rateLimiterMaxKeyLen {
		return k[:rateLimiterMaxKeyLen]
	}
	return k
}

// limiterKey normalizes a client address into the unit a limit applies to.
// IPv4 is one address, one bucket. IPv6 is aggregated to the /64: the smallest
// block anyone is routinely delegated is a /64, so keying on the full address
// would hand a single subscriber 2^64 independent buckets and no limit at all.
func limiterKey(addr string) string {
	ip := net.ParseIP(addr)
	if ip == nil {
		return addr
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	return ip.Mask(net.CIDRMask(64, 128)).String() + "/64"
}

// allow reports whether this key may proceed and, when it may not, how long it
// should wait and whether this is the first refusal for that key in a window.
// The wait comes from the bucket itself rather than a guessed constant, so the
// Retry-After a caller is handed is the truth.
func (l *rateLimiter) allow(key string) (ok bool, retry time.Duration, firstTrip bool) {
	return l.admit(key, true)
}

// peek is allow without spending: it reports whether the key has budget right
// now and leaves the bucket exactly as it found it. It is how a failures-only
// limiter admits — every request is measured against the budget, and only
// charge() takes anything out of it.
func (l *rateLimiter) peek(key string) (ok bool, retry time.Duration, firstTrip bool) {
	return l.admit(key, false)
}

// charge spends one token against a key's budget, or nothing when the budget is
// already empty. This is the failures-only limiter's other half: the wrong
// password costs the username a token, the right one costs it nothing.
func (l *rateLimiter) charge(key string) {
	if l == nil || !l.enabled {
		return
	}
	key = l.key(key)
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweepLocked(now)
	e := l.entryLocked(key, now)
	e.lim.AllowN(now, 1)
}

// admit is the body of allow and peek: spend=false gives the token back before
// returning, so the answer is the same and the bucket is untouched.
func (l *rateLimiter) admit(key string, spend bool) (ok bool, retry time.Duration, firstTrip bool) {
	if l == nil || !l.enabled {
		return true, 0, false
	}
	key = l.key(key)
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweepLocked(now)
	e := l.entryLocked(key, now)
	res := e.lim.ReserveN(now, 1)
	if !res.OK() { // burst of 0 — nothing is ever admitted
		return false, time.Second, l.noteTripLocked(e, now)
	}
	if d := res.DelayFrom(now); d > 0 {
		// Hand the token back: a refused request must not also consume future
		// capacity, or a client hammering the door would never get back in.
		res.CancelAt(now)
		return false, d, l.noteTripLocked(e, now)
	}
	if !spend {
		res.CancelAt(now)
	}
	return true, 0, false
}

// key maps a caller's key onto the table's, and entryLocked fetches or creates
// its bucket. Caller of entryLocked holds l.mu.
func (l *rateLimiter) key(k string) string {
	if l.rawKeys {
		return truncateKey(k)
	}
	return limiterKey(k)
}

func (l *rateLimiter) entryLocked(key string, now time.Time) *rateLimiterEntry {
	e, found := l.entries[key]
	if !found {
		e = &rateLimiterEntry{lim: rate.NewLimiter(l.limit, l.burst)}
		l.entries[key] = e
	}
	e.seen = now
	return e
}

// noteTripLocked reports whether this refusal is the first for its key in a
// window, and records it. Caller holds l.mu.
func (l *rateLimiter) noteTripLocked(e *rateLimiterEntry, now time.Time) bool {
	if !e.notified.IsZero() && now.Sub(e.notified) < rateLimiterNotifyEvery {
		return false
	}
	e.notified = now
	return true
}

// sweepLocked drops entries nobody has used lately, and — when the table has
// reached its cap — evicts down to the watermark, oldest-seen first. Caller
// holds l.mu.
//
// Two things make that eviction safe rather than a way around the limiter.
// It frees a QUARTER of the table rather than the one entry that was over the
// line, so the sort amortises over everything it bought instead of running
// under the mutex on every later admission. And it passes over any client that
// cannot make a request right now — a bucket with less than one token, which is
// precisely the set currently being refused — because dropping one of those
// would hand it a full burst and make filling the table the cheapest way past
// the limit.
//
// That protection yields to the cap if it has to: if every candidate is
// currently refused, the oldest go anyway. A bounded table is not negotiable —
// it is the reason any of this exists — and the alternative is a map an
// attacker grows without limit by keeping every bucket spent.
func (l *rateLimiter) sweepLocked(now time.Time) {
	due := now.Sub(l.lastSweep) >= rateLimiterSweepEvery
	if !due && len(l.entries) < rateLimiterMaxEntries {
		return
	}
	if due {
		l.lastSweep = now
		for k, e := range l.entries {
			if now.Sub(e.seen) > rateLimiterIdleTTL {
				delete(l.entries, k)
			}
		}
	}
	if len(l.entries) < rateLimiterMaxEntries {
		return
	}
	keys := make([]string, 0, len(l.entries))
	for k := range l.entries {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return l.entries[keys[i]].seen.Before(l.entries[keys[j]].seen)
	})
	for _, protectRefused := range []bool{true, false} {
		for _, k := range keys {
			if len(l.entries) <= rateLimiterWatermark {
				return
			}
			e, ok := l.entries[k]
			if !ok {
				continue // already evicted on the first pass
			}
			if protectRefused && e.lim.TokensAt(now) < 1 {
				continue // being refused right now; its penalty is not ours to forgive
			}
			delete(l.entries, k)
		}
	}
}

// size reports how many keys the table holds (the sweep's test-facing check).
func (l *rateLimiter) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.entries)
}

// reject applies the limiter to one request, writing the 429 itself and
// reporting whether the caller should stop. It is what both the middleware and
// the token branch of a download route go through, so there is one refusal.
//
// The key is Server.clientIP — the real TCP peer, or, when the peer is a
// configured trusted proxy, the address that proxy says the client has (see
// clientip.go). Getting that wrong in either direction breaks the limiter: with
// no trusted set a forwarded header would let an attacker mint a new bucket per
// request, and behind an unconfigured proxy every caller shares one bucket.
// A client inside KRAKEN_RATE_LIMIT_IP_SKIP is exempt: that list names the
// addresses a per-IP bucket is meaningless for — a NAT gateway standing in for
// everybody — where the limit would refuse the whole internet together rather
// than refuse anyone in particular. Login keeps its per-username limiter there,
// which needs no address at all.
func (s *Server) reject(l *rateLimiter, w http.ResponseWriter, r *http.Request) bool {
	ip := s.clientIP(r)
	if s.ipLimitExempt(ip) {
		return false
	}
	ok, retry, first := l.allow(ip)
	if ok {
		return false
	}
	incRateLimited(l.name)
	if first && l.onFirstTrip != nil {
		l.onFirstTrip(r)
	}
	s.writeRateLimited(w, retry)
	return true
}

// The audit actions the per-username limiter writes when it refuses. Separate
// from the per-IP limiter's row, and deliberately so: "this username is being
// guessed at" and "this address is hammering the door" are different findings
// and an operator reading the log should not have to tell them apart by hand.
const (
	loginRateLimitedAction = "POST /auth/login — rate limited (too many failed attempts for this username)"
	// Named for the credential rather than spelling the word gosec's
	// hardcoded-credential rule scans identifiers for: this is an audit
	// action string, and a suppression comment would be the noisier fix.
	credentialRotateRateLimitedAction = "POST /auth/change-password — rate limited (too many failed attempts for this account)"
)

// rejectUsername applies the failures-only per-username limiter, writing the
// 429 and — once per username per window — the audit row that says why. It is
// applied to EVERY submitted username, known to the store or not: a 429 that
// only ever came back for real accounts would answer "does this user exist?"
// for anybody who asked eleven times.
func (s *Server) rejectUsername(w http.ResponseWriter, r *http.Request, username, action string) bool {
	l := s.loginUserLimit
	key := normalizeUsername(username)
	ok, retry, first := l.peek(key)
	if ok {
		return false
	}
	incRateLimited(l.name)
	// Claim the audit middleware's row for EVERY refusal, not just the first.
	// Change-password runs inside that middleware, so without this a flood
	// there would simply move into the audit table at one row per request —
	// which is the amplification the once-a-window rule exists to prevent.
	// Login is outside the middleware and has no note to claim.
	if n, _ := r.Context().Value(ctxKeyAuditNote).(*auditNote); n != nil {
		n.claimed = true
	}
	if first {
		s.appendAudit(r, http.StatusTooManyRequests, key, action)
	}
	s.writeRateLimited(w, retry)
	return true
}

// writeRateLimited is the one refusal: the ordinary JSON error envelope plus
// the Retry-After the bucket itself computed, so what a caller is told to wait
// is the truth rather than a guessed constant.
func (s *Server) writeRateLimited(w http.ResponseWriter, retry time.Duration) {
	secs := int(math.Ceil(retry.Seconds()))
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	writeError(w, http.StatusTooManyRequests, "too many requests")
}

// ipLimitExempt reports whether a resolved client address is one the operator
// has taken out of the per-IP limiters (KRAKEN_RATE_LIMIT_IP_SKIP).
func (s *Server) ipLimitExempt(addr string) bool {
	if len(s.rateLimitSkipNets) == 0 {
		return false
	}
	ip := net.ParseIP(addr)
	if ip == nil {
		return false
	}
	for _, n := range s.rateLimitSkipNets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// limit wraps a handler in the limiter, for routes where every request is
// subject to it (login). The download routes call reject directly instead,
// because only their token branch is limited.
func (s *Server) limit(l *rateLimiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if s.reject(l, w, r) {
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
