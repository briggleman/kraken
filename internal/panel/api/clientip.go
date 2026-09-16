package api

import (
	"net"
	"net/http"
	"strings"
)

// peerIP is the request's real TCP peer — the one thing no client can forge.
// Everything that wants "who is calling" goes through Server.clientIP, which
// starts here and only looks at forwarding headers when the peer is a proxy
// the operator has explicitly named.
func peerIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// clientIP resolves the address the Panel treats as the caller's, for audit
// rows, the internal-network gate and the per-IP rate limiters alike — one
// answer, so those three can never disagree about who made a request.
//
// With no KRAKEN_TRUSTED_PROXIES configured (the default) this is the real TCP
// peer and nothing else: `X-Forwarded-For` is a request header like any other,
// and believing it without a trusted set lets anyone claim any address.
//
// The reference deployment, though, puts the Panel behind a Cloudflare Tunnel —
// and behind any reverse proxy the peer is the PROXY for every request, which
// is worse than spoofable: one shared bucket for every rate limiter and one
// address on every audit row. So when the peer is inside a trusted CIDR the
// Panel reads the client from the proxy's own headers: `CF-Connecting-IP` if
// the tunnel set it, otherwise the RIGHTMOST `X-Forwarded-For` entry that is
// not itself trusted. Rightmost, because the list is appended to hop by hop:
// everything to the left of the last trusted hop is whatever the client chose
// to send, and only what a trusted proxy appended can be believed.
func (s *Server) clientIP(r *http.Request) string {
	peer := peerIP(r)
	if len(s.trustedProxies) == 0 || !s.isTrustedProxy(peer) {
		return peer
	}
	if cf := strings.TrimSpace(r.Header.Get("CF-Connecting-IP")); cf != "" {
		if ip := net.ParseIP(cf); ip != nil {
			return ip.String()
		}
	}
	fwd := r.Header.Values("X-Forwarded-For")
	var hops []string
	for _, h := range fwd {
		for _, part := range strings.Split(h, ",") {
			if p := strings.TrimSpace(part); p != "" {
				hops = append(hops, p)
			}
		}
	}
	for i := len(hops) - 1; i >= 0; i-- {
		ip := net.ParseIP(hops[i])
		if ip == nil {
			// A malformed hop is the end of anything believable to its left.
			break
		}
		if s.isTrustedProxy(ip.String()) {
			continue // another of our own proxies; keep walking left
		}
		return ip.String()
	}
	// Every hop was a trusted proxy, or there were none: the peer is the most
	// specific thing we actually know.
	return peer
}

// isTrustedProxy reports whether an address is one of the operator-declared
// reverse proxies.
func (s *Server) isTrustedProxy(addr string) bool {
	ip := net.ParseIP(addr)
	if ip == nil {
		return false
	}
	for _, n := range s.trustedProxies {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}
