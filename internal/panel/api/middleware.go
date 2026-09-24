package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/briggleman/kraken/internal/panel/rbac"
	"github.com/briggleman/kraken/internal/panel/store"
)

type ctxKey int

const (
	ctxKeyUser ctxKey = iota
	ctxKeyRole
	// ctxKeyAuditNote carries the audit middleware's per-request note, which a
	// handler claims when it writes a richer entry itself (see audit.go).
	ctxKeyAuditNote
	// ctxKeyDownloadPaths carries the exact path set a redeemed download token
	// authorized, for the GET zip route that has no body to read them from
	// (see handlers_filedownloadtoken.go).
	ctxKeyDownloadPaths
	// ctxKeySessionHash carries the SHA-256 digest of the bearer token that
	// authenticated the request — the same digest the store keys sessions by,
	// and NEVER the token itself. It exists so something minted inside a
	// session can be tied back to it (download tokens are, and die with it).
	ctxKeySessionHash
)

// authError classifies a session-resolution failure with the HTTP status the
// caller should surface (401/403 or a WS close).
type authError struct {
	status int
	msg    string
}

func (e *authError) Error() string { return e.msg }

func newAuthErr(status int, msg string) *authError { return &authError{status: status, msg: msg} }

// resolveSession validates a session token and returns the associated user and
// role. Used by both the HTTP bearer middleware and the WebSocket (query-token)
// auth path.
func (s *Server) resolveSession(ctx context.Context, token string) (*store.User, *rbac.Role, error) {
	if token == "" {
		return nil, nil, newAuthErr(http.StatusUnauthorized, "missing token")
	}
	sess, err := s.store.GetSession(ctx, token)
	if err != nil {
		return nil, nil, newAuthErr(http.StatusUnauthorized, "invalid session")
	}
	if sess.Expired(time.Now()) {
		_ = s.store.DeleteSession(ctx, token)
		return nil, nil, newAuthErr(http.StatusUnauthorized, "session expired")
	}
	user, err := s.store.GetUser(ctx, sess.UserID)
	if err != nil || user.Disabled {
		return nil, nil, newAuthErr(http.StatusUnauthorized, "account unavailable")
	}
	role, err := s.store.GetRole(ctx, user.RoleID)
	if err != nil {
		return nil, nil, newAuthErr(http.StatusForbidden, "role not found")
	}
	return user, role, nil
}

// requireAuth resolves the bearer token to a live session + user and attaches
// the user and its role to the request context. Missing/invalid/expired tokens
// yield 401.
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		user, role, err := s.resolveSession(r.Context(), token)
		if err != nil {
			ae := err.(*authError)
			writeError(w, ae.status, ae.Error())
			return
		}
		ctx := context.WithValue(r.Context(), ctxKeyUser, user)
		ctx = context.WithValue(ctx, ctxKeyRole, role)
		// The session's identity, as its digest — the same value the store keys
		// sessions by, so anything minted here can ask later whether the session
		// that minted it is still alive. The token itself never enters ctx.
		ctx = context.WithValue(ctx, ctxKeySessionHash, store.HashToken(token))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// requirePermission gates a handler on a single permission. Must be used inside
// a group already wrapped by requireAuth.
func (s *Server) requirePermission(p rbac.Permission) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			role := roleFrom(r.Context())
			if role == nil {
				writeError(w, http.StatusUnauthorized, "not authenticated")
				return
			}
			if !role.Has(p) {
				// Coded, so a refusal here reads the same as one a handler
				// makes on a second permission it checks itself.
				writeJSON(w, http.StatusForbidden, map[string]string{
					"error": "missing permission: " + string(p), "code": "forbidden",
				})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if h == "" {
		return ""
	}
	const prefix = "Bearer "
	if len(h) > len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) {
		return strings.TrimSpace(h[len(prefix):])
	}
	return ""
}

func userFrom(ctx context.Context) *store.User {
	u, _ := ctx.Value(ctxKeyUser).(*store.User)
	return u
}

func roleFrom(ctx context.Context) *rbac.Role {
	r, _ := ctx.Value(ctxKeyRole).(*rbac.Role)
	return r
}

// sessionHashFrom returns the digest of the session token that authenticated
// the request, or "" when it did not come through requireAuth (a redeemed
// download token, for instance, carries a user but no session of its own).
func sessionHashFrom(ctx context.Context) string {
	h, _ := ctx.Value(ctxKeySessionHash).(string)
	return h
}

// mayAccessServer reports whether the request's user may act on sv. A role with
// PermServerAny (Owner/Admin, via the "*"/"server.*" wildcards) reaches every
// server; everyone else is scoped to servers they own. Servers with no owner
// (created before ownership existed) are reachable only by PermServerAny holders.
func (s *Server) mayAccessServer(ctx context.Context, sv *store.Server) bool {
	role := roleFrom(ctx)
	if role == nil {
		return false
	}
	if role.Has(rbac.PermServerAny) {
		return true
	}
	u := userFrom(ctx)
	return u != nil && sv.OwnerID != "" && sv.OwnerID == u.ID
}

// authorizeServer enforces mayAccessServer, writing 404 (not 403 — so we don't
// reveal that another user's server exists) when access is denied.
func (s *Server) authorizeServer(w http.ResponseWriter, ctx context.Context, sv *store.Server) bool {
	if s.mayAccessServer(ctx, sv) {
		return true
	}
	writeError(w, http.StatusNotFound, "server not found")
	return false
}
