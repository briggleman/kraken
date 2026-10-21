package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/briggleman/kraken/internal/panel/rbac"
	"github.com/briggleman/kraken/internal/panel/store"
)

// Shared servers (#146). A server is reached by its owner, by the users it is
// granted to, and by any role with server.any. A grant makes the server visible
// and nothing more: the grantee's role still decides every action on it.
//
// Who may change that: the server's owner and an administrator (server.any +
// user.manage) may grant and revoke; only an administrator may transfer
// ownership. Anyone who cannot reach the server gets 404, as on every other
// server route; someone who can reach it but not manage it gets 403.

// accessVia names how u reaches sv: "any" (server.any), "owner", or "grant".
// Call it only for a server the caller is already known to reach.
func accessVia(u *store.User, role *rbac.Role, sv *store.Server) string {
	switch {
	case role != nil && role.Has(rbac.PermServerAny):
		return "any"
	case u != nil && sv.OwnerID != "" && sv.OwnerID == u.ID:
		return "owner"
	default:
		return "grant"
	}
}

// mayTransferServers reports whether the role may move ownership and manage
// any server's grants: an administrator, holding both server.any and
// user.manage.
func mayTransferServers(role *rbac.Role) bool {
	return role != nil && role.Has(rbac.PermServerAny) && role.Has(rbac.PermUserManage)
}

// mayManageAccess reports whether u may grant and revoke access to sv: its
// owner, whatever their role, or an administrator.
func mayManageAccess(u *store.User, role *rbac.Role, sv *store.Server) bool {
	if mayTransferServers(role) {
		return true
	}
	return u != nil && sv.OwnerID != "" && sv.OwnerID == u.ID
}

// accessUserJSON is a user as the access view names them: enough to show and
// pick, nothing more — this view reaches server owners who cannot list users.
type accessUserJSON struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Role     string `json:"role"`
}

