package api

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	netmail "net/mail"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/briggleman/kraken/internal/panel/auth"
	"github.com/briggleman/kraken/internal/panel/mail"
	"github.com/briggleman/kraken/internal/panel/store"
)

// inviteTTL is how long an invite link can be redeemed. Longer than a
// password-reset link would be: the person it goes to is not at the keyboard
// waiting for it. A variable so a test can run the expiry branch.
var inviteTTL = 24 * time.Hour

// inviteResendCooldown is the least time between two invites to one user. It
// stops a double-click (or a stuck key) from sending the same person two
// emails, and gives the first one a moment to arrive before anybody decides
// it did not. A variable so a test can send twice.
var inviteResendCooldown = time.Minute

// inviteView is an invite as the users list and the create/resend answers
// show it. Only an OPEN invite has one: once accepted or revoked there is
// nothing to say.
type inviteView struct {
	// State is "sent" (live), "expired" (delivered, past its time) or
	// "failed" (the relay never took the message; resend to try again).
	State     string     `json:"state"`
	SentTo    string     `json:"sent_to"`
	SentAt    *time.Time `json:"sent_at,omitempty"`
	ExpiresAt time.Time  `json:"expires_at"`
}

func inviteViewOf(inv *store.Invite, now time.Time) *inviteView {
	if inv == nil || !inv.Open() {
		return nil
	}
	v := &inviteView{SentTo: inv.SentTo, SentAt: inv.SentAt, ExpiresAt: inv.ExpiresAt}
	switch {
	case inv.SentAt == nil:
		v.State = "failed"
	case !now.Before(inv.ExpiresAt):
		v.State = "expired"
	default:
		v.State = "sent"
	}
	return v
}

// inviteReady reports whether the Panel can send an invite at all, and if not,
// the sentence that says what is missing.
func (s *Server) inviteReady(ctx context.Context) (mail.Config, string, string) {
	st := s.panelSettings(ctx)
	cfg := mailConfig(st)
	if !cfg.Configured() {
		return cfg, "", "outbound email is not configured — set an smtp relay under console settings first"
	}
	public := s.publicURL(ctx)
	if public == "" {
		return cfg, "", "no public address is set — an invite link needs it; set it under sessions & security"
	}
	return cfg, public, ""
}

// sendInvite revokes the user's open invite, mints a new one and emails it.
// The invite row is written before the send and marked sent after it, so a
// link the relay never took is never redeemable. The returned invite is what
// was recorded either way; err is the send failure, if any.
func (s *Server) sendInvite(ctx context.Context, cfg mail.Config, public string, u *store.User, inviter *store.User) (*store.Invite, error) {
	now := time.Now().UTC()
	if _, err := s.store.RevokeInvitesByUser(ctx, u.ID, now); err != nil {
		return nil, fmt.Errorf("could not close the previous invite: %w", err)
	}
	token, err := auth.NewSessionToken()
	if err != nil {
		return nil, err
	}
	inv := &store.Invite{
		ID: uuid.NewString(), UserID: u.ID, TokenHash: store.HashToken(token),
		SentTo: u.Email, CreatedAt: now, ExpiresAt: now.Add(inviteTTL),
	}
	if inviter != nil {
		inv.CreatedBy = inviter.ID
	}
	if err := s.store.CreateInvite(ctx, inv); err != nil {
		return nil, fmt.Errorf("could not record the invite: %w", err)
	}

	role, _ := s.store.GetRole(ctx, u.RoleID)
	roleName := u.RoleID
	if role != nil {
		roleName = role.Name
	}
	inviterName := "an administrator"
	if inviter != nil {
		inviterName = inviter.Username
	}
	data := mail.InviteData{
		Inviter:   inviterName,
		Username:  u.Username,
		RoleName:  roleName,
		PanelHost: hostOf(public),
		InviteURL: public + "/invite/" + token,
		ExpiresAt: inv.ExpiresAt.Format("2 Jan 2006, 15:04 UTC"),
		ExpiresIn: humanDuration(inviteTTL),
	}
	text, html, err := mail.Render("invite", data)
	if err != nil {
		return inv, err
	}
	res, err := s.mailer.Send(ctx, cfg, mail.Message{To: u.Email, Subject: mail.InviteSubject(data), Text: text, HTML: html})
	if err != nil {
		return inv, err
	}
	sent := time.Now().UTC()
	if err := s.store.MarkInviteSent(ctx, inv.ID, sent); err != nil {
		// The message is out; a row that says otherwise would only stop the
		// person redeeming it. Say so and carry on.
		s.logger.Warn("invite sent but could not be marked sent", "user", u.Username, "err", err)
	}
	inv.SentAt = &sent
	s.logger.Info("invite sent", "user", u.Username, "by", inviterName, "message_id", res.MessageID)
	return inv, nil
}

