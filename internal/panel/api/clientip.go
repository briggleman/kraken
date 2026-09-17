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
// Panel takes the RIGHTMOST `X-Forwarded-For` entry that is not itself trusted.
// Rightmost, because the list is appended to hop by hop: everything to the left
// of the last trusted hop is whatever the client chose to send, and only what a
// trusted proxy appended can be believed.
//
// `X-Forwarded-For` is the ONLY header consulted, and deliberately so.
// `CF-Connecting-IP` looks more direct, but only Cloudflare ever sets it:
// Caddy, nginx and Traefik pass a client-supplied one through untouched, and
// nothing in the request says which of them is in front. Believing it from any
// trusted proxy would let an internet client name its own address to
// `requireInternal` (which would put `/setup/*` and the unauthenticated local
// enrollment behind a header the caller writes), to both rate limiters, and to
// every audit row. Cloudflare appends to `X-Forwarded-For` as well, so the
// rightmost-untrusted rule serves the reference deployment without trusting
// anything a proxy did not append.
func (s *Server) clientIP(r *http.Request) string {
	peer := peerIP(r)
	if len(s.trustedProxies) == 0 || !s.isTrustedProxy(peer) {
		return peer
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

// auditForwardedMaxLen bounds the forwarded chain an audit row carries. The
// header is written by the caller, so the row must not be.
const auditForwardedMaxLen = 256

// forwardedChain returns the raw `X-Forwarded-For` an audit row should carry
// beside its resolved source address, or "" when the row does not need one.
//
// It is populated for exactly one case: the resolved address identifies nobody.
// Either the operator has exempted it from the per-IP limiters, or it is a
// private/gateway address — which, with no trusted proxy configured, is the
// signature of a NAT that overwrote the real client. Behind Cloudflare → a
// proxy → a Docker Desktop published port, the true address is in that chain
// and nowhere else the Panel can reach, so an operator tracing a sign-in has
// the only copy of it here.
//
// It is UNTRUSTED and treated as such: never resolved to, never limited on,
// never compared against an allowlist. Nothing decides anything on it. It is
// bounded, stripped of anything unprintable, and labelled as forensics
// wherever it is displayed.
func (s *Server) forwardedChain(r *http.Request, resolved string) string {
	ip := net.ParseIP(resolved)
	informative := ip != nil && !isGatewayish(ip)
	if informative && !s.ipLimitExempt(resolved) {
		return ""
	}
	var hops []string
	for _, h := range r.Header.Values("X-Forwarded-For") {
		for _, part := range strings.Split(h, ",") {
			if p := strings.TrimSpace(part); p != "" {
				hops = append(hops, p)
			}
		}
	}
	if len(hops) == 0 {
		return ""
	}
	chain := strings.Map(func(c rune) rune {
		if c < 0x20 || c == 0x7f {
			return -1
		}
		return c
	}, strings.Join(hops, ", "))
	if len(chain) > auditForwardedMaxLen {
		// ToValidUTF8 so a cut through a multi-byte rune does not put a broken
		// one in the store and then in the browser.
		chain = strings.ToValidUTF8(chain[:auditForwardedMaxLen], "")
	}
	return chain
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
