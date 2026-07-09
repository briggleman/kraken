package api

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/briggleman/kraken/internal/shared/mtls"
)

// redeemedTTL is how long a redeemed token's record is kept so the setup UI
// can observe the "agent enrolled" transition after the fact.
const redeemedTTL = 30 * time.Minute

// bootstrapRegistry holds one-time Agent enrollment tokens in memory. Tokens are
// short-lived and single-use, so in-memory storage is appropriate; a Panel
// restart simply invalidates outstanding tokens (re-issue to recover).
// Redeemed tokens are remembered briefly so the setup wizard can poll for the
// moment an agent enrolls (and learn its advertised hosts).
type bootstrapRegistry struct {
	mu       sync.Mutex
	tokens   map[string]bootstrapToken
	redeemed map[string]redeemedToken
}

type bootstrapToken struct {
	nodeName  string
	expiresAt time.Time
}

type redeemedToken struct {
	nodeName   string
	ip         string
	hosts      []string // extra SANs the agent requested (its reachable IPs/DNS names)
	agentPort  int      // the gRPC port the agent reports it will serve on (0 = unknown)
	redeemedAt time.Time
}

func newBootstrapRegistry() *bootstrapRegistry {
	return &bootstrapRegistry{
		tokens:   map[string]bootstrapToken{},
		redeemed: map[string]redeemedToken{},
	}
}

func (b *bootstrapRegistry) issue(nodeName string, ttl time.Duration) (string, time.Time, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", time.Time{}, err
	}
	token := hex.EncodeToString(buf)
	exp := time.Now().Add(ttl)
	b.mu.Lock()
	b.tokens[token] = bootstrapToken{nodeName: nodeName, expiresAt: exp}
	// Opportunistically sweep expired tokens + stale redeemed records.
	for t, bt := range b.tokens {
		if time.Now().After(bt.expiresAt) {
			delete(b.tokens, t)
		}
	}
	for t, rt := range b.redeemed {
		if time.Since(rt.redeemedAt) > redeemedTTL {
			delete(b.redeemed, t)
		}
	}
	b.mu.Unlock()
	return token, exp, nil
}

// redeem validates and consumes a token. It returns the node name the token was
// issued for, or an error if the token is unknown/expired.
func (b *bootstrapRegistry) redeem(token string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	bt, ok := b.tokens[token]
	if !ok {
		return "", fmt.Errorf("unknown or already-used token")
	}
	delete(b.tokens, token) // one-time use
	if time.Now().After(bt.expiresAt) {
		return "", fmt.Errorf("token expired")
	}
	return bt.nodeName, nil
}

// recordRedeemed remembers a successful enrollment so status() can report it.
func (b *bootstrapRegistry) recordRedeemed(token, nodeName, ip string, hosts []string, agentPort int) {
	b.mu.Lock()
	b.redeemed[token] = redeemedToken{nodeName: nodeName, ip: ip, hosts: hosts, agentPort: agentPort, redeemedAt: time.Now()}
	b.mu.Unlock()
}

// enrollState is the setup wizard's view of a token's lifecycle.
type enrollState struct {
	Status     string    `json:"status"` // pending | redeemed | expired
	NodeName   string    `json:"node_name,omitempty"`
	IP         string    `json:"ip,omitempty"`
	Hosts      []string  `json:"hosts,omitempty"`
	AgentPort  int       `json:"agent_port,omitempty"` // agent-reported gRPC port for the registration prefill
	ExpiresAt  time.Time `json:"expires_at,omitempty"`
	RedeemedAt time.Time `json:"redeemed_at,omitempty"`
}

func (b *bootstrapRegistry) status(token string) enrollState {
	b.mu.Lock()
	defer b.mu.Unlock()
	if rt, ok := b.redeemed[token]; ok {
		return enrollState{Status: "redeemed", NodeName: rt.nodeName, IP: rt.ip, Hosts: rt.hosts, AgentPort: rt.agentPort, RedeemedAt: rt.redeemedAt}
	}
	if bt, ok := b.tokens[token]; ok {
		if time.Now().After(bt.expiresAt) {
			return enrollState{Status: "expired", NodeName: bt.nodeName}
		}
		return enrollState{Status: "pending", NodeName: bt.nodeName, ExpiresAt: bt.expiresAt}
	}
	// Unknown = expired-and-swept, never issued, or the Panel restarted.
	return enrollState{Status: "expired"}
}

type bootstrapTokenRequest struct {
	NodeName   string `json:"node_name"` // optional label, used for audit logging only
	TTLSeconds int    `json:"ttl_seconds"`
}

