package api

import (
	"context"
	"net"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/briggleman/kraken/internal/panel/auth"
	"github.com/briggleman/kraken/internal/panel/cluster"
	"github.com/briggleman/kraken/internal/panel/store"
)

// minPasswordLen is the minimum length for a self-service password change.
const minPasswordLen = 8

// localNodeName is the reserved name for the co-located Agent registered by the
// single-host quickstart path.
const localNodeName = "local"

// passwordChangeAllowed is the set of authenticated endpoints a user with a
// pending must-change-password flag may still call, so they can recover: rotate
// the password, read their own state to drive the UI, and log out.
var passwordChangeAllowed = map[string]bool{
	"/api/v1/auth/change-password": true,
	"/api/v1/auth/logout":          true,
	"/api/v1/auth/me":              true,
	"/api/v1/setup/status":         true,
	"/api/v1/setup/database":       true,
}

// requirePasswordCurrent blocks a user whose password must be changed from doing
// anything except rotating it (and reading their own state). It must run inside
// the requireAuth group. The 403 carries a machine-readable code so the frontend
// can route to the change-password screen.
func (s *Server) requirePasswordCurrent(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := userFrom(r.Context())
		if u != nil && u.MustChangePassword && !passwordChangeAllowed[r.URL.Path] {
			writeJSON(w, http.StatusForbidden, map[string]string{
				"error": "password change required before continuing",
				"code":  "password_change_required",
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// handleChangePassword lets the authenticated user rotate their own password. It
// clears the must-change flag and rotates the session token so the old bearer is
// invalidated.
func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	if u == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	var req changePasswordRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := auth.VerifyPassword(req.CurrentPassword, u.PasswordHash); err != nil {
		s.recordAudit(r, http.StatusUnauthorized, u.Username)
		writeError(w, http.StatusUnauthorized, "current password is incorrect")
		return
	}
	if len(req.NewPassword) < minPasswordLen {
		writeError(w, http.StatusBadRequest, "new password must be at least 8 characters")
		return
	}
	if req.NewPassword == req.CurrentPassword {
		writeError(w, http.StatusBadRequest, "new password must differ from the current password")
		return
	}
	hash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not hash password")
		return
	}
	ctx := r.Context()
	u.PasswordHash = hash
	u.MustChangePassword = false
	if err := s.store.UpdateUser(ctx, u); err != nil {
		writeError(w, http.StatusInternalServerError, "could not update password")
		return
	}
	// Rotate the session: kill the bearer used for this request and mint a new one.
	if old := bearerToken(r); old != "" {
		_ = s.store.DeleteSession(ctx, old)
	}
	token, err := auth.NewSessionToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create session")
		return
	}
	sess := &store.Session{Token: token, UserID: u.ID, ExpiresAt: time.Now().Add(s.sessionTTL(ctx))}
	if err := s.store.CreateSession(ctx, sess); err != nil {
		writeError(w, http.StatusInternalServerError, "could not persist session")
		return
	}
	s.logger.Info("user changed password", "user", u.Username)
	s.recordAudit(r, http.StatusOK, u.Username)
	writeJSON(w, http.StatusOK, loginResponse{Token: token, ExpiresAt: sess.ExpiresAt, User: toUserView(u)})
}

type setupStatusResponse struct {
	AdminMustChangePassword bool `json:"admin_must_change_password"`
	UsingMemory             bool `json:"using_memory"`
	HasNodeOnline           bool `json:"has_node_online"`
	HasSpec                 bool `json:"has_spec"`
	HasServer               bool `json:"has_server"`
	SetupComplete           bool `json:"setup_complete"`
}

// handleSetupStatus reports first-run progress so the UI can drive (and dismiss)
// the onboarding wizard. Computed from existing reads; no new state.
func (s *Server) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	resp := setupStatusResponse{UsingMemory: s.cfg.UsesMemoryStore()}
	if u := userFrom(ctx); u != nil {
		resp.AdminMustChangePassword = u.MustChangePassword
	}
	if nodes, err := s.store.ListNodes(ctx); err == nil {
		for _, n := range nodes {
			if n.Status == cluster.NodeOnline {
				resp.HasNodeOnline = true
				break
			}
		}
	}
	if specs, err := s.store.ListSpecs(ctx); err == nil {
		resp.HasSpec = len(specs) > 0
	}
	if servers, err := s.store.ListServers(ctx); err == nil {
		resp.HasServer = len(servers) > 0
	}
	resp.SetupComplete = !resp.AdminMustChangePassword && resp.HasNodeOnline && resp.HasSpec && resp.HasServer
	writeJSON(w, http.StatusOK, resp)
}

// handleLocalEnroll issues a one-time bootstrap token for the co-located Agent so
// a secure single-host install can self-enroll without an operator-issued token.
// It is gated on a loopback source IP: an off-host caller cannot reach it, so no
// arbitrary host can mint an enrollment token this way.
func (s *Server) handleLocalEnroll(w http.ResponseWriter, r *http.Request) {
	if !isLoopback(clientIP(r)) {
		writeError(w, http.StatusForbidden, "local enrollment is only available from the Panel host")
		return
	}
	if s.caCert == nil {
		writeError(w, http.StatusServiceUnavailable, "agent enrollment is not configured")
		return
	}
	token, exp, err := s.bootstrap.issue(localNodeName, 15*time.Minute)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not issue token")
		return
	}
	s.recordAudit(r, http.StatusCreated, "local-enroll")
	writeJSON(w, http.StatusCreated, map[string]any{
		"token":      token,
		"node_name":  localNodeName,
		"expires_at": exp,
	})
}

// isLoopback reports whether ip is an IPv4/IPv6 loopback address.
func isLoopback(ip string) bool {
	if parsed := net.ParseIP(ip); parsed != nil {
		return parsed.IsLoopback()
	}
	return false
}

// AutoRegisterLocalNode registers the co-located Agent as the "local" node and
// brings it online, so a fresh single-host install reaches a running server with
// no CLI. It is a no-op when quickstart is disabled or any node already exists
// (idempotent across restarts, and it never interferes with a real fleet).
func (s *Server) AutoRegisterLocalNode(ctx context.Context) {
	if !s.cfg.Quickstart {
		return
	}
	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		s.logger.Warn("quickstart: could not list nodes", "err", err)
		return
	}
	if len(nodes) > 0 {
		return
	}
	n := &cluster.Node{
		ID:          uuid.NewString(),
		Name:        localNodeName,
		OS:          cluster.OSLinux, // corrected from the Agent's report on reconcile
		WineEnabled: true,            // allow Wine-on-Linux placements where available
		Status:      cluster.NodeOffline,
		Address:     s.cfg.LocalAgentAddr,
		Ports:       cluster.NewPortPool(cluster.PortRange{Start: 28000, End: 28999}),
	}
	if err := s.store.CreateNode(ctx, n); err != nil {
		s.logger.Warn("quickstart: could not register local node", "err", err)
		return
	}
	s.logger.Info("quickstart: registered co-located agent as the 'local' node", "addr", n.Address)
	if _, err := s.reconcileNode(ctx, n); err != nil {
		s.logger.Info("quickstart: local agent not reachable yet — it will come online on first ping",
			"addr", n.Address, "err", err)
		return
	}
	s.logger.Info("quickstart: local node is online", "addr", n.Address)
}
