package api

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/briggleman/kraken/internal/shared/mtls"
)

// bootstrapRegistry holds one-time Agent enrollment tokens in memory. Tokens are
// short-lived and single-use, so in-memory storage is appropriate; a Panel
// restart simply invalidates outstanding tokens (re-issue to recover).
type bootstrapRegistry struct {
	mu     sync.Mutex
	tokens map[string]bootstrapToken
}

type bootstrapToken struct {
	nodeName  string
	expiresAt time.Time
}

func newBootstrapRegistry() *bootstrapRegistry {
	return &bootstrapRegistry{tokens: map[string]bootstrapToken{}}
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
	// Opportunistically sweep expired tokens.
	for t, bt := range b.tokens {
		if time.Now().After(bt.expiresAt) {
			delete(b.tokens, t)
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

type bootstrapTokenRequest struct {
	NodeName   string `json:"node_name"`
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
	if req.NodeName == "" {
		writeError(w, http.StatusBadRequest, "node_name is required")
		return
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

type enrollRequest struct {
	Token string `json:"token"`
	CSR   string `json:"csr"`
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
	s.logger.Info("agent enrolled", "node", nodeName, "ip", clientIP(r),
		"issued_cert", mtls.SummarizePEM(certPEM), "ca_sha256", mtls.FingerprintPEM(s.caCert))
	s.recordAudit(r, http.StatusOK, "enroll:"+nodeName)
	writeJSON(w, http.StatusOK, map[string]string{
		"certificate": string(certPEM),
		"ca":          string(s.caCert),
	})
}
