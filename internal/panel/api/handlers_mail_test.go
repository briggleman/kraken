package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/briggleman/kraken/internal/panel/api"
	"github.com/briggleman/kraken/internal/panel/config"
	"github.com/briggleman/kraken/internal/panel/mail/smtptest"
	"github.com/briggleman/kraken/internal/panel/store/memory"
)

func startRelay(t *testing.T, opts smtptest.Options) *smtptest.Server {
	t.Helper()
	srv, err := smtptest.Start(opts)
	if err != nil {
		t.Fatalf("smtptest: %v", err)
	}
	t.Cleanup(srv.Close)
	return srv
}

func settingsOf(t *testing.T, h http.Handler, token string) map[string]any {
	t.Helper()
	rec := do(t, h, http.MethodGet, "/api/v1/settings", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get settings: %d %s", rec.Code, rec.Body.String())
	}
	var v map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// configureRelay points the Panel's SMTP settings at a fake relay, in the
// clear (it is on loopback) and without a login.
func configureRelay(t *testing.T, h http.Handler, token string, srv *smtptest.Server) {
	t.Helper()
	rec := do(t, h, http.MethodPut, "/api/v1/settings", token, map[string]any{
		"smtp_host": srv.Host, "smtp_port": srv.Port, "smtp_security": "none",
		"smtp_from": "kraken@example.com", "smtp_from_name": "Kraken",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("configure relay: %d %s", rec.Code, rec.Body.String())
	}
}

// The settings view says a relay is configured and shows every field but the
// password, which is write-only. An empty string clears a field; an omitted
// one is left alone.
func TestSettings_SMTP_RoundTripNeverEchoesPassword(t *testing.T) {
	h, st := newTestServerStore(t)
	token := login(t, h)

	before := settingsOf(t, h, token)
	if before["smtp_configured"] != false || before["smtp_port"] != float64(587) || before["smtp_security"] != "starttls" {
		t.Fatalf("defaults: %v", before)
	}

	rec := do(t, h, http.MethodPut, "/api/v1/settings", token, map[string]any{
		"smtp_host": "smtp.example.com", "smtp_port": 465, "smtp_security": "tls",
		"smtp_username": "kraken", "smtp_password": "hunter2-not-for-show",
		"smtp_from": "kraken@example.com", "smtp_from_name": "Kraken Panel",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "hunter2") {
		t.Fatalf("the save echoed the password: %s", rec.Body.String())
	}
	v := settingsOf(t, h, token)
	if v["smtp_configured"] != true || v["smtp_host"] != "smtp.example.com" || v["smtp_port"] != float64(465) ||
		v["smtp_security"] != "tls" || v["smtp_username"] != "kraken" || v["smtp_from"] != "kraken@example.com" ||
		v["smtp_from_name"] != "Kraken Panel" {
		t.Fatalf("view: %v", v)
	}
	for k := range v {
		if strings.Contains(k, "password") {
			t.Fatalf("the view has a password-shaped field %q", k)
		}
	}
	stored, err := st.GetSettings(context.Background())
	if err != nil || stored.SMTPPassword != "hunter2-not-for-show" {
		t.Fatalf("stored password = %q (%v)", stored.SMTPPassword, err)
	}

	// A save that touches only the from name keeps the password.
	if rec := do(t, h, http.MethodPut, "/api/v1/settings", token, map[string]any{"smtp_from_name": "Kraken"}); rec.Code != http.StatusOK {
		t.Fatalf("put name: %d", rec.Code)
	}
	stored, _ = st.GetSettings(context.Background())
	if stored.SMTPPassword != "hunter2-not-for-show" || stored.SMTPFromName != "Kraken" {
		t.Fatalf("a partial save disturbed other fields: %+v", stored)
	}
	// Clearing the host turns email off; the password goes with it when cleared too.
	if rec := do(t, h, http.MethodPut, "/api/v1/settings", token, map[string]any{"smtp_host": "", "smtp_password": ""}); rec.Code != http.StatusOK {
		t.Fatalf("clear: %d", rec.Code)
	}
	if v := settingsOf(t, h, token); v["smtp_configured"] != false {
		t.Fatalf("still configured after clearing the host: %v", v)
	}
}

func TestSettings_SMTP_Validation(t *testing.T) {
	h := newTestServer(t)
	token := login(t, h)
	for _, tc := range []struct {
		name string
		body map[string]any
		want string
	}{
		{"security", map[string]any{"smtp_security": "ssl"}, "starttls, tls or none"},
		{"port", map[string]any{"smtp_port": 70000}, "port"},
		{"from with name", map[string]any{"smtp_from": "Kraken <kraken@example.com>"}, "bare address"},
		{"from garbage", map[string]any{"smtp_from": "not-an-address"}, "bare address"},
		{"host with scheme", map[string]any{"smtp_host": "smtp://smtp.example.com"}, "no scheme"},
		{"from name injection", map[string]any{"smtp_from_name": "a\r\nBcc: b@example.com"}, "single line"},
	} {
		rec := do(t, h, http.MethodPut, "/api/v1/settings", token, tc.body)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("%s: %d %s", tc.name, rec.Code, rec.Body.String())
		}
	}
	// Nothing invalid was stored.
	if v := settingsOf(t, h, token); v["smtp_configured"] != false || v["smtp_host"] != nil {
		t.Fatalf("an invalid save landed: %v", v)
	}
}

func TestSettings_PublicURL_ValidatedAndLockable(t *testing.T) {
	h := newTestServer(t)
	token := login(t, h)

	for _, bad := range []string{"panel.example.com", "ftp://panel.example.com", "https://", "https://panel.example.com/?x=1", "https://panel.example.com/#frag", "https://user:pw@panel.example.com"} {
		rec := do(t, h, http.MethodPut, "/api/v1/settings", token, map[string]any{"public_url": bad})
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "public_url") {
			t.Errorf("%q accepted: %d %s", bad, rec.Code, rec.Body.String())
		}
	}
	rec := do(t, h, http.MethodPut, "/api/v1/settings", token, map[string]any{"public_url": " https://panel.example.com/kraken/ "})
	if rec.Code != http.StatusOK {
		t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
	}
	v := settingsOf(t, h, token)
	if v["public_url"] != "https://panel.example.com/kraken" || v["public_url_locked"] != false {
		t.Fatalf("stored public url: %v", v)
	}
	// Empty clears it.
	if rec := do(t, h, http.MethodPut, "/api/v1/settings", token, map[string]any{"public_url": ""}); rec.Code != http.StatusOK {
		t.Fatalf("clear: %d", rec.Code)
	}
	if v := settingsOf(t, h, token); v["public_url"] != "" {
		t.Fatalf("not cleared: %v", v)
	}

	// Pinned by the environment: the env value is shown, a save is ignored.
	locked := newTestServerWith(t, func(c *config.Config) {
		c.PublicURL = "https://pinned.example.com"
		c.PublicURLFromEnv = true
	})
	ltoken := login(t, locked)
	if rec := do(t, locked, http.MethodPut, "/api/v1/settings", ltoken, map[string]any{"public_url": "https://other.example.com"}); rec.Code != http.StatusOK {
		t.Fatalf("put under lock: %d", rec.Code)
	}
	if v := settingsOf(t, locked, ltoken); v["public_url"] != "https://pinned.example.com" || v["public_url_locked"] != true {
		t.Fatalf("lock not honoured: %v", v)
	}
}

func TestTestMail_SendsThroughConfiguredRelay(t *testing.T) {
	h, st := newTestServerStore(t)
	token := login(t, h)

	// Not configured yet.
	rec := do(t, h, http.MethodPost, "/api/v1/settings/smtp/test", token, map[string]any{"to": "ben@example.org"})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "not configured") {
		t.Fatalf("unconfigured: %d %s", rec.Code, rec.Body.String())
	}

	relay := startRelay(t, smtptest.Options{})
	configureRelay(t, h, token, relay)
	if rec := do(t, h, http.MethodPut, "/api/v1/settings", token, map[string]any{"public_url": "https://kraken.example.com"}); rec.Code != http.StatusOK {
		t.Fatalf("public url: %d", rec.Code)
	}

	// No recipient anywhere: the admin has no email on their account.
	rec = do(t, h, http.MethodPost, "/api/v1/settings/smtp/test", token, nil)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "no recipient") {
		t.Fatalf("no recipient: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodPost, "/api/v1/settings/smtp/test", token, map[string]any{"to": "nope"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad recipient: %d %s", rec.Code, rec.Body.String())
	}

	rec = do(t, h, http.MethodPost, "/api/v1/settings/smtp/test", token, map[string]any{"to": "ben@example.org"})
	if rec.Code != http.StatusOK {
		t.Fatalf("send: %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		SentTo, MessageID, ServerReply string
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &struct {
		SentTo      *string `json:"sent_to"`
		MessageID   *string `json:"message_id"`
		ServerReply *string `json:"server_reply"`
	}{&resp.SentTo, &resp.MessageID, &resp.ServerReply})
	if resp.SentTo != "ben@example.org" || !strings.HasPrefix(resp.MessageID, "<") || !strings.Contains(resp.ServerReply, "queued") {
		t.Fatalf("response: %+v", resp)
	}
	got := relay.Messages()
	if len(got) != 1 || got[0].To[0] != "ben@example.org" || got[0].From != "kraken@example.com" {
		t.Fatalf("relay saw %+v", got)
	}
	body := got[0].Data
	for _, want := range []string{"Message-ID: " + resp.MessageID, "multipart/alternative", "Outbound email works", "kraken.example.com", testAdmin} {
		if !strings.Contains(body, want) {
			t.Errorf("message lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "{{") {
		t.Errorf("unrendered template:\n%s", body)
	}

	// The default recipient is the caller's own address.
	u, _ := st.GetUserByUsername(context.Background(), testAdmin)
	u.Email = "admin@example.org"
	_ = st.UpdateUser(context.Background(), u)
	rec = do(t, h, http.MethodPost, "/api/v1/settings/smtp/test", token, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "admin@example.org") {
		t.Fatalf("default recipient: %d %s", rec.Code, rec.Body.String())
	}

	// Audited by name, without the password.
	rec = do(t, h, http.MethodGet, "/api/v1/audit?limit=50", token, nil)
	if !strings.Contains(rec.Body.String(), "mail.test:ben@example.org") || !strings.Contains(rec.Body.String(), "settings:smtp") {
		t.Fatalf("audit lacks the mail rows: %s", rec.Body.String())
	}
}

func TestTestMail_RelayRefusalIs424(t *testing.T) {
	h := newTestServer(t)
	token := login(t, h)
	relay := startRelay(t, smtptest.Options{RejectRecipient: "550 5.1.1 no mailbox by that name"})
	configureRelay(t, h, token, relay)
	rec := do(t, h, http.MethodPost, "/api/v1/settings/smtp/test", token, map[string]any{"to": "ben@example.org"})
	if rec.Code != http.StatusFailedDependency || !strings.Contains(rec.Body.String(), "no mailbox by that name") {
		t.Fatalf("refusal: %d %s", rec.Code, rec.Body.String())
	}
}

// Every send spends from one Panel-wide budget; the burst-and-one-th is refused
// with a Retry-After, and KRAKEN_RATE_LIMITS=off lifts it.
func TestTestMail_RateLimited(t *testing.T) {
	h := newTestServer(t)
	token := login(t, h)
	relay := startRelay(t, smtptest.Options{})
	configureRelay(t, h, token, relay)
	for i := 0; i < api.MailSendBurstForTest; i++ {
		if rec := do(t, h, http.MethodPost, "/api/v1/settings/smtp/test", token, map[string]any{"to": "ben@example.org"}); rec.Code != http.StatusOK {
			t.Fatalf("send %d: %d %s", i, rec.Code, rec.Body.String())
		}
	}
	rec := do(t, h, http.MethodPost, "/api/v1/settings/smtp/test", token, map[string]any{"to": "ben@example.org"})
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("over budget: %d %s", rec.Code, rec.Body.String())
	}
	if n := len(relay.Messages()); n != api.MailSendBurstForTest {
		t.Fatalf("relay saw %d messages, want %d", n, api.MailSendBurstForTest)
	}

	off := newTestServerWith(t, func(c *config.Config) { c.RateLimits = config.RateLimitsOff })
	otoken := login(t, off)
	configureRelay(t, off, otoken, relay)
	for i := 0; i <= api.MailSendBurstForTest; i++ {
		if rec := do(t, off, http.MethodPost, "/api/v1/settings/smtp/test", otoken, map[string]any{"to": "ben@example.org"}); rec.Code != http.StatusOK {
			t.Fatalf("limits off, send %d: %d", i, rec.Code)
		}
	}
}

// Only settings.manage may configure or test the relay.
func TestTestMail_RequiresSettingsManage(t *testing.T) {
	h := newTestServer(t)
	token := login(t, h)
	rec := do(t, h, http.MethodPost, "/api/v1/users", token, map[string]any{
		"username": "viewer", "password": "viewer-pass-123", "role_id": "readonly",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create viewer: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodPost, "/api/v1/auth/login", "", map[string]string{"username": "viewer", "password": "viewer-pass-123"})
	var lr struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &lr)
	if rec := do(t, h, http.MethodPost, "/api/v1/settings/smtp/test", lr.Token, map[string]any{"to": "x@example.org"}); rec.Code != http.StatusForbidden {
		t.Fatalf("viewer could test mail: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodPut, "/api/v1/settings", lr.Token, map[string]any{"smtp_host": "x"}); rec.Code != http.StatusForbidden {
		t.Fatalf("viewer could set smtp: %d", rec.Code)
	}
}

var _ = memory.New // the store type is what newTestServerStore hands back