type serverGrantJSON struct {
	accessUserJSON
	// GrantedBy is the username of whoever made the grant, or "" once that
	// account is gone.
	GrantedBy string    `json:"granted_by,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// serverAccessJSON is a server's access as its caller may see it. Grants and
// candidates are for those who may manage them; for everyone else they are
// null.
type serverAccessJSON struct {
	// Owner is null when nobody owns the server.
	Owner       *accessUserJSON   `json:"owner"`
	Access      string            `json:"access"`
	CanManage   bool              `json:"can_manage"`
	CanTransfer bool              `json:"can_transfer"`
	Grants      []serverGrantJSON `json:"grants"`
	// Candidates are the users a grant could go to: everyone but the owner,
	// the users already granted, disabled accounts, and roles that reach
	// every server anyway.
	Candidates []accessUserJSON `json:"candidates"`
}

// accessView builds the access view of sv for the request's user.
func (s *Server) accessView(ctx context.Context, sv *store.Server) (*serverAccessJSON, error) {
	u, role := userFrom(ctx), roleFrom(ctx)
	out := &serverAccessJSON{
		Access:      accessVia(u, role, sv),
		CanManage:   mayManageAccess(u, role, sv),
		CanTransfer: mayTransferServers(role),
	}
	users, err := s.store.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	roles := map[string]*rbac.Role{}
	if rs, rerr := s.store.ListRoles(ctx); rerr == nil {
		for _, r := range rs {
			roles[r.ID] = r
		}
	}
	byID := make(map[string]*store.User, len(users))
	for _, x := range users {
		byID[x.ID] = x
	}
	name := func(x *store.User) accessUserJSON {
		rn := x.RoleID
		if r := roles[x.RoleID]; r != nil {
			rn = r.Name
		}
		return accessUserJSON{ID: x.ID, Username: x.Username, Role: rn}
	}
	if o := byID[sv.OwnerID]; o != nil {
		v := name(o)
		out.Owner = &v
	}
	if !out.CanManage {
		return out, nil
	}
	grants, err := s.store.ListServerGrants(ctx, sv.ID)
	if err != nil {
		return nil, err
	}
	granted := make(map[string]bool, len(grants))
	out.Grants = make([]serverGrantJSON, 0, len(grants))
	for _, g := range grants {
		granted[g.UserID] = true
		x := byID[g.UserID]
		if x == nil {
			continue // deleted between the two reads; its grant went with it
		}
		gj := serverGrantJSON{accessUserJSON: name(x), CreatedAt: g.CreatedAt}
		if by := byID[g.GrantedBy]; by != nil {
			gj.GrantedBy = by.Username
		}
		out.Grants = append(out.Grants, gj)
	}
	out.Candidates = []accessUserJSON{}
	for _, x := range users {
		if x.ID == sv.OwnerID || granted[x.ID] || x.Disabled {
			continue
		}
		if r := roles[x.RoleID]; r != nil && r.Has(rbac.PermServerAny) {
			continue
		}
		out.Candidates = append(out.Candidates, name(x))
	}
	return out, nil
}

// loadReachableServer loads the {id} server and enforces the access rule,
// writing the 404 or 500 itself. nil means the response is written.
func (s *Server) loadReachableServer(w http.ResponseWriter, r *http.Request) *store.Server {
	sv, err := s.store.GetServer(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "server not found")
		return nil
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not get server")
		return nil
	}
	if !s.authorizeServer(w, r.Context(), sv) {
		return nil
	}
	return sv
}

// writeAccessView answers with the server's access view as it now stands.
func (s *Server) writeAccessView(w http.ResponseWriter, ctx context.Context, sv *store.Server) {
	view, err := s.accessView(ctx, sv)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read server access")
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// handleGetServerAccess answers GET /servers/{id}/access.
func (s *Server) handleGetServerAccess(w http.ResponseWriter, r *http.Request) {
	sv := s.loadReachableServer(w, r)
	if sv == nil {
		return
	}
	s.writeAccessView(w, r.Context(), sv)
}

// handleGrantServerAccess answers PUT /servers/{id}/access/{userID}: grant the
// user access. Granting again is not an error.
func (s *Server) handleGrantServerAccess(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sv := s.loadReachableServer(w, r)
	if sv == nil {
		return
	}
	u := userFrom(ctx)
	if !mayManageAccess(u, roleFrom(ctx), sv) {
		writeError(w, http.StatusForbidden, "only this server's owner or an administrator can share it")
		return
	}
	target, err := s.store.GetUser(ctx, chi.URLParam(r, "userID"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not get user")
		return
	}
	if target.ID == sv.OwnerID {
		writeError(w, http.StatusConflict, target.Username+" already owns this server")
		return
	}
	g := &store.ServerGrant{ServerID: sv.ID, UserID: target.ID, CreatedAt: time.Now().UTC()}
	if u != nil {
		g.GrantedBy = u.ID
	}
	if err := s.store.GrantServer(ctx, g); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "server or user not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not grant access")
		return
	}
	s.recordAuditDetail(r, http.StatusOK, "server.grant:"+target.Username)
	s.writeAccessView(w, ctx, sv)
}

// handleRevokeServerAccess answers DELETE /servers/{id}/access/{userID}.
func (s *Server) handleRevokeServerAccess(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sv := s.loadReachableServer(w, r)
	if sv == nil {
		return
	}
	if !mayManageAccess(userFrom(ctx), roleFrom(ctx), sv) {
		writeError(w, http.StatusForbidden, "only this server's owner or an administrator can change who it is shared with")
		return
	}
	userID := chi.URLParam(r, "userID")
	username := userID
	if target, err := s.store.GetUser(ctx, userID); err == nil {
		username = target.Username
	}
	if err := s.store.RevokeServerGrant(ctx, sv.ID, userID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "that user has no grant on this server")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not revoke access")
		return
	}
	s.recordAuditDetail(r, http.StatusOK, "server.revoke:"+username)
	s.writeAccessView(w, ctx, sv)
}

type transferServerRequest struct {
	UserID string `json:"user_id"`
}

// handleTransferServer answers PUT /servers/{id}/owner: move ownership to
// another user. Administrators only (the route requires server.any and
// user.manage). The new owner's grant, if they held one, goes — ownership
// says it all. The previous owner keeps nothing they are not granted.
func (s *Server) handleTransferServer(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sv := s.loadReachableServer(w, r)
	if sv == nil {
		return
	}
	var req transferServerRequest
	if err := decodeJSON(r, &req); err != nil || req.UserID == "" {
		writeError(w, http.StatusBadRequest, "user_id is required")
		return
	}
	target, err := s.store.GetUser(ctx, req.UserID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not get user")
		return
	}
	if target.ID != sv.OwnerID {
		if err := s.store.SetServerOwner(ctx, sv.ID, target.ID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusNotFound, "server or user not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "could not transfer the server")
			return
		}
		if err := s.store.RevokeServerGrant(ctx, sv.ID, target.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
			s.logger.Warn("transfer: could not drop the new owner's grant", "server", sv.ID, "user", target.Username, "err", err)
		}
		sv.OwnerID = target.ID
		s.recordAuditDetail(r, http.StatusOK, "server.transfer:"+target.Username)
	}
	s.writeAccessView(w, ctx, sv)
}
