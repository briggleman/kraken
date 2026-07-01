package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/briggleman/kraken/internal/panel/auth"
	"github.com/briggleman/kraken/internal/panel/rbac"
	"github.com/briggleman/kraken/internal/panel/store"
)

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.store.ListUsers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list users")
		return
	}
	views := make([]userView, 0, len(users))
	for _, u := range users {
		views = append(views, toUserView(u))
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
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req createUserRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Username == "" || req.Password == "" || req.RoleID == "" {
		writeError(w, http.StatusBadRequest, "username, password and role_id are required")
		return
	}
	ctx := r.Context()
	if _, err := s.store.GetRole(ctx, req.RoleID); err != nil {
		writeError(w, http.StatusBadRequest, "unknown role")
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not hash password")
		return
	}
	u := &store.User{
		ID: uuid.NewString(), Username: req.Username, Email: req.Email,
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
	s.logger.Info("user created", "username", u.Username, "role", u.RoleID)
	writeJSON(w, http.StatusCreated, toUserView(u))
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
