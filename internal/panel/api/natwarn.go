package api

import (
	"fmt"
	"net"
	"sync"
)

// natSampleSize is how many audited requests — authenticated changes and login
// attempts, the two things that write an audit row — have to agree before the
// Panel says anything. Small enough that an operator meets the warning on their
// first afternoon, large enough that a quiet Panel with one admin on it does not
// trip it before anybody else has had a chance to connect.
const natSampleSize = 20

// natDetector spots the one topology the client-address model cannot survive: a
// NAT that rewrites every connection's source to a single gateway. Docker
// Desktop does exactly this to published ports, including the ports a reverse
// proxy on the same host publishes, so `192.168.65.1` arrives as the client for
// the entire internet. Nothing downstream can recover the real address — and
// the symptom is not an error, it is an audit log full of one private address
// and rate limiters that refuse everybody at once.
//
// So the Panel watches its own answers and says so. It samples the resolved
// client of the first natSampleSize audited requests; if every one of them is
// the same private address and no trusted proxy is configured, that is the
// signature, and it logs one Warn pointing at the page that explains the fix.
// A single differing address clears the suspicion for the life of the process:
// a Panel that sees two clients is a Panel that can tell them apart.
type natDetector struct {
	mu sync.Mutex
	// armed is false when KRAKEN_TRUSTED_PROXIES is set — an operator who has
	// named their proxy has already been told this story, and the addresses
	// they see now come from the forwarded chain rather than the peer.
	armed bool
	done  bool // warned, or ruled out; either way there is nothing left to watch
	addr  string
	seen  int
}

// arm enables the detector. Called once at construction.
func (d *natDetector) arm(on bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.armed = on
}

// observe records one resolved client address and reports whether this is the
// sample that completes the pattern — true exactly once per process.
func (d *natDetector) observe(addr string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.armed || d.done {
		return false
	}
	ip := net.ParseIP(addr)
	if ip == nil || !isGatewayish(ip) {
		// A public client address means the Panel can see real callers. Nothing
		// to warn about, now or later.
		d.done = true
		return false
	}
	if d.seen == 0 {
		d.addr = addr
	} else if addr != d.addr {
		d.done = true // two distinct clients: the Panel can tell them apart
		return false
	}
	d.seen++
	if d.seen < natSampleSize {
		return false
	}
	d.done = true
	return true
}

// isGatewayish reports whether an address is one a NAT would substitute for a
// real client: private, loopback or link-local. A public address is a real
// client by definition.
func isGatewayish(ip net.IP) bool {
	return ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast()
}

// noteClientIP feeds the detector from the audit path — every authenticated
// change and every login attempt, which is exactly the traffic whose client the
// Panel is claiming to know. One map-free comparison per audited request, and
// nothing at all once the question is settled.
func (s *Server) noteClientIP(addr string) {
	// An operator who has already exempted this address from the per-IP
	// limiters knows what it is; do not tell them again.
	if s.ipLimitExempt(addr) {
		return
	}
	if !s.nat.observe(addr) {
		return
	}
	// State what was observed, then BOTH readings of it. The heuristic cannot
	// tell a NAT that erased every client from a Panel with one operator on it,
	// and a single admin working from the LAN or from localhost produces the
	// same twenty samples. Asserting the NAT would be wrong for them, and the
	// fix it names — trusting a proxy network that is not there — would be a
	// misconfiguration talked into existence by a log line.
	s.logger.Warn(fmt.Sprintf("the first %d audited requests all came from one private "+
		"address. If more than one person reaches this Panel, a NAT — Docker Desktop's "+
		"published-port gateway, for one — is rewriting every client to that address: "+
		"per-IP rate limits then protect nobody and the audit log records it for everyone. "+
		"Fix: proxy to the Panel over a shared Docker network and name it in "+
		"KRAKEN_TRUSTED_PROXIES, or exempt the address with KRAKEN_RATE_LIMIT_IP_SKIP and "+
		"rely on the per-username login limiter. If you are the only client, this is "+
		"expected and needs nothing.", natSampleSize),
		"client", addr, "samples", natSampleSize,
		"docs", "https://krakenserver.io/wiki/configure/reverse-proxy/")
}
