package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/briggleman/kraken/internal/panel/config"
	"github.com/briggleman/kraken/internal/panel/store/memory"
)

func proxyAPI(t *testing.T, trusted ...string) *Server {
	t.Helper()
	return New(&config.Config{Env: "test", SessionTTL: time.Hour, TrustedProxies: trusted},
		memory.New(), slog.New(slog.NewTextHandler(io.Discard, nil)))
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

// The reference deployment is a Cloudflare Tunnel, which names the client
// outright. When the peer is trusted, that header is the most direct answer
// there is.
func TestClientIPPrefersCloudflareHeaderFromATrustedPeer(t *testing.T) {
	s := proxyAPI(t, "127.0.0.1")
	r := request("127.0.0.1:8443", map[string]string{
		"CF-Connecting-IP": "198.51.100.42",
		"X-Forwarded-For":  "198.51.100.42",
	})
	if got := s.clientIP(r); got != "198.51.100.42" {
		t.Fatalf("clientIP = %q, want the address the tunnel named", got)
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
		h.ServeHTTP(rec, request("127.0.0.1:8443", map[string]string{"CF-Connecting-IP": client}))
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
