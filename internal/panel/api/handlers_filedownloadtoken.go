package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/briggleman/kraken/internal/panel/rbac"
	"github.com/briggleman/kraken/internal/panel/store"
)

// canonicalFilePath normalizes a client-supplied logical path the way the
// Agent's safePath does before it touches disk — Windows separators become
// slashes, the path is cleaned — and rejects anything that escapes upwards.
// It is deliberately NOT a jail: the Agent owns that check and applies it to
// every request regardless of how it was authorized. What this buys is a
// stable spelling to compare against, so "/data//saves" and "/data/saves"
// cannot be used to widen a token past the path it was minted for.
func canonicalFilePath(p string) (string, bool) {
	p = strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
	if p == "" || len(p) > maxTokenPathLen {
		return "", false
	}
	clean := path.Clean(p)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", false
	}
	return clean, true
}

// canonicalFilePaths canonicalizes a whole set, refusing an empty or oversized
// one. Order is preserved: the zip the Agent builds is ordered by this list.
func canonicalFilePaths(in []string) ([]string, bool) {
	if len(in) == 0 || len(in) > maxTokenPaths {
		return nil, false
	}
	out := make([]string, 0, len(in))
	for _, p := range in {
		c, ok := canonicalFilePath(p)
		if !ok {
			return nil, false
		}
		out = append(out, c)
	}
	return out, true
}

type downloadTokenRequest struct {
	// Path mints a token for the raw route (one file's bytes); Paths mints one
	// for the zip route. Exactly one of them — the two shapes mirror the raw
	// and zip routes the token is redeemed on.
	Path  string   `json:"path,omitempty"`
	Paths []string `json:"paths,omitempty"`
}

// serverForRequest loads the server named by {id} and enforces the ownership
// scope, without dialing its Agent (agentForServer does that, and minting a
// token must not depend on the node being reachable).
func (s *Server) serverForRequest(w http.ResponseWriter, r *http.Request) (*store.Server, bool) {
	sv, err := s.store.GetServer(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "server not found")
		return nil, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load server")
		return nil, false
	}
	if !s.authorizeServer(w, r.Context(), sv) {
		return nil, false
	}
	return sv, true
}

// handleCreateDownloadToken mints a one-time, 60-second token for one server
// and one exact path set, bound to the calling user. It exists so the browser
// can hand a download to a plain <a download href>: the session is a Bearer
// header and a navigation cannot carry one, which is why the Files tab used to
// buffer whole payloads into a Blob (#304). The token is never a session
// credential — it authorizes exactly this one download and nothing else.
func (s *Server) handleCreateDownloadToken(w http.ResponseWriter, r *http.Request) {
	var req downloadTokenRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if (req.Path == "") == (len(req.Paths) == 0) {
		writeError(w, http.StatusBadRequest, "exactly one of path or paths is required")
		return
	}
	kind, raw := downloadKindZip, req.Paths
	if req.Path != "" {
		kind, raw = downloadKindRaw, []string{req.Path}
	}
	paths, ok := canonicalFilePaths(raw)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid path")
		return
	}
	sv, ok := s.serverForRequest(w, r)
	if !ok {
		return
	}
	u := userFrom(r.Context())
	// Both are set by requireAuth, so neither is reachable as nil/empty here —
	// a belt on the binding. A grant with no session behind it would outlive
	// the logout that should have killed it, so refuse to mint one.
	sessionHash := sessionHashFrom(r.Context())
	if u == nil || sessionHash == "" {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	token, exp, err := s.downloads.issue(sv.ID, u.ID, sessionHash, kind, paths, downloadTokenTTL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not issue download token")
		return
	}
	// The Panel builds the redemption URL so the query shape lives in one place
	// and the browser only has to navigate to it.
	var redeem string
	if kind == downloadKindRaw {
		redeem = fmt.Sprintf("/api/v1/servers/%s/files/raw?path=%s&token=%s",
			url.PathEscape(sv.ID), url.QueryEscape(paths[0]), url.QueryEscape(token))
	} else {
		redeem = fmt.Sprintf("/api/v1/servers/%s/files/download?token=%s",
			url.PathEscape(sv.ID), url.QueryEscape(token))
	}
	// Audit the mint with what it covers — never the token itself.
	s.recordAuditDetail(r, http.StatusCreated, auditAction(r,
		fmt.Sprintf("mint download token (%s, %s)", kind, pathCount(len(paths)))))
	s.logger.Info("file download token minted", "server", sv.ID, "user", u.Username,
		"kind", kind, "paths", len(paths), "expires_at", exp)
	writeJSON(w, http.StatusCreated, map[string]any{
		"url":                redeem,
		"token":              token,
		"kind":               kind,
		"expires_at":         exp,
		"expires_in_seconds": int(downloadTokenTTL.Seconds()),
	})
}

