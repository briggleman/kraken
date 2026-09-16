package api

import (
	"net"
	"net/http"
)

// parseCIDRList turns a configured list (CIDRs or bare IPs) into nets, logging
// and skipping entries that don't parse. label names the env var for that log
// line. Called once at server construction, for both the /setup/* allowlist —
// where an empty result fails closed, every request denied — and the
// trusted-proxy set, where an empty result means no header is believed.
func (s *Server) parseCIDRList(entries []string, label string) []*net.IPNet {
	nets := make([]*net.IPNet, 0, len(entries))
	for _, e := range entries {
		if _, n, err := net.ParseCIDR(e); err == nil {
			nets = append(nets, n)
			continue
		}
		// Bare IP → host-sized network.
		if ip := net.ParseIP(e); ip != nil {
			bits := 32
			if ip.To4() == nil {
				bits = 128
			}
			nets = append(nets, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
			continue
		}
		s.logger.Warn(label+": ignoring unparseable entry (use CIDRs or IPs — hostnames are not source-verifiable)", "entry", e)
	}
	return nets
}

// requireInternal gates a route group on the request's client address being
// inside the setup allowlist. That address is the real TCP peer unless the peer
// is a configured trusted proxy, in which case it is what that proxy says (see
// clientip.go) — which makes this gate STRONGER behind a reverse proxy, not
// weaker: a Panel fronted by a Cloudflare Tunnel on the same host otherwise
// sees 127.0.0.1 for the whole public internet. With no trusted set nothing
// forwarded is believed, exactly as before.
func (s *Server) requireInternal(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := net.ParseIP(s.clientIP(r))
		allowed := false
		if ip != nil {
			for _, n := range s.setupNets {
				if n.Contains(ip) {
					allowed = true
					break
				}
			}
		}
		if !allowed {
			s.logger.Warn("setup endpoint rejected: source is outside the internal-network allowlist",
				"ip", s.clientIP(r), "path", r.URL.Path)
			s.recordAudit(r, http.StatusForbidden, "setup-external-denied")
			writeError(w, http.StatusForbidden,
				"setup is restricted to the internal network (adjust KRAKEN_SETUP_ALLOWED_CIDRS to change)")
			return
		}
		next.ServeHTTP(w, r)
	})
}
