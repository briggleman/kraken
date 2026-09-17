package api_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/briggleman/kraken/internal/panel/config"
	"github.com/briggleman/kraken/internal/panel/store"
)

// The address httptest hands every request, and the /24 it sits in.
const (
	testPeer    = "192.0.2.1:31337"
	testPeerNet = "192.0.2.0/24"
)

// post is `do` with control of the TCP peer, which is what both per-IP
// behaviours below turn on.
func post(t *testing.T, h http.Handler, path, peer string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = peer
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func tryLogin(t *testing.T, h http.Handler, user, pass string) *httptest.ResponseRecorder {
	t.Helper()
	return post(t, h, "/api/v1/auth/login", testPeer,
		map[string]string{"username": user, "password": pass})
}

// noIPLimit takes the per-IP login limiter out of the way, so what a test
// observes is the username axis and nothing else.
func noIPLimit(cfg *config.Config) { cfg.RateLimitIPSkip = []string{testPeerNet} }

// The failure budget is per username and independent of any address: ten wrong
// passwords for one account are answered, the eleventh is refused, and the
// account next to it never notices. This is the limiter that still works on a
// deployment where every request resolves to one NAT gateway.
func TestLoginLimiterThrottlesTheEleventhFailureForOneUsername(t *testing.T) {
	h := newTestServerWith(t, noIPLimit)
	for i := range 10 {
		if rec := tryLogin(t, h, testAdmin, "wrong"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d: got %d, want 401", i+1, rec.Code)
		}
	}
	rec := tryLogin(t, h, testAdmin, "wrong")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("the eleventh failure got %d, want 429", rec.Code)
	}
	if ra := rec.Header().Get("Retry-After"); ra == "" || ra == "0" {
		t.Fatalf("Retry-After = %q, want a positive number of seconds", ra)
	}
	// Somebody else's account is not collateral.
	if rec := tryLogin(t, h, "bob", "wrong"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("a second username got %d, want 401 — it shared the first one's bucket", rec.Code)
	}
	// And the correct password for the throttled username is refused too:
	// that is the point of a per-username budget, and it is why the budget is
	// spent by failures alone.
	if rec := tryLogin(t, h, testAdmin, testPass); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("a correct password during the penalty got %d, want 429", rec.Code)
	}
}

// Only failures cost anything. A person who knows their password can sign in as
// often as they like without spending the budget that protects them.
func TestLoginLimiterChargesFailuresOnly(t *testing.T) {
	h := newTestServerWith(t, noIPLimit)
	for i := range 12 {
		if rec := tryLogin(t, h, testAdmin, testPass); rec.Code != http.StatusOK {
			t.Fatalf("correct login %d: got %d, want 200", i+1, rec.Code)
		}
	}
	// The budget is untouched: a full ten failures are still answered.
	for i := range 10 {
		if rec := tryLogin(t, h, testAdmin, "wrong"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d after a dozen successes: got %d, want 401", i+1, rec.Code)
		}
	}
	if rec := tryLogin(t, h, testAdmin, "wrong"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("the eleventh failure got %d, want 429", rec.Code)
	}
}

// An unknown username is throttled exactly like a real one. If it were not, the
// eleventh attempt would answer "does this account exist?" for anybody willing
// to ask eleven times — a 429 for real accounts and a 401 for invented ones.
func TestLoginLimiterThrottlesUnknownUsernamesIdentically(t *testing.T) {
	h := newTestServerWith(t, noIPLimit)
	const ghost = "no-such-operator"
	for i := range 10 {
		if rec := tryLogin(t, h, ghost, "wrong"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("unknown-user failure %d: got %d, want 401", i+1, rec.Code)
		}
	}
	if rec := tryLogin(t, h, ghost, "wrong"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("the eleventh failure for an unknown username got %d, want 429 — "+
			"a limiter that skips unknown usernames enumerates them", rec.Code)
	}
}

// Case and stray whitespace do not buy a fresh budget.
func TestLoginLimiterNormalisesTheUsernameKey(t *testing.T) {
	h := newTestServerWith(t, noIPLimit)
	spellings := []string{"admin", "ADMIN", " admin", "Admin ", "aDmIn"}
	for i := range 10 {
		s := spellings[i%len(spellings)]
		if rec := tryLogin(t, h, s, "wrong"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d (%q): got %d, want 401", i+1, s, rec.Code)
		}
	}
	if rec := tryLogin(t, h, "AdMiN", "wrong"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("a re-spelled username got %d, want 429 — case bought a second bucket", rec.Code)
	}
}

// Without the skip list the per-IP limiter is what it always was: twenty a
// minute from one address, and the twenty-first is refused whichever username
// it names.
func TestLoginPerIPLimiterStillRefusesABurstFromOneAddress(t *testing.T) {
	h := newTestServer(t)
	for i := range 20 {
		if rec := tryLogin(t, h, fmt.Sprintf("user-%d", i), "wrong"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: got %d, want 401", i+1, rec.Code)
		}
	}
	if rec := tryLogin(t, h, "user-20", "wrong"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("the twenty-first attempt got %d, want 429", rec.Code)
	}
}