// tokenOrSession is the whole authorization of a GET download route, and the
// only place it is spelled. The route's ORIGINAL handler is registered
// unchanged behind it; what varies is where the identity in ctx comes from.
//
//   - With ?token= the one-time grant is the whole authority and the
//     Authorization header is not consulted at all, so a live session can never
//     rescue an invalid token. redeemDownloadToken puts the minting user and
//     role into ctx and rewrites the query from the grant.
//   - Without ?token= requireAuth resolves the ordinary Bearer session, exactly
//     as the authenticated group does. allowSession is false on the GET zip
//     route, which has no session form at all (its paths only ever come from a
//     grant; the POST twin inside the group is the session-authenticated one).
//
// After that the SAME chain runs either way, in the order the authenticated
// group uses (see routes()): audit → first-run password gate → permission. The
// point is that there is one chain rather than a hand-rebuilt copy: nothing can
// drift from the group it is meant to mirror. The audit middleware is a
// pass-through on GET today and is included so it stops being one here the day
// it stops being one there.
func (s *Server) tokenOrSession(kind string, allowSession bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		gated := s.auditMiddleware(s.requirePasswordCurrent(
			s.requirePermission(rbac.PermServerFilesRead)(next)))
		sessioned := s.requireAuth(gated)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("token") == "" {
				if !allowSession {
					writeError(w, http.StatusBadRequest, "token query param is required")
					return
				}
				sessioned.ServeHTTP(w, r)
				return
			}
			r, grant, ok := s.redeemDownloadToken(w, r, kind)
			if !ok {
				return
			}
			// Audit the OUTCOME, not the intent: the handler can still 404 on
			// the ownership scope or 502 on an unreachable Agent, and a row
			// that says every redemption streamed is worse than no row at all.
			// The recorder is the audit middleware's (it forwards Unwrap, so
			// the stream still flushes), and the row is deferred so a stream
			// that aborts mid-way (streamChunks panics with
			// http.ErrAbortHandler) is recorded too rather than unwound past.
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			defer func() {
				s.recordAuditDetail(r, rec.status, auditAction(r,
					fmt.Sprintf("download token redeemed (%s, %s)", grant.kind, pathCount(len(grant.paths)))))
			}()
			gated.ServeHTTP(rec, r)
		})
	}
}

