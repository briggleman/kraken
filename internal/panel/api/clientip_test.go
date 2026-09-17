package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/briggleman/kraken/internal/panel/config"
	"github.com/briggleman/kraken/internal/panel/store/memory"
)

func proxyAPI(t *testing.T, trusted ...string) *Server {
	t.Helper()
	return New(&config.Config{Env: "test", SessionTTL: time.Hour, TrustedProxies: trusted},
		memory.New(), slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// skipAPI is proxyAPI with an address taken out of the per-IP limiters.
func skipAPI(t *testing.T, skip ...string) *Server {
	t.Helper()
	return New(&config.Config{Env: "test", SessionTTL: time.Hour, RateLimitIPSkip: skip},
		memory.New(), slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// The forwarded chain rides along ONLY when the resolved address identifies
// nobody. A row whose source is a real client address already says everything
// there is to say, and copying an attacker-written header into every row would
// be noise at best.
func TestForwardedChainIsRecordedOnlyWhenTheSourceIdentifiesNobody(t *testing.T) {
	s := proxyAPI(t)
	hdr := map[string]string{"X-Forwarded-For": "203.0.113.9, 192.168.65.1"}

	if got := s.forwardedChain(request("198.51.100.7:1234", hdr), "198.51.100.7"); got != "" {
		t.Fatalf("a public client carried a forwarded chain: %q", got)
	}
	// A NAT gateway: this is the case the chain exists for.
	if got := s.forwardedChain(request("192.168.65.1:1234", hdr), "192.168.65.1"); got != "203.0.113.9, 192.168.65.1" {
		t.Fatalf("forwardedChain = %q, want the raw chain as received", got)
	}
	// No header, nothing to record.
	if got := s.forwardedChain(request("192.168.65.1:1234", nil), "192.168.65.1"); got != "" {
		t.Fatalf("forwardedChain invented a chain: %q", got)
	}
	// A public address the operator exempted: they have said that one stands in
	// for others, so the chain is worth keeping there too.
	ex := skipAPI(t, "198.51.100.7")
	if got := ex.forwardedChain(request("198.51.100.7:1234", hdr), "198.51.100.7"); got == "" {
		t.Fatal("an exempted address carried no chain — it is the only place a real client survives there")
	}
}

// The header is written by the caller, so what lands in the store is bounded
// and printable. It is forensics, not a place to park a megabyte.
func TestForwardedChainIsBoundedAndPrintable(t *testing.T) {
	s := proxyAPI(t)
	long := strings.Repeat("203.0.113.9, ", 200) + "10.0.0.1"
	got := s.forwardedChain(request("10.0.0.1:1234", map[string]string{"X-Forwarded-For": long}), "10.0.0.1")
	if len(got) > auditForwardedMaxLen {
		t.Fatalf("forwardedChain kept %d bytes, want at most %d", len(got), auditForwardedMaxLen)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("forwardedChain produced invalid UTF-8: %q", got)
	}
}

// End to end: an audit row written from behind a NAT carries the chain, and the
// same row written from a real client address does not.
func TestAuditRowCarriesTheForwardedChain(t *testing.T) {
	st := memory.New()
	s := New(&config.Config{Env: "test", SessionTTL: time.Hour}, st,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	hdr := map[string]string{"X-Forwarded-For": "203.0.113.9"}
	s.appendAudit(request("192.168.65.1:1234", hdr), 401, "alice", "")
	s.appendAudit(request("198.51.100.7:1234", hdr), 401, "bob", "")

	entries, err := st.ListAudit(context.Background(), 10)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	got := map[string]string{}
	for _, e := range entries {
		got[e.Actor] = e.ForwardedFor
	}
	if got["alice"] != "203.0.113.9" {
		t.Fatalf("the row from behind a NAT carries %q, want the forwarded chain", got["alice"])
	}
	if got["bob"] != "" {
		t.Fatalf("the row from a real client address carries %q, want nothing", got["bob"])
	}
}

func request(peer string, headers map[string]string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/auth/login", nil)
	r.RemoteAddr = peer
	for k, v := range headers {
		r.Header.Add(k, v)
	}
	return r
}

// With no trusted proxies configured — the default — a forwarding header is
// just a header somebody sent. Believing it would let any caller mint a fresh
// rate-limit bucket per request and file audit rows under any address they
// liked, so the peer is the only answer.
func TestClientIPIgnoresForwardedHeadersFromAnUntrustedPeer(t *testing.T) {
	s := proxyAPI(t)
	r := request("203.0.113.9:44321", map[string]string{
		"X-Forwarded-For":  "10.0.0.1, 198.51.100.5",
		"CF-Connecting-IP": "198.51.100.6",
	})
	if got := s.clientIP(r); got != "203.0.113.9" {
		t.Fatalf("clientIP = %q, want the real peer — a spoofed header was believed", got)
	}
}

// Behind a configured proxy the peer is the PROXY on every request, which is
// worse than spoofable: one bucket for the whole internet. The rightmost hop
// that is not itself one of our proxies is the client — everything to its left
// is whatever the client chose to send.
func TestClientIPTakesTheRightmostUntrustedHopFromATrustedPeer(t *testing.T) {
	s := proxyAPI(t, "127.0.0.0/8", "10.0.0.0/8")
	r := request("127.0.0.1:8443", map[string]string{
		"X-Forwarded-For": "203.0.113.1, 198.51.100.7, 10.0.0.2",
	})
	if got := s.clientIP(r); got != "198.51.100.7" {
		t.Fatalf("clientIP = %q, want the rightmost hop outside the trusted set", got)
	}
}

// The reference deployment is a Cloudflare Tunnel on the Panel's own host. It
// appends to X-Forwarded-For like any other proxy, so the rightmost-untrusted
// rule serves it without the Panel having to believe a vendor-specific header
// that other proxies forward verbatim.
func TestClientIPResolvesTheClientBehindACloudflareTunnel(t *testing.T) {
	s := proxyAPI(t, "127.0.0.1")
	r := request("127.0.0.1:8443", map[string]string{
		"CF-Connecting-IP": "198.51.100.42",
		"X-Forwarded-For":  "198.51.100.42",
	})
	if got := s.clientIP(r); got != "198.51.100.42" {
		t.Fatalf("clientIP = %q, want the address the tunnel forwarded", got)
	}
}

// A chain of nothing but our own proxies, or no chain at all, leaves the peer
// as the most specific thing actually known.
func TestClientIPFallsBackToThePeer(t *testing.T) {
	s := proxyAPI(t, "127.0.0.0/8")
	if got := s.clientIP(request("127.0.0.1:8443", nil)); got != "127.0.0.1" {
		t.Fatalf("clientIP with no headers = %q, want the peer", got)
	}
	r := request("127.0.0.1:8443", map[string]string{"X-Forwarded-For": "127.0.0.9, 127.0.0.8"})
	if got := s.clientIP(r); got != "127.0.0.1" {
		t.Fatalf("clientIP with an all-trusted chain = %q, want the peer", got)
	}
}

// The limiter and the audit log ask the same question of the same function, so
// they cannot disagree about who made a request — and behind a trusted proxy
// two real clients get two buckets rather than sharing the proxy's.
func TestRateLimiterKeysOnTheResolvedClientBehindAProxy(t *testing.T) {
	s := proxyAPI(t, "127.0.0.0/8")
	l, _ := testLimiter(30, 1)
	h := s.limit(l)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	call := func(client string) int {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, request("127.0.0.1:8443", map[string]string{"X-Forwarded-For": client}))
		return rec.Code
	}
	if code := call("198.51.100.1"); code != http.StatusOK {
		t.Fatalf("first client: got %d, want 200", code)
	}
	if code := call("198.51.100.2"); code != http.StatusOK {
		t.Fatalf("second client: got %d, want 200 — it shared the proxy's bucket", code)
	}
	if code := call("198.51.100.1"); code != http.StatusTooManyRequests {
		t.Fatalf("first client again: got %d, want 429", code)
	}
}

// CF-Connecting-IP is never believed, from any peer. Only Cloudflare sets it;
// Caddy, nginx and Traefik pass a client-supplied one straight through, and
// nothing in the request says which is in front. Believing it from a trusted
// peer would let an internet client name its own address — and that address
// now drives the internal-network gate in front of /setup/* and the
// unauthenticated local-enrollment route, both rate limiters, and every audit
// row. X-Forwarded-For, which a proxy appends itself, is the only header read.
func TestClientIPNeverBelievesCloudflareHeader(t *testing.T) {
	s := proxyAPI(t, "127.0.0.0/8")
	r := request("127.0.0.1:8443", map[string]string{
		"CF-Connecting-IP": "10.0.0.1", // the prize: an "internal" address
		"X-Forwarded-For":  "198.51.100.7",
	})
	if got := s.clientIP(r); got != "198.51.100.7" {
		t.Fatalf("clientIP = %q, want the forwarded hop — a spoofable vendor header won", got)
	}
	// With no X-Forwarded-For at all it falls back to the peer, not to the
	// header a caller supplied.
	only := request("127.0.0.1:8443", map[string]string{"CF-Connecting-IP": "10.0.0.1"})
	if got := s.clientIP(only); got != "127.0.0.1" {
		t.Fatalf("clientIP = %q, want the peer", got)
	}
}

// A hop that is not an address ends the chain: everything to its left is
// unverifiable, and an unparseable value must not become a rate-limit key of
// its own (or an empty one).
func TestClientIPFallsBackWhenAHopIsGarbage(t *testing.T) {
	s := proxyAPI(t, "127.0.0.0/8")
	r := request("127.0.0.1:8443", map[string]string{"X-Forwarded-For": "not-an-ip"})
	if got := s.clientIP(r); got != "127.0.0.1" {
		t.Fatalf("clientIP = %q, want the peer when the chain is unusable", got)
	}
	empty := request("127.0.0.1:8443", map[string]string{"X-Forwarded-For": " , "})
	if got := s.clientIP(empty); got != "127.0.0.1" {
		t.Fatalf("clientIP = %q, want the peer for an empty chain", got)
	}
}

// Go hands an IPv4 connection to a dual-stack listener as ::ffff:10.0.0.1. A
// trusted proxy written as an IPv4 CIDR has to match it, or the operator's
// configuration silently does nothing on exactly the deployment it was for.
func TestTrustedProxyMatchesAnIPv4MappedPeer(t *testing.T) {
	s := proxyAPI(t, "10.0.0.0/8")
	r := request("[::ffff:10.0.0.1]:8443", map[string]string{"X-Forwarded-For": "198.51.100.7"})
	if got := s.clientIP(r); got != "198.51.100.7" {
		t.Fatalf("clientIP = %q, want the forwarded hop — the IPv4-mapped peer did not match its CIDR", got)
	}
}

// A clipped id must still be valid UTF-8: slicing bytes mid-rune would put an
// invalid sequence into a log line for a JSON handler to mangle.
func TestClipForLogCutsOnARuneBoundary(t *testing.T) {
	// 21 three-byte runes = 63 bytes, so the 64th byte is the middle of the
	// next one — exactly where a naive slice would cut.
	id := strings.Repeat("あ", 21) + "あ"
	got := clipForLog(id)
	if !utf8.ValidString(got) {
		t.Fatalf("clipForLog produced invalid UTF-8: %q", got)
	}
	if len(got) > maxLoggedIDLen+len("…") {
		t.Fatalf("clipForLog returned %d bytes, want at most the cap plus the marker", len(got))
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("clipForLog = %q, want the clipped marker", got)
	}
	if short := clipForLog("srv-1"); short != "srv-1" {
		t.Fatalf("clipForLog clipped a short id: %q", short)
	}
}
