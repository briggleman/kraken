package api

import (
	"net"
	"net/http"
)

// parseSetupCIDRs turns the configured allowlist (CIDRs or bare IPs) into
// nets, logging and skipping entries that don't parse. Called once at server
// construction; an empty result fails closed (every request denied) rather
// than open.
func (s *Server) parseSetupCIDRs(entries []string) []*net.IPNet {
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
		s.logger.Warn("KRAKEN_SETUP_ALLOWED_CIDRS: ignoring unparseable entry (use CIDRs or IPs — hostnames are not source-verifiable)", "entry", e)
	}
	return nets
}

// requireInternal gates a route group on the request's REAL TCP peer being
// inside the setup allowlist. Deliberately ignores X-Forwarded-For (client-
// spoofable); a panel behind a trusted reverse proxy on another host should
// widen KRAKEN_SETUP_ALLOWED_CIDRS to include the proxy instead.
func (s *Server) requireInternal(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := net.ParseIP(clientIP(r))
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
				"ip", clientIP(r), "path", r.URL.Path)
			s.recordAudit(r, http.StatusForbidden, "setup-external-denied")
			writeError(w, http.StatusForbidden,
				"setup is restricted to the internal network (adjust KRAKEN_SETUP_ALLOWED_CIDRS to change)")
			return
		}
		next.ServeHTTP(w, r)
	})
}
