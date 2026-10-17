package api

import (
	"context"
	"net"
	"net/http"
	netmail "net/mail"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/briggleman/kraken/internal/panel/mail"
	"github.com/briggleman/kraken/internal/panel/store"
)

// mailConfig is the relay as the stored settings describe it. Read on every
// send, so a save in the UI takes effect on the next message.
func mailConfig(st *store.Settings) mail.Config {
	sec, _ := mail.ParseSecurity(st.SMTPSecurity)
	return mail.Config{
		Host: st.SMTPHost, Port: st.SMTPPort, Security: sec,
		Username: st.SMTPUsername, Password: st.SMTPPassword,
		From: st.SMTPFrom, FromName: st.SMTPFromName,
	}
}

// publicURL is the Panel's public address: KRAKEN_PUBLIC_URL wins, then the
// stored setting, then "" for not set. It is the only source for a link the
// Panel writes into email; the request's Host header is never consulted.
func (s *Server) publicURL(ctx context.Context) string {
	if s.cfg.PublicURLFromEnv {
		return s.cfg.PublicURL
	}
	return s.panelSettings(ctx).PublicURL
}

// publicHost is the host part of publicURL, or "" when no public address is set.
func (s *Server) publicHost(ctx context.Context) string {
	u, err := url.Parse(s.publicURL(ctx))
	if err != nil {
		return ""
	}
	return u.Host
}

type testMailRequest struct {
	// To is the recipient; empty sends to the caller's own address.
	To string `json:"to"`
}

type testMailResponse struct {
	SentTo      string `json:"sent_to"`
	MessageID   string `json:"message_id"`
	ServerReply string `json:"server_reply"`
}

// handleTestMail sends the test message to prove the relay settings work:
// POST /settings/smtp/test {to}. 400 when email is not configured or the
// recipient is not an address, 429 under the outbound-mail limiter, 424 with
// the relay's own words when it refuses.
func (s *Server) handleTestMail(w http.ResponseWriter, r *http.Request) {
	var req testMailRequest
	if r.ContentLength != 0 {
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid body")
			return
		}
	}
	ctx := r.Context()
	to := strings.TrimSpace(req.To)
	if to == "" {
		if u := userFrom(ctx); u != nil {
			to = strings.TrimSpace(u.Email)
		}
	}
	if to == "" {
		writeError(w, http.StatusBadRequest, "no recipient: give an address, or add an email address to your own account")
		return
	}
	if _, err := netmail.ParseAddress(to); err != nil {
		writeError(w, http.StatusBadRequest, "the recipient is not an email address")
		return
	}
	cfg := mailConfig(s.panelSettings(ctx))
	if !cfg.Configured() {
		writeError(w, http.StatusBadRequest, "outbound email is not configured — set an smtp host and a from address first")
		return
	}
	if s.rejectMailSend(w, r) {
		return
	}

	actor := "an administrator"
	if u := userFrom(ctx); u != nil {
		actor = u.Username
	}
	data := mail.TestData{
		PanelHost: s.publicHost(ctx),
		Username:  actor,
		Relay:     net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)),
		Security:  string(cfg.Security),
		SentAt:    time.Now().UTC().Format("2 Jan 2006, 15:04 UTC"),
	}
	if cfg.Port == 0 {
		data.Relay = net.JoinHostPort(cfg.Host, strconv.Itoa(mail.DefaultPort))
	}
	text, html, err := mail.Render("test", data)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not render the test message")
		return
	}
	res, err := s.mailer.Send(ctx, cfg, mail.Message{To: to, Subject: mail.TestSubject(data), Text: text, HTML: html})
	if err != nil {
		s.recordAuditDetail(r, statusUpstream, "mail.test:"+to)
		writeError(w, statusUpstream, err.Error())
		return
	}
	s.logger.Info("test email sent", "to", to, "by", actor, "message_id", res.MessageID)
	s.recordAuditDetail(r, http.StatusOK, "mail.test:"+to)
	writeJSON(w, http.StatusOK, testMailResponse{SentTo: to, MessageID: res.MessageID, ServerReply: res.ServerReply})
}

// rejectMailSend applies the outbound-mail limiter. One key for the whole
// Panel: the limit is on what this Panel sends, not on who asked, because the
// thing it protects is the operator's domain reputation with the relay, which
// a hijacked admin session could spend just as fast as a careless one.
func (s *Server) rejectMailSend(w http.ResponseWriter, r *http.Request) bool {
	ok, retry, _ := s.mailLimit.allow(mailLimitKey)
	if ok {
		return false
	}
	incRateLimited(s.mailLimit.name)
	s.writeRateLimited(w, retry)
	return true
}
