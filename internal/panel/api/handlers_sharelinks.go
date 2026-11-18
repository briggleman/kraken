package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/briggleman/kraken/internal/panel/auth"
	"github.com/briggleman/kraken/internal/panel/store"
)

// Server share links (#475): a read-only capability link to one server's
// public card. This file is the operator's side — mint, list, revoke. The
// public page the link opens is served elsewhere and never consumes a link.

const (
	// shareLinkLabelMax bounds the operator's own name for a link.
	shareLinkLabelMax = 60
	// shareLinkMaxDays bounds a chosen expiry; 0 means the link lives until
	// it is revoked.
	shareLinkMaxDays = 365
	// shareLinksPerServer caps the open links on one server. A link is cheap
	// to make and lives forever by default, so an unbounded list is a list
	// nobody reads; revoking one makes room.
	shareLinksPerServer = 25
	// shareLinkPruneAfter is how long a revoked or expired link row is kept
	// before pruneShareLinksOnce removes it.
	shareLinkPruneAfter = 30 * 24 * time.Hour
)

// shareLinkJSON is a link as the operator sees it. URL is set only in the
// answer to the mint: the token is never stored, so it can never be shown
// again.
type shareLinkJSON struct {
	ID           string     `json:"id"`
	Label        string     `json:"label"`
	ShowAddress  bool       `json:"show_address"`
	ShowPassword bool       `json:"show_password"`
	CreatedAt    time.Time  `json:"created_at"`
	CreatedBy    string     `json:"created_by,omitempty"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
	LastSeenAt   *time.Time `json:"last_seen_at,omitempty"`
	// State is "live" or "expired".
	State string `json:"state"`
	URL   string `json:"url,omitempty"`
}

func (s *Server) shareLinkView(ctx context.Context, l *store.ShareLink, now time.Time, names map[string]string) shareLinkJSON {
	v := shareLinkJSON{
		ID: l.ID, Label: l.Label, ShowAddress: l.ShowAddress, ShowPassword: l.ShowPassword,
		CreatedAt: l.CreatedAt, ExpiresAt: l.ExpiresAt, LastSeenAt: l.LastSeenAt, State: "live",
	}
	if !l.Live(now) {
		v.State = "expired"
	}
	if l.CreatedBy != "" {
		if n, ok := names[l.CreatedBy]; ok {
			v.CreatedBy = n
		} else if u, err := s.store.GetUser(ctx, l.CreatedBy); err == nil {
			v.CreatedBy = u.Username
			names[l.CreatedBy] = u.Username
		}
	}
	return v
}

// handleListShareLinks answers GET /servers/{id}/share-links: the server's
// links that are not revoked, newest first.
func (s *Server) handleListShareLinks(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sv := s.loadReachableServer(w, r)
	if sv == nil {
		return
	}
	links, err := s.store.ListShareLinks(ctx, sv.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list share links")
		return
	}
	now := time.Now().UTC()
	names := map[string]string{}
	out := make([]shareLinkJSON, 0, len(links))
	for _, l := range links {
		out = append(out, s.shareLinkView(ctx, l, now, names))
	}
	writeJSON(w, http.StatusOK, map[string]any{"links": out, "public_url_set": s.publicURL(ctx) != ""})
}

// shareLinkRequest is the body of a mint.
type shareLinkRequest struct {
	Label        string `json:"label"`
	ShowAddress  bool   `json:"show_address"`
	ShowPassword bool   `json:"show_password"`
	// ExpiresInDays is 0 for a link that lives until it is revoked, else 1–365.
	ExpiresInDays int `json:"expires_in_days"`
}

// handleCreateShareLink answers POST /servers/{id}/share-links: mint a link
// and answer with its URL, the only time the URL is ever shown.
func (s *Server) handleCreateShareLink(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sv := s.loadReachableServer(w, r)
	if sv == nil {
		return
	}
	var req shareLinkRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	label := strings.TrimSpace(req.Label)
	if utf8.RuneCountInString(label) > shareLinkLabelMax || strings.ContainsAny(label, "\r\n") {
		writeError(w, http.StatusBadRequest, "label must be one line of at most 60 characters")
		return
	}
	if req.ExpiresInDays < 0 || req.ExpiresInDays > shareLinkMaxDays {
		writeError(w, http.StatusBadRequest, "expires_in_days must be 0 (never) or 1 to 365")
		return
	}
	// The link is an absolute URL pasted somewhere else, so it is built from
	// the configured public address and never from this request's Host header.
	public := s.publicURL(ctx)
	if public == "" {
		writeError(w, http.StatusConflict, "no public address is set — a share link needs it; set it under sessions & security")
		return
	}
	open, err := s.store.ListShareLinks(ctx, sv.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not count share links")
		return
	}
	if len(open) >= shareLinksPerServer {
		writeError(w, http.StatusConflict, "this server already has 25 share links — revoke one first")
		return
	}

	token, err := auth.NewSessionToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create a share link")
		return
	}
	now := time.Now().UTC()
	l := &store.ShareLink{
		ID: uuid.NewString(), ServerID: sv.ID, TokenHash: store.HashToken(token), Label: label,
		ShowAddress: req.ShowAddress, ShowPassword: req.ShowPassword, CreatedAt: now,
	}
	if req.ExpiresInDays > 0 {
		exp := now.Add(time.Duration(req.ExpiresInDays) * 24 * time.Hour)
		l.ExpiresAt = &exp
	}
	if u := userFrom(ctx); u != nil {
		l.CreatedBy = u.ID
	}
	if err := s.store.CreateShareLink(ctx, l); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "server not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not create a share link")
		return
	}
	// The audit detail names what the link reveals; the token never reaches
	// the log, and neither does the path, which carries only the server id.
	detail := "server.share:" + l.ID
	if l.ShowAddress {
		detail += "+address"
	}
	if l.ShowPassword {
		detail += "+password"
	}
	s.recordAuditDetail(r, http.StatusCreated, detail)
	v := s.shareLinkView(ctx, l, now, map[string]string{})
	v.URL = public + "/s/" + token
	writeJSON(w, http.StatusCreated, v)
}

// handleRevokeShareLink answers DELETE /servers/{id}/share-links/{linkID}.
// Revoking closes the public page at once; what a chat service already
// rendered from it stays where it was posted.
func (s *Server) handleRevokeShareLink(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sv := s.loadReachableServer(w, r)
	if sv == nil {
		return
	}
	id := chi.URLParam(r, "linkID")
	if err := s.store.RevokeShareLink(ctx, sv.ID, id, time.Now().UTC()); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "share link not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "could not revoke the share link")
		return
	}
	s.recordAuditDetail(r, http.StatusNoContent, "server.unshare:"+id)
	w.WriteHeader(http.StatusNoContent)
}

// pruneShareLinksOnce deletes links revoked or expired longer ago than the
// window — housekeeping on the retention ticker; the audit log keeps the
// record of who shared what.
func (s *Server) pruneShareLinksOnce(ctx context.Context) {
	n, err := s.store.DeleteShareLinksBefore(ctx, time.Now().Add(-shareLinkPruneAfter))
	if err != nil {
		s.logger.Warn("share link prune failed", "err", err)
		return
	}
	if n > 0 {
		s.logger.Info("closed share links pruned", "removed", n)
	}
}
