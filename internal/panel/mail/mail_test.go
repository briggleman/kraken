package mail_test

import (
	"context"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"strings"
	"testing"
	"time"

	kmail "github.com/briggleman/kraken/internal/panel/mail"
	"github.com/briggleman/kraken/internal/panel/mail/smtptest"
)

func start(t *testing.T, opts smtptest.Options) *smtptest.Server {
	t.Helper()
	srv, err := smtptest.Start(opts)
	if err != nil {
		t.Fatalf("smtptest: %v", err)
	}
	t.Cleanup(srv.Close)
	return srv
}

func cfgFor(srv *smtptest.Server, sec kmail.Security) kmail.Config {
	return kmail.Config{
		Host: srv.Host, Port: srv.Port, Security: sec,
		From: "kraken@example.com", FromName: "Kraken",
	}
}

var msg = kmail.Message{
	To:      "captain@example.org",
	Subject: "Test · ünïcode subject",
	Text:    "plain part\nline two.\n",
	HTML:    "<p>html part — <b>bold</b></p>\n",
}

// The wire format: both parts, text first, headers the relay and the client
// need, the Message-ID returned matching the one in the message.
func TestSend_PlainRelay_MultipartAlternative(t *testing.T) {
	srv := start(t, smtptest.Options{})
	s := &kmail.Sender{Now: func() time.Time { return time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC) }}
	res, err := s.Send(context.Background(), cfgFor(srv, kmail.SecurityNone), msg)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if !strings.HasPrefix(res.ServerReply, "2.0.0 OK queued") {
		t.Errorf("server reply not captured: %q", res.ServerReply)
	}
	got := srv.Messages()
	if len(got) != 1 {
		t.Fatalf("relay saw %d messages, want 1", len(got))
	}
	m := got[0]
	if m.From != "kraken@example.com" || len(m.To) != 1 || m.To[0] != "captain@example.org" {
		t.Errorf("envelope: from %q to %v", m.From, m.To)
	}
	parsed, err := mail.ReadMessage(strings.NewReader(m.Data))
	if err != nil {
		t.Fatalf("relay received something that is not a message: %v\n%s", err, m.Data)
	}
	h := parsed.Header
	if h.Get("Message-ID") != res.MessageID || !strings.HasSuffix(res.MessageID, "@example.com>") {
		t.Errorf("Message-ID header %q vs result %q", h.Get("Message-ID"), res.MessageID)
	}
	if h.Get("From") != `"Kraken" <kraken@example.com>` {
		t.Errorf("From = %q", h.Get("From"))
	}
	if h.Get("Date") != "Sat, 17 Oct 2026 12:00:00 +0000" {
		t.Errorf("Date = %q", h.Get("Date"))
	}
	if h.Get("Auto-Submitted") != "auto-generated" {
		t.Errorf("Auto-Submitted = %q", h.Get("Auto-Submitted"))
	}
	dec := new(mime.WordDecoder)
	if subj, err := dec.DecodeHeader(h.Get("Subject")); err != nil || subj != msg.Subject {
		t.Errorf("Subject decoded to %q (%v) from %q", subj, err, h.Get("Subject"))
	}
	ctype, params, err := mime.ParseMediaType(h.Get("Content-Type"))
	if err != nil || ctype != "multipart/alternative" {
		t.Fatalf("Content-Type = %q (%v)", h.Get("Content-Type"), err)
	}
	mr := multipart.NewReader(parsed.Body, params["boundary"])
	var types []string
	var bodies []string
	for {
		// NextRawPart: NextPart would decode quoted-printable itself and drop
		// the header the test is checking for.
		p, err := mr.NextRawPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("part: %v", err)
		}
		types = append(types, p.Header.Get("Content-Type"))
		if p.Header.Get("Content-Transfer-Encoding") != "quoted-printable" {
			t.Errorf("part %s is %q, want quoted-printable", p.Header.Get("Content-Type"), p.Header.Get("Content-Transfer-Encoding"))
		}
		b, _ := io.ReadAll(quotedprintable.NewReader(p))
		bodies = append(bodies, string(b))
	}
	if len(types) != 2 || !strings.HasPrefix(types[0], "text/plain") || !strings.HasPrefix(types[1], "text/html") {
		t.Fatalf("parts = %v, want text then html", types)
	}
	if strings.TrimSpace(bodies[0]) != strings.TrimSpace(strings.ReplaceAll(msg.Text, "\n", "\r\n")) &&
		strings.TrimSpace(bodies[0]) != strings.TrimSpace(msg.Text) {
		t.Errorf("text part = %q", bodies[0])
	}
	if !strings.Contains(bodies[1], "<b>bold</b>") || !strings.Contains(bodies[1], "—") {
		t.Errorf("html part = %q", bodies[1])
	}
}