// redeemDownloadToken consumes the ?token= on a download route and returns a
// request carrying the minting user, ready for the ordinary handler.
//
// The grant is deleted the moment it is looked up — before any validation, and
// long before a byte streams — so a replayed URL finds nothing whether or not
// the first attempt succeeded. Everything else is then checked against the
// grant rather than the URL: the server must be the one it was minted for, the
// route kind must match, the requested path must equal the bound path exactly,
// and the minting user must still exist, still be enabled, still hold
// server.files.read, and still pass the ownership scope on that server. A
// permission revoked, an account deleted or a server re-owned in the 60 seconds
// since the mint all take effect here.
func (s *Server) redeemDownloadToken(w http.ResponseWriter, r *http.Request, kind string) (*http.Request, downloadGrant, bool) {
	reject := func(reason string) (*http.Request, downloadGrant, bool) {
		// Deliberately opaque to the caller and never logged with the token:
		// a redeemer holding a bad token learns only that it did not work.
		// Debug, not Warn: these routes are reachable with no credentials at
		// all, so every bad token would otherwise be a log line an anonymous
		// caller chose to write. The server id is attacker-chosen too — it is
		// whatever the URL said — so it is capped before it reaches the log.
		s.logger.Debug("file download token rejected", "reason", reason,
			"server", clipForLog(chi.URLParam(r, "id")), "ip", clientIP(r))
		writeError(w, http.StatusUnauthorized, "invalid or expired download token")
		return nil, downloadGrant{}, false
	}
	grant, err := s.downloads.redeem(r.URL.Query().Get("token"))
	if err != nil {
		return reject(err.Error())
	}
	if grant.serverID != chi.URLParam(r, "id") {
		return reject("server mismatch")
	}
	if grant.kind != kind {
		return reject("route mismatch")
	}
	// The raw route names its path in the URL so the request reads honestly,
	// and a named path that is not the bound one is a rejection rather than a
	// silent correction. Note what this check is NOT load-bearing for: the
	// grant is the only source of the path downstream (see the rewrite below),
	// so an absent or mismatched URL path can never widen anything — this
	// refuses the mismatch instead of quietly serving something else.
	if kind == downloadKindRaw {
		if q := r.URL.Query().Get("path"); q != "" {
			c, ok := canonicalFilePath(q)
			if !ok || !slices.Equal([]string{c}, grant.paths) {
				return reject("path mismatch")
			}
		}
	}
	// The grant is no stronger than the session that minted it. Logging out or
	// having a session revoked used to leave a token good for the rest of its
	// 60 seconds; it does not any more. The lookup is by digest — the Panel
	// never held the session token to begin with.
	if live, serr := s.store.SessionExistsByHash(r.Context(), grant.sessionHash); serr != nil || !live {
		return reject("minting session is gone")
	}
	user, err := s.store.GetUser(r.Context(), grant.userID)
	if err != nil || user.Disabled {
		return reject("minting user is gone or disabled")
	}
	role, err := s.store.GetRole(r.Context(), user.RoleID)
	if err != nil {
		return reject("minting user's role is gone")
	}
	if !role.Has(rbac.PermServerFilesRead) {
		return reject("minting user no longer holds " + string(rbac.PermServerFilesRead))
	}
	ctx := context.WithValue(r.Context(), ctxKeyUser, user)
	ctx = context.WithValue(ctx, ctxKeyRole, role)
	ctx = context.WithValue(ctx, ctxKeyDownloadPaths, slices.Clone(grant.paths))
	r = r.WithContext(ctx)
	// The ownership scope is re-checked here, not only downstream: a server
	// handed to another owner inside the 60-second window must fail as a token
	// rejection rather than reach a handler at all. (agentForServer checks it
	// again on the way through, which is what keeps the Bearer path honest.)
	sv, err := s.store.GetServer(r.Context(), grant.serverID)
	if err != nil {
		return reject("server is gone")
	}
	if !s.mayAccessServer(r.Context(), sv) {
		return reject("minting user may no longer reach this server")
	}
	// Downstream may only ever see the bound path: the query is REWRITTEN from
	// the grant rather than trusted as it arrived, so nothing after this point
	// can read a path the token did not authorize — and reordering the check
	// above away would not change that. The spent token is dropped at the same
	// time, so nothing downstream can log it.
	q := r.URL.Query()
	q.Del("token")
	if kind == downloadKindRaw {
		q.Set("path", grant.paths[0])
	}
	r.URL.RawQuery = q.Encode()
	return r, grant, true
}

// downloadPathsFrom returns the path set a redeemed download token authorized,
// or nil when the request did not come through one.
func downloadPathsFrom(ctx context.Context) []string {
	p, _ := ctx.Value(ctxKeyDownloadPaths).([]string)
	return p
}

// maxLoggedIDLen caps an attacker-chosen identifier before it reaches a log
// line. The download routes take a {id} from the URL with no credentials
// behind it, and a log file is not somewhere to let a stranger write 4 KiB.
const maxLoggedIDLen = 64

// clipForLog truncates a caller-supplied value to maxLoggedIDLen, marking that
// it was cut so a clipped id never reads as a real one.
func clipForLog(v string) string {
	if len(v) <= maxLoggedIDLen {
		return v
	}
	return v[:maxLoggedIDLen] + "…"
}

// pathCount renders a path count for the audit log ("1 path" / "3 paths").
func pathCount(n int) string {
	if n == 1 {
		return "1 path"
	}
	return fmt.Sprintf("%d paths", n)
}

// auditAction composes an audit action string: the method and route pattern the
// generic middleware would have written, plus a detail the handler knows.
func auditAction(r *http.Request, detail string) string {
	pattern := r.URL.Path
	if rc := chi.RouteContext(r.Context()); rc != nil && rc.RoutePattern() != "" {
		pattern = rc.RoutePattern()
	}
	return r.Method + " " + strings.TrimPrefix(pattern, "/api/v1") + " — " + detail
}
