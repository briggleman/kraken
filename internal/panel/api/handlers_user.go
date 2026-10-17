package api

import (
	"errors"
	"net/http"
	netmail "net/mail"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/briggleman/kraken/internal/panel/auth"
	"github.com/briggleman/kraken/internal/panel/mail"
	"github.com/briggleman/kraken/internal/panel/rbac"
	"github.com/briggleman/kraken/internal/panel/store"
)

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.store.ListUsers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list users")
		return
	}
	// One read for every open invite, so the list shows who has not chosen a
	// password yet without a query per row.
	invites := map[string]*store.Invite{}
	if list, err := s.store.ListLatestInvites(r.Context()); err == nil {
		for _, inv := range list {
			invites[inv.UserID] = inv
		}
	}
	now := time.Now()
	views := make([]userView, 0, len(users))
	for _, u := range users {
		v := toUserView(u)
		v.Invite = inviteViewOf(invites[u.ID], now)
		views = append(views, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": views})
}

func (s *Server) handleListRoles(w http.ResponseWriter, r *http.Request) {
	roles, err := s.store.ListRoles(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list roles")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"roles": roles})
}

// handleListPermissions returns the full permission vocabulary for the role UI.
func (s *Server) handleListPermissions(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"permissions": rbac.AllPermissions()})
}

type createUserRequest struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Password string `json:"password"`
	RoleID   string `json:"role_id"`
	// Invite, when true, emails the new person a one-time link to choose
	// their own password instead of taking one here. Password is then
	// ignored; Email is required; outbound email and the public address
	// must be configured (409 says which is missing).
	Invite bool `json:"invite"`
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req createUserRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Username == "" || req.RoleID == "" || (req.Password == "" && !req.Invite) {
		writeError(w, http.StatusBadRequest, "username, password and role_id are required")
		return
	}
	ctx := r.Context()
	var (
		inviteCfg    mail.Config
		invitePublic string
	)
	if req.Invite {
		if _, err := netmail.ParseAddress(strings.TrimSpace(req.Email)); err != nil {
			writeError(w, http.StatusBadRequest, "an invite needs an email address to go to")
			return
		}
		var why string
		inviteCfg, invitePublic, why = s.inviteReady(ctx)
		if why != "" {
			writeError(w, http.StatusConflict, why)
			return
		}
	}
	if _, err := s.store.GetRole(ctx, req.RoleID); err != nil {
		writeError(w, http.StatusBadRequest, "unknown role")
		return
	}
	password := req.Password
	if req.Invite {
		// The account exists before the person has chosen anything, so it
		// gets a password nobody knows: 32 random bytes, hashed and dropped.
		// The invite is the only way in until they set their own.
		secret, err := auth.NewSessionToken()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not create user")
			return
		}
		password = secret
		// The send comes after the row is written; spend the budget first so
		// a refused send does not leave an account nobody meant to create yet.
		if s.rejectMailSend(w, r) {
			return
		}
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not hash password")
		return
	}
	u := &store.User{
		ID: uuid.NewString(), Username: req.Username, Email: strings.TrimSpace(req.Email),
		PasswordHash: hash, RoleID: req.RoleID, CreatedAt: time.Now().UTC(),
	}
	if err := s.store.CreateUser(ctx, u); err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeError(w, http.StatusConflict, "username already taken")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not create user")
		return
	}
	s.logger.Info("user created", "username", u.Username, "role", u.RoleID, "invited", req.Invite)
	if !req.Invite {
		writeJSON(w, http.StatusCreated, toUserView(u))
		return
	}
	view := toUserView(u)
	inv, err := s.sendInvite(ctx, inviteCfg, invitePublic, u, userFrom(ctx))
	view.Invite = inviteViewOf(inv, time.Now())
	if err != nil {
		// The account exists; the email did not go out. Say both, so the
		// admin resends rather than creating the user a second time.
		s.recordAuditDetail(r, statusUpstream, "invite.sent:"+u.Username)
		writeJSON(w, statusUpstream, map[string]any{
			"error": "the account was created but the invite email did not go out: " + err.Error() + " — fix the relay settings and use resend",
			"user":  view,
		})
		return
	}
	s.recordAuditDetail(r, http.StatusCreated, "invite.sent:"+u.Username)
	writeJSON(w, http.StatusCreated, view)
}

type updateUserRequest struct {
	Email    *string `json:"email"`
	RoleID   *string `json:"role_id"`
	Disabled *bool   `json:"disabled"`
}

func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	var req updateUserRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	u, err := s.store.GetUser(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load user")
		return
	}
	if req.Email != nil {
		u.Email = *req.Email
	}
	if req.RoleID != nil {
		if _, rerr := s.store.GetRole(ctx, *req.RoleID); rerr != nil {
			writeError(w, http.StatusBadRequest, "unknown role")
			return
		}
		u.RoleID = *req.RoleID
	}
	if req.Disabled != nil {
		// Don't let an admin disable their own account and lock themselves out.
		if cur := userFrom(ctx); *req.Disabled && cur != nil && cur.ID == u.ID {
			writeError(w, http.StatusBadRequest, "you cannot disable your own account")
			return
		}
		u.Disabled = *req.Disabled
	}
	if err := s.store.UpdateUser(ctx, u); err != nil {
		writeError(w, http.StatusInternalServerError, "could not update user")
		return
	}
	// A disabled user's phones stop receiving alerts now, not when the alert
	// pipeline next notices the account. Run on every disable, not only the
	// transition, so a request that failed here can simply be sent again.
	if u.Disabled {
		n, err := s.store.DeleteDevicesByUser(ctx, u.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "user disabled, but their push devices could not be revoked; disable again to retry")
			return
		}
		if n > 0 {
			s.logger.Info("push devices revoked: user disabled", "username", u.Username, "devices", n)
		}
		// An open invite is a way into a disabled account; close it too.
		// (A live invite already refuses a disabled user; this keeps the row
		// honest as well.)
		if n, err := s.store.RevokeInvitesByUser(ctx, u.ID, time.Now().UTC()); err != nil {
			s.logger.Warn("user disabled but their invite could not be revoked", "username", u.Username, "err", err)
		} else if n > 0 {
			s.logger.Info("invite revoked: user disabled", "username", u.Username)
		}
	}
	writeJSON(w, http.StatusOK, toUserView(u))
}

type resetPasswordRequest struct {
	Password string `json:"password"`
}

func (s *Server) handleResetPassword(w http.ResponseWriter, r *http.Request) {
	var req resetPasswordRequest
	if err := decodeJSON(r, &req); err != nil || req.Password == "" {
		writeError(w, http.StatusBadRequest, "password is required")
		return
	}
	ctx := r.Context()
	u, err := s.store.GetUser(ctx, chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load user")
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not hash password")
		return
	}
	u.PasswordHash = hash
	if err := s.store.UpdateUser(ctx, u); err != nil {
		writeError(w, http.StatusInternalServerError, "could not reset password")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "password reset"})
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	if current := userFrom(ctx); current != nil && current.ID == id {
		writeError(w, http.StatusBadRequest, "you cannot delete your own account")
		return
	}
	err := s.store.DeleteUser(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not delete user")
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}