// hostOf is the host of a public URL, for the sentences the email uses.
func hostOf(public string) string {
	rest := public
	if i := strings.Index(rest, "://"); i >= 0 {
		rest = rest[i+3:]
	}
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		rest = rest[:i]
	}
	return rest
}

// humanDuration writes a TTL the way the email says it: "24 hours", "90 minutes".
func humanDuration(d time.Duration) string {
	if d >= time.Hour && d%time.Hour == 0 {
		h := int(d / time.Hour)
		if h == 1 {
			return "1 hour"
		}
		return fmt.Sprintf("%d hours", h)
	}
	m := int(math.Round(d.Minutes()))
	if m == 1 {
		return "1 minute"
	}
	return fmt.Sprintf("%d minutes", m)
}

// inviteTarget loads the user an admin is acting on and checks they can be
// invited at all. It writes the refusal itself and reports ok=false.
func (s *Server) inviteTarget(w http.ResponseWriter, r *http.Request) (*store.User, bool) {
	u, err := s.store.GetUser(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "user not found")
		return nil, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load user")
		return nil, false
	}
	return u, true
}

// handleSendInvite is POST /users/{id}/invite: send (or send again) the
// invite for an existing account. The previous one stops working.
func (s *Server) handleSendInvite(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u, ok := s.inviteTarget(w, r)
	if !ok {
		return
	}
	if u.Disabled {
		writeError(w, http.StatusConflict, "the account is disabled; enable it before inviting")
		return
	}
	if _, err := netmail.ParseAddress(u.Email); err != nil {
		writeError(w, http.StatusConflict, "the account has no email address to send an invite to")
		return
	}
	cfg, public, why := s.inviteReady(ctx)
	if why != "" {
		writeError(w, http.StatusConflict, why)
		return
	}
	if latest, err := s.store.LatestInviteByUser(ctx, u.ID); err == nil {
		if wait := inviteResendCooldown - time.Since(latest.CreatedAt); wait > 0 {
			s.writeRateLimited(w, wait)
			return
		}
	}
	if s.rejectMailSend(w, r) {
		return
	}
	inv, err := s.sendInvite(ctx, cfg, public, u, userFrom(ctx))
	if err != nil {
		s.recordAuditDetail(r, statusUpstream, "invite.resent:"+u.Username)
		writeJSON(w, statusUpstream, map[string]any{"error": "the invite was recorded but the email did not go out: " + err.Error(), "invite": inviteViewOf(inv, time.Now())})
		return
	}
	s.recordAuditDetail(r, http.StatusOK, "invite.resent:"+u.Username)
	writeJSON(w, http.StatusOK, map[string]any{"invite": inviteViewOf(inv, time.Now())})
}

// handleRevokeInvite is DELETE /users/{id}/invite: the open invite, if any,
// stops working now. The account stays, locked, until a resend or a reset.
func (s *Server) handleRevokeInvite(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u, ok := s.inviteTarget(w, r)
	if !ok {
		return
	}
	n, err := s.store.RevokeInvitesByUser(ctx, u.ID, time.Now().UTC())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not revoke the invite")
		return
	}
	s.recordAuditDetail(r, http.StatusOK, "invite.revoked:"+u.Username)
	writeJSON(w, http.StatusOK, map[string]any{"revoked": n})
}

// liveInvite resolves a bearer token to a redeemable invite and its user, or
// nothing. Every way it can fail — unknown, expired, used, revoked, the
// account disabled or gone — looks the same to the caller, who holds nothing
// but a link and should learn nothing from a wrong one.
func (s *Server) liveInvite(ctx context.Context, token string) (*store.Invite, *store.User) {
	if token == "" || len(token) > 128 {
		return nil, nil
	}
	inv, err := s.store.GetInviteByHash(ctx, store.HashToken(token))
	if err != nil || !inv.Live(time.Now()) {
		return nil, nil
	}
	u, err := s.store.GetUser(ctx, inv.UserID)
	if err != nil || u.Disabled {
		return nil, nil
	}
	return inv, u
}

