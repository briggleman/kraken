package api

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/briggleman/kraken/internal/panel/store"
)

// statusRecorder captures the response status code for audit + metrics.
type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if !r.wrote {
		r.status = code
		r.wrote = true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if !r.wrote {
		r.status = http.StatusOK
		r.wrote = true
	}
	return r.ResponseWriter.Write(b)
}

// Hijack lets the wrapped ResponseWriter be used for WebSocket upgrades. Without
// it, wrapping the writer (metrics/audit middleware) would hide the underlying
// http.Hijacker and break the live console/stats stream (HTTP 501).
func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("kraken: underlying ResponseWriter is not an http.Hijacker")
	}
	return hj.Hijack()
}

// Unwrap exposes the wrapped writer to net/http's ResponseController (Flush etc.).
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// auditMiddleware records every mutating (non-GET) authenticated request to the
// audit log: who, what action, target, result. Reads are not audited to keep
// the log signal-rich. Must run inside requireAuth (it reads the user from ctx).
func (s *Server) auditMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		s.recordAudit(r, rec.status, "")
	})
}

// recordAudit appends one audit entry. actorOverride is used for pre-auth events
// (login) where there is no user in context; otherwise the ctx user is used.
func (s *Server) recordAudit(r *http.Request, status int, actorOverride string) {
	actor, actorID := "anonymous", ""
	if u := userFrom(r.Context()); u != nil {
		actor, actorID = u.Username, u.ID
	}
	if actorOverride != "" {
		actor = actorOverride
	}

	pattern := r.URL.Path
	if rc := chi.RouteContext(r.Context()); rc != nil && rc.RoutePattern() != "" {
		pattern = rc.RoutePattern()
	}
	short := strings.TrimPrefix(pattern, "/api/v1")

	e := &store.AuditEntry{
		ID:         uuid.NewString(),
		Time:       time.Now(),
		ActorID:    actorID,
		Actor:      actor,
		Action:     r.Method + " " + short,
		Method:     r.Method,
		Path:       r.URL.Path,
		TargetType: targetType(short),
		TargetID:   chi.URLParam(r, "id"),
		Status:     status,
		IP:         clientIP(r),
	}
	metricsAuditTotal.Add(1)
	if err := s.store.AppendAudit(r.Context(), e); err != nil {
		s.logger.Warn("audit: append failed", "err", err)
	}
}

func targetType(short string) string {
	switch {
	case strings.HasPrefix(short, "/servers"):
		return "server"
	case strings.HasPrefix(short, "/nodes"):
		return "node"
	case strings.HasPrefix(short, "/specs"):
		return "spec"
	case strings.HasPrefix(short, "/users"):
		return "user"
	case strings.HasPrefix(short, "/auth"):
		return "auth"
	default:
		return ""
	}
}

// clientIP returns the peer IP from the TCP connection. It deliberately ignores
// X-Forwarded-For / X-Real-IP — those are client-spoofable when the panel isn't
// behind a trusted proxy, and audit-log integrity depends on the real peer.
func clientIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

func (s *Server) handleListAudit(w http.ResponseWriter, r *http.Request) {
	limit := 200
	entries, err := s.store.ListAudit(r.Context(), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list audit log")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}