// KRAKEN_RATE_LIMIT_IP_SKIP exempts an address from the per-IP limiter — the
// NAT gateway that stands in for everybody, where the bucket refuses the whole
// internet at once rather than refusing anyone in particular. It does NOT
// exempt anything from the per-username limiter, which is the whole reason it
// is safe to set.
func TestRateLimitIPSkipBypassesThePerIPLimiterOnly(t *testing.T) {
	h := newTestServerWith(t, noIPLimit)
	for i := range 25 {
		if rec := tryLogin(t, h, fmt.Sprintf("user-%d", i), "wrong"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d from an exempt address: got %d, want 401", i+1, rec.Code)
		}
	}
	// Same address, one username: still throttled.
	for range 10 {
		tryLogin(t, h, "target", "wrong")
	}
	if rec := tryLogin(t, h, "target", "wrong"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("an exempt address got %d guessing one username, want 429 — "+
			"the skip list must not reach the per-username limiter", rec.Code)
	}
}

// KRAKEN_RATE_LIMITS=downloads leaves login unlimited on both axes; the
// redemption limiter is what it keeps. The inverse (=login) is covered by
// TestRateLimitsOffSwitch's neighbour in handlers_filedownloadtoken_test.go.
func TestRateLimitsDownloadsModeLeavesLoginUnlimited(t *testing.T) {
	h := newTestServerWith(t, func(cfg *config.Config) { cfg.RateLimits = config.RateLimitsDownloads })
	for i := range 25 {
		if rec := tryLogin(t, h, testAdmin, "wrong"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: got %d, want 401 — a login limiter ran in downloads mode", i+1, rec.Code)
		}
	}
}

// KRAKEN_RATE_LIMITS=login keeps both login limiters and drops the download
// one, so the eleventh failure for one username is still refused.
func TestRateLimitsLoginModeKeepsThePerUsernameLimiter(t *testing.T) {
	h := newTestServerWith(t, func(cfg *config.Config) {
		cfg.RateLimits = config.RateLimitsLogin
		cfg.RateLimitIPSkip = []string{testPeerNet}
	})
	for range 10 {
		tryLogin(t, h, testAdmin, "wrong")
	}
	if rec := tryLogin(t, h, testAdmin, "wrong"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("login mode did not keep the per-username limiter: got %d, want 429", rec.Code)
	}
}

// KRAKEN_RATE_LIMITS=off means off, on every axis.
func TestRateLimitsOffLeavesTheUsernameLimiterOff(t *testing.T) {
	h := newTestServerWith(t, func(cfg *config.Config) { cfg.RateLimits = config.RateLimitsOff })
	for i := range 25 {
		if rec := tryLogin(t, h, testAdmin, "wrong"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: got %d, want 401 — a limiter ran with limiting off", i+1, rec.Code)
		}
	}
}

// Change-password verifies the current password exactly as login does, so it
// spends the same budget. Otherwise a stolen session would be the way around
// the login limiter: guess there instead, unthrottled, and the account's own
// limiter never sees it.
func TestChangePasswordSharesTheLoginFailureBudget(t *testing.T) {
	h := newTestServerWith(t, noIPLimit)
	token := login(t, h)
	for i := range 10 {
		rec := do(t, h, http.MethodPost, "/api/v1/auth/change-password", token, map[string]string{
			"current_password": "wrong", "new_password": "abyss-key-2",
		})
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("wrong current password %d: got %d, want 401", i+1, rec.Code)
		}
	}
	rec := do(t, h, http.MethodPost, "/api/v1/auth/change-password", token, map[string]string{
		"current_password": "wrong", "new_password": "abyss-key-2",
	})
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("the eleventh wrong current password got %d, want 429", rec.Code)
	}
	// One budget, not two: the same account's login is refused as well.
	if rec := tryLogin(t, h, testAdmin, testPass); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("login after the change-password budget was spent: got %d, want 429 — "+
			"the two routes must share one bucket, or either is the way around the other", rec.Code)
	}
	// And the refusals do not move the flood into the audit table. This route
	// runs inside the audit middleware, so a 429 that did not claim the row
	// would write one per request.
	for range 10 {
		do(t, h, http.MethodPost, "/api/v1/auth/change-password", token, map[string]string{
			"current_password": "wrong", "new_password": "abyss-key-2",
		})
	}
	rec = do(t, h, http.MethodGet, "/api/v1/audit", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("audit: got %d", rec.Code)
	}
	var out struct {
		Entries []store.AuditEntry `json:"entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode audit: %v", err)
	}
	limited := 0
	for _, ent := range out.Entries {
		if ent.Status == http.StatusTooManyRequests {
			limited++
		}
	}
	if limited != 1 {
		t.Fatalf("the change-password flood left %d 429 audit rows, want exactly 1", limited)
	}
}

// A refusal answers in the shape every other refusal does, so a client can read
// it without special-casing the limiter.
func TestLoginLimiterRefusalIsTheOrdinaryErrorEnvelope(t *testing.T) {
	h := newTestServerWith(t, noIPLimit)
	for range 10 {
		tryLogin(t, h, testAdmin, "wrong")
	}
	rec := tryLogin(t, h, testAdmin, "wrong")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("got %d, want 429", rec.Code)
	}
	body, err := io.ReadAll(rec.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var env struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("a 429 was not the JSON error envelope: %v (body %s)", err, body)
	}
	if env.Error == "" {
		t.Fatalf("a 429 carried no error message: %s", body)
	}
}