const inviteInvalidMsg = "this invite link is not valid any more — ask the person who invited you to send a new one"

// handleGetInvite is GET /invites/{token}: what the set-password sheet shows
// before asking for a password. Unauthenticated; the token is the authority.
func (s *Server) handleGetInvite(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	inv, u := s.liveInvite(ctx, chi.URLParam(r, "token"))
	if inv == nil {
		s.logger.Debug("invite lookup refused", "ip", s.clientIP(r))
		writeError(w, http.StatusNotFound, inviteInvalidMsg)
		return
	}
	roleName := u.RoleID
	if role, err := s.store.GetRole(ctx, u.RoleID); err == nil {
		roleName = role.Name
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"username":   u.Username,
		"role_name":  roleName,
		"panel_host": hostOf(s.publicURL(ctx)),
		"expires_at": inv.ExpiresAt,
	})
}

type acceptInviteRequest struct {
	Password string `json:"password"`
}

// handleAcceptInvite is POST /invites/{token}/accept: the person chooses
// their password and is signed in. The invite is consumed first — a single
// UPDATE that a second redemption cannot also win — then the password is
// set, every session the account had is ended, and a fresh one is answered
// with, as login does.
func (s *Server) handleAcceptInvite(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req acceptInviteRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	inv, u := s.liveInvite(ctx, chi.URLParam(r, "token"))
	if inv == nil {
		s.logger.Debug("invite redemption refused", "ip", s.clientIP(r))
		writeError(w, http.StatusNotFound, inviteInvalidMsg)
		return
	}
	if len(req.Password) < minPasswordLen {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("the password must be at least %d characters", minPasswordLen))
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not hash password")
		return
	}
	if err := s.store.ConsumeInvite(ctx, inv.TokenHash, time.Now()); err != nil {
		// Somebody got here first with the same link, or it just expired.
		writeError(w, http.StatusNotFound, inviteInvalidMsg)
		return
	}
	u.PasswordHash = hash
	u.MustChangePassword = false
	if err := s.store.UpdateUser(ctx, u); err != nil {
		writeError(w, http.StatusInternalServerError, "could not set the password")
		return
	}
	if err := s.store.DeleteSessionsByUser(ctx, u.ID); err != nil {
		s.logger.Warn("invite accepted but older sessions could not be ended", "user", u.Username, "err", err)
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
	s.logger.Info("invite accepted", "user", u.Username)
	s.appendAudit(withoutInviteToken(r), http.StatusOK, u.Username, "invite.accepted")
	writeJSON(w, http.StatusOK, loginResponse{Token: token, ExpiresAt: sess.ExpiresAt, User: toUserView(u)})
}

// withoutInviteToken returns a copy of the request whose path is its route
// pattern rather than the path it arrived on. An audit row records the path,
// and on the invite routes the path IS the one-time token; a row that kept it
// would hand a live link to anyone who can read the log.
func withoutInviteToken(r *http.Request) *http.Request {
	pattern := "/api/v1/invites/{token}"
	if rc := chi.RouteContext(r.Context()); rc != nil && rc.RoutePattern() != "" {
		pattern = rc.RoutePattern()
	}
	r2 := r.Clone(r.Context())
	u := *r.URL
	u.Path, u.RawPath = pattern, ""
	r2.URL = &u
	return r2
}

// pruneInvitesOnce deletes invites that are closed or expired and older than
// the window — housekeeping on the retention ticker; the audit log keeps the
// record of who was invited and when.
func (s *Server) pruneInvitesOnce(ctx context.Context) {
	before := time.Now().Add(-invitePruneAfter)
	n, err := s.store.DeleteInvitesBefore(ctx, before)
	if err != nil {
		s.logger.Warn("invite prune failed", "err", err)
		return
	}
	if n > 0 {
		s.logger.Info("stale invites pruned", "removed", n)
	}
}

// invitePruneAfter is how long a closed or expired invite row is kept before
// pruneInvitesOnce removes it.
const invitePruneAfter = 30 * 24 * time.Hour