func TestSend_StartTLS_Required(t *testing.T) {
	tlsCfg, pool, err := smtptest.SelfSigned("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	// Offered and taken: the message arrives over TLS.
	srv := start(t, smtptest.Options{TLS: tlsCfg})
	s := &kmail.Sender{RootCAs: pool}
	if _, err := s.Send(context.Background(), cfgFor(srv, kmail.SecurityStartTLS), msg); err != nil {
		t.Fatalf("starttls send: %v", err)
	}
	if got := srv.Messages(); len(got) != 1 || !got[0].TLS {
		t.Fatalf("message did not arrive over TLS: %+v", got)
	}

	// Not offered: refused before anything is sent, with the fix in the text.
	plain := start(t, smtptest.Options{})
	_, err = s.Send(context.Background(), cfgFor(plain, kmail.SecurityStartTLS), msg)
	if err == nil || !strings.Contains(err.Error(), "does not offer STARTTLS") {
		t.Fatalf("plain relay accepted under starttls: %v", err)
	}
	if n := len(plain.Messages()); n != 0 {
		t.Fatalf("%d messages went to the plain relay", n)
	}

	// Offered by a relay whose certificate nobody trusts: refused. No
	// InsecureSkipVerify exists to be reached for.
	untrusted := &kmail.Sender{}
	_, err = untrusted.Send(context.Background(), cfgFor(srv, kmail.SecurityStartTLS), msg)
	if err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("untrusted certificate accepted: %v", err)
	}
}

