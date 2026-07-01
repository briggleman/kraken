package api

import (
	"net/http"
	"time"

	"github.com/briggleman/kraken/internal/panel/auth"
	"github.com/briggleman/kraken/internal/panel/store"
)

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type loginResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	User      userView  `json:"user"`
}

type userView struct {
	ID                 string `json:"id"`
	Username           string `json:"username"`
	Email              string `json:"email"`
	RoleID             string `json:"role_id"`
	Disabled           bool   `json:"disabled"`
	MustChangePassword bool   `json:"must_change_password"`
}

func toUserView(u *store.User) userView {
	return userView{
		ID:                 u.ID,
		Username:           u.Username,
		Email:              u.Email,
		RoleID:             u.RoleID,
		Disabled:           u.Disabled,
		MustChangePassword: u.MustChangePassword,
	}
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Username == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "username and password are required")
		return
	}

	ctx := r.Context()
	user, err := s.store.GetUserByUsername(ctx, req.Username)
	// Always run a verify to keep timing roughly constant whether or not the
	// user exists, then fail uniformly to avoid username enumeration.
	if err != nil {
		_ = auth.VerifyPassword(req.Password, dummyHash)
		s.recordAudit(r, http.StatusUnauthorized, req.Username)
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if verr := auth.VerifyPassword(req.Password, user.PasswordHash); verr != nil {
		s.recordAudit(r, http.StatusUnauthorized, req.Username)
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if user.Disabled {
		s.recordAudit(r, http.StatusForbidden, req.Username)
		writeError(w, http.StatusForbidden, "account disabled")
		return
	}

	token, err := auth.NewSessionToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create session")
		return
	}
	sess := &store.Session{Token: token, UserID: user.ID, ExpiresAt: time.Now().Add(s.sessionTTL(ctx))}
	if err := s.store.CreateSession(ctx, sess); err != nil {
		writeError(w, http.StatusInternalServerError, "could not persist session")
		return
	}

	s.logger.Info("user logged in", "user", user.Username)
	s.recordAudit(r, http.StatusOK, user.Username)
	writeJSON(w, http.StatusOK, loginResponse{Token: token, ExpiresAt: sess.ExpiresAt, User: toUserView(user)})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if token := bearerToken(r); token != "" {
		_ = s.store.DeleteSession(r.Context(), token)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "logged out"})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	role := roleFrom(r.Context())
	if user == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	resp := map[string]any{"user": toUserView(user)}
	if role != nil {
		resp["role"] = role
	}
	writeJSON(w, http.StatusOK, resp)
}

// dummyHash is a precomputed argon2id hash of a random value, used to equalize
// login timing when the username is unknown. Its plaintext is irrelevant.
var dummyHash = mustDummyHash()

func mustDummyHash() string {
	h, err := auth.HashPassword("kraken-timing-equalizer")
	if err != nil {
		// Fall back to a structurally valid constant; VerifyPassword will simply
		// return an error, which is fine for the timing path.
		return "$argon2id$v=19$m=65536,t=1,p=4$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	}
	return h
}
