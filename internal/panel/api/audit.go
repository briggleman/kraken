package api

import (
	"bufio"
	"context"
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
		note := &auditNote{}
		r = r.WithContext(context.WithValue(r.Context(), ctxKeyAuditNote, note))
		next.ServeHTTP(rec, r)
		// A handler that wrote its own, richer entry (recordAuditDetail) has
		// already said everything this one would — one request, one row.
		if note.claimed {
			return
		}
		s.recordAudit(r, rec.status, "")
	})
}

// auditNote lets a handler tell the middleware that it wrote the request's
// audit entry itself. Carried by pointer in the request context.
type auditNote struct{ claimed bool }

// recordAuditDetail appends one audit entry with a caller-supplied action
// string — how a handler says more than "POST /some/path" (which paths a
// download token covers, that one was redeemed). Inside the audit middleware
// it also claims the request's entry, so an enriched mint is one row, not two.
func (s *Server) recordAuditDetail(r *http.Request, status int, action string) {
	// Claim the middleware's row only once this one is actually on the record:
	// a store that refused the write must still leave the generic entry to be
	// written, rather than trading one row for none.
	if s.appendAudit(r, status, "", action) {
		if n, _ := r.Context().Value(ctxKeyAuditNote).(*auditNote); n != nil {
			n.claimed = true
		}
	}
}

// recordAudit appends one audit entry. actorOverride is used for pre-auth events
// (login) where there is no user in context; otherwise the ctx user is used.
func (s *Server) recordAudit(r *http.Request, status int, actorOverride string) {
	s.appendAudit(r, status, actorOverride, "")
}

// appendAudit is the one writer. actionOverride replaces the derived
// "METHOD /route" action when a handler has something more specific to say.
// It reports whether the entry reached the store.
func (s *Server) appendAudit(r *http.Request, status int, actorOverride, actionOverride string) bool {
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

	action := r.Method + " " + short
	if actionOverride != "" {
		action = actionOverride
	}

	ip := s.clientIP(r)
	s.noteClientIP(ip)

	e := &store.AuditEntry{
		ID:         uuid.NewString(),
		Time:       time.Now(),
		ActorID:    actorID,
		Actor:      actor,
		Action:     action,
		Method:     r.Method,
		Path:       r.URL.Path,
		TargetType: targetType(short),
		TargetID:   chi.URLParam(r, "id"),
		Status:     status,
		IP:         ip,
		// Only when the resolved address is one that cannot identify anybody —
		// a NAT gateway, or an address the operator has exempted. There the
		// chain is the only place a real client address survives, and an
		// operator diagnosing a sign-in has nothing else to read. It is
		// attacker-writable and labelled as such wherever it is shown.
		ForwardedFor: s.forwardedChain(r, ip),
	}
	metricsAuditTotal.Add(1)
	// The append does NOT inherit the request's cancellation. An entry is
	// written after the response — and the cases most worth recording are
	// exactly the ones where the client hung up first: a cancelled multi-GB
	// download, an abandoned POST. On the request context those rows would be
	// dropped to a Warn by the store. Values (and so any request-scoped
	// tracing) are kept; a short deadline of its own keeps a wedged store from
	// pinning the handler goroutine now that the client cannot free it.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer cancel()
	if err := s.store.AppendAudit(ctx, e); err != nil {
		s.logger.Warn("audit: append failed", "err", err)
		return false
	}
	return true
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

func (s *Server) handleListAudit(w http.ResponseWriter, r *http.Request) {
	limit := 200
	entries, err := s.store.ListAudit(r.Context(), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list audit log")
		return
	}
	// retention_days travels with the list so the console can say how long these
	// entries live without hard-coding a number the Panel might not be keeping
	// to. 0 means nothing is pruned.
	writeJSON(w, http.StatusOK, map[string]any{
		"entries":        entries,
		"retention_days": s.cfg.AuditRetentionDays,
	})
}