func TestSend_ImplicitTLS(t *testing.T) {
	tlsCfg, pool, err := smtptest.SelfSigned("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	srv := start(t, smtptest.Options{TLS: tlsCfg, ImplicitTLS: true})
	s := &kmail.Sender{RootCAs: pool}
	if _, err := s.Send(context.Background(), cfgFor(srv, kmail.SecurityTLS), msg); err != nil {
		t.Fatalf("implicit tls send: %v", err)
	}
	if got := srv.Messages(); len(got) != 1 || !got[0].TLS {
		t.Fatalf("message did not arrive over TLS: %+v", got)
	}
}

// The mechanism follows what the relay offers, and a password never goes over
// a plain connection to a remote host.
func TestSend_AuthMechanismSelection(t *testing.T) {
	tlsCfg, pool, err := smtptest.SelfSigned("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ offered, want string }{
		{"PLAIN LOGIN CRAM-MD5", "PLAIN"},
		{"LOGIN CRAM-MD5", "CRAM-MD5"},
		{"LOGIN", "LOGIN"},
	} {
		srv := start(t, smtptest.Options{TLS: tlsCfg, AdvertiseAuth: tc.offered})
		cfg := cfgFor(srv, kmail.SecurityStartTLS)
		cfg.Username, cfg.Password = "kraken", "s3cret"
		s := &kmail.Sender{RootCAs: pool}
		if _, err := s.Send(context.Background(), cfg, msg); err != nil {
			t.Fatalf("offered %q: %v", tc.offered, err)
		}
		if got := srv.Messages(); len(got) != 1 || got[0].AuthMech != tc.want {
			t.Errorf("offered %q: client used %q, want %q", tc.offered, got[0].AuthMech, tc.want)
		}
	}

	// A relay that advertises nothing but has a username configured.
	srv := start(t, smtptest.Options{AdvertiseAuth: "XOAUTH2"})
	cfg := cfgFor(srv, kmail.SecurityNone)
	cfg.Username, cfg.Password = "kraken", "s3cret"
	if _, err := (&kmail.Sender{}).Send(context.Background(), cfg, msg); err == nil || !strings.Contains(err.Error(), "no authentication mechanism") {
		t.Fatalf("unknown mechanism accepted: %v", err)
	}
}

func TestSend_RelayRefusesRecipient(t *testing.T) {
	srv := start(t, smtptest.Options{RejectRecipient: "550 5.1.1 no such user here"})
	_, err := (&kmail.Sender{}).Send(context.Background(), cfgFor(srv, kmail.SecurityNone), msg)
	if err == nil || !strings.Contains(err.Error(), "refused the recipient captain@example.org") || !strings.Contains(err.Error(), "no such user") {
		t.Fatalf("err = %v", err)
	}
}

func TestSend_Validation(t *testing.T) {
	srv := start(t, smtptest.Options{})
	s := &kmail.Sender{}
	base := cfgFor(srv, kmail.SecurityNone)

	if _, err := s.Send(context.Background(), kmail.Config{}, msg); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Errorf("unconfigured: %v", err)
	}
	bad := base
	bad.From = "Kraken <kraken@example.com>"
	if _, err := s.Send(context.Background(), bad, msg); err == nil || !strings.Contains(err.Error(), "bare address") {
		t.Errorf("display-name From accepted: %v", err)
	}
	bad = base
	bad.FromName = "Kraken\r\nBcc: everyone@example.org"
	if _, err := s.Send(context.Background(), bad, msg); err == nil || !strings.Contains(err.Error(), "single line") {
		t.Errorf("CRLF in from name accepted: %v", err)
	}
	bad = base
	bad.Security = "ssl"
	if _, err := s.Send(context.Background(), bad, msg); err == nil || !strings.Contains(err.Error(), "security mode") {
		t.Errorf("unknown security accepted: %v", err)
	}
	m := msg
	m.To = "not an address"
	if _, err := s.Send(context.Background(), base, m); err == nil || !strings.Contains(err.Error(), "not an email address") {
		t.Errorf("bad recipient accepted: %v", err)
	}
	m = msg
	m.Subject = "hi\r\nX-Injected: yes"
	if _, err := s.Send(context.Background(), base, m); err == nil || !strings.Contains(err.Error(), "single line") {
		t.Errorf("CRLF in subject accepted: %v", err)
	}
	if n := len(srv.Messages()); n != 0 {
		t.Fatalf("%d invalid messages reached the relay", n)
	}
}

func TestSend_DialTimeout(t *testing.T) {
	// 192.0.2.1 is TEST-NET-1: nothing answers, so the dial waits out the timeout.
	s := &kmail.Sender{Timeout: 200 * time.Millisecond}
	cfg := kmail.Config{Host: "192.0.2.1", Port: 25, Security: kmail.SecurityNone, From: "kraken@example.com"}
	start := time.Now()
	_, err := s.Send(context.Background(), cfg, msg)
	if err == nil || !strings.Contains(err.Error(), "could not reach the relay") {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("dial took %s with a 200ms timeout", time.Since(start))
	}
}

func TestRender_TestMessage(t *testing.T) {
	d := kmail.TestData{PanelHost: "kraken.example.com", Username: "ben", Relay: "smtp.example.com:587", Security: "starttls", SentAt: "17 Oct 2026, 12:00 UTC"}
	text, html, err := kmail.Render("test", d)
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{text, html} {
		if strings.Contains(part, "{{") || !strings.Contains(part, "kraken.example.com") || !strings.Contains(part, "smtp.example.com:587") {
			t.Errorf("rendered part is wrong:\n%s", part)
		}
	}
	if kmail.TestSubject(d) != "Test message from the Kraken panel at kraken.example.com" {
		t.Errorf("subject = %q", kmail.TestSubject(d))
	}
	d.PanelHost = ""
	if kmail.TestSubject(d) != "Test message from the Kraken panel" {
		t.Errorf("subject without host = %q", kmail.TestSubject(d))
	}
	if _, _, err := kmail.Render("nope", d); err == nil {
		t.Error("unknown template rendered")
	}
}