func (s *Server) handleCreateBootstrapToken(w http.ResponseWriter, r *http.Request) {
	if s.caCert == nil {
		writeError(w, http.StatusServiceUnavailable, "agent enrollment is not configured (set KRAKEN_CA_CERT/KRAKEN_CA_KEY)")
		return
	}
	var req bootstrapTokenRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	// The name is an audit-log label, not an identity: the node's real name
	// comes from the agent itself at registration (KRAKEN_NODE_ID).
	if req.NodeName == "" {
		req.NodeName = "remote-agent"
	}
	ttl := 15 * time.Minute
	if req.TTLSeconds > 0 {
		ttl = time.Duration(req.TTLSeconds) * time.Second
	}
	token, exp, err := s.bootstrap.issue(req.NodeName, ttl)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not issue token")
		return
	}
	s.logger.Info("bootstrap token issued", "node", req.NodeName, "expires_at", exp, "ip", clientIP(r))
	writeJSON(w, http.StatusCreated, map[string]any{
		"token":      token,
		"node_name":  req.NodeName,
		"expires_at": exp,
	})
}

// handleEnrollStatus reports a bootstrap token's lifecycle so the setup wizard
// can show live progress (pending → redeemed) and prefill the registration
// address from the hosts the agent baked into its cert.
func (s *Server) handleEnrollStatus(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		writeError(w, http.StatusBadRequest, "token query param is required")
		return
	}
	writeJSON(w, http.StatusOK, s.bootstrap.status(token))
}

type enrollRequest struct {
	Token string `json:"token"`
	CSR   string `json:"csr"`
	// AgentPort is the gRPC port the agent will serve on (optional; default
	// 9090). Reported so node registration can be prefilled host:port —
	// several agents can share one IP on different ports.
	AgentPort int `json:"agent_port,omitempty"`
}

// handleEnroll is the Agent enrollment endpoint. It is authenticated solely by a
// one-time bootstrap token (no session), since the Agent has no credentials yet.
// On success it signs the submitted CSR and returns the issued cert + CA so the
// Agent can speak mutual TLS to the Panel. Re-enrolling rotates the cert.
func (s *Server) handleEnroll(w http.ResponseWriter, r *http.Request) {
	if s.caCert == nil {
		writeError(w, http.StatusServiceUnavailable, "agent enrollment is not configured")
		return
	}
	var req enrollRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	nodeName, err := s.bootstrap.redeem(req.Token)
	if err != nil {
		s.logger.Warn("agent enrollment rejected", "err", err, "ip", clientIP(r))
		s.recordAudit(r, http.StatusUnauthorized, "enroll")
		writeError(w, http.StatusUnauthorized, "invalid bootstrap token: "+err.Error())
		return
	}
	certPEM, err := mtls.SignAgentCSR(s.caCert, s.caKey, []byte(req.CSR), mtls.DefaultAgentCertTTL)
	if err != nil {
		s.logger.Warn("agent enrollment: CSR rejected", "node", nodeName, "ip", clientIP(r), "err", err)
		writeError(w, http.StatusBadRequest, "could not sign CSR: "+err.Error())
		return
	}
	port := req.AgentPort
	if port <= 0 || port > 65535 {
		port = 9090
	}
	s.bootstrap.recordRedeemed(req.Token, nodeName, clientIP(r), enrollHosts(certPEM), port)
	s.logger.Info("agent enrolled", "node", nodeName, "ip", clientIP(r),
		"issued_cert", mtls.SummarizePEM(certPEM), "ca_sha256", mtls.FingerprintPEM(s.caCert))
	s.recordAudit(r, http.StatusOK, "enroll:"+nodeName)
	writeJSON(w, http.StatusOK, map[string]string{
		"certificate": string(certPEM),
		"ca":          string(s.caCert),
	})
}

// enrollHosts extracts the operator-supplied SANs from an issued agent cert —
// the `-hosts` values passed to `krakenctl enroll`, i.e. the addresses the
// agent expects to be reached at. Baked-in logical/loopback names are dropped,
// and IPs are ordered BEFORE DNS names: the first entry seeds the registration
// prefill, and an IP is always dialable while a name (worst case a bare
// computer name) may not resolve from the Panel at all.
func enrollHosts(certPEM []byte) []string {
	skip := map[string]bool{mtls.AgentServerName: true, "localhost": true, "127.0.0.1": true, "::1": true}
	var ips, names []string
	for _, h := range mtls.SANHosts(certPEM) {
		if skip[h] {
			continue
		}
		if net.ParseIP(h) != nil {
			ips = append(ips, h)
		} else {
			names = append(names, h)
		}
	}
	return append(ips, names...)
}
