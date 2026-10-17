// Package mail sends the Panel's outbound email over SMTP: account invites and
// the test message the Integrations panel sends. It is the standard library's
// net/smtp with the three things that package leaves to the caller decided
// here — which TLS the connection gets, which authentication mechanism is
// used, and what a message looks like on the wire — plus the server's final
// reply captured, so an operator chasing a bounce has the line the relay
// answered with.
//
// The configuration is read from the Panel's stored settings on every send,
// so a save in the UI takes effect on the next message with no restart.
package mail

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	netmail "net/mail"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"
)

// Security is how the connection to the relay is protected.
type Security string

const (
	// SecurityStartTLS opens a plain connection and upgrades it with STARTTLS
	// before anything else is said. The upgrade is required: a server that
	// does not offer it is refused, never talked to in the clear. The usual
	// choice, on port 587.
	SecurityStartTLS Security = "starttls"
	// SecurityTLS opens a TLS connection from the first byte (implicit TLS),
	// usually on port 465.
	SecurityTLS Security = "tls"
	// SecurityNone talks in the clear. For a relay on your own network only;
	// authentication over it is refused unless the relay is on this host.
	SecurityNone Security = "none"
)

// ParseSecurity reads a stored security value. Empty is STARTTLS, the default.
func ParseSecurity(s string) (Security, bool) {
	switch Security(strings.ToLower(strings.TrimSpace(s))) {
	case "", SecurityStartTLS:
		return SecurityStartTLS, true
	case SecurityTLS:
		return SecurityTLS, true
	case SecurityNone:
		return SecurityNone, true
	}
	return "", false
}

// DefaultPort is the submission port, used when the configured port is 0.
const DefaultPort = 587

// Config is one relay's settings — the SMTP fields of store.Settings.
type Config struct {
	Host     string
	Port     int
	Security Security
	Username string
	Password string
	// From is the bare sender address; FromName is its display name.
	From     string
	FromName string
}

// Configured reports whether there is enough here to send anything: a relay
// host and a sender address.
func (c Config) Configured() bool { return c.Host != "" && c.From != "" }

// Validate reports the first thing wrong with the configuration. An
// unconfigured Config is valid: it just cannot send.
func (c Config) Validate() error {
	if strings.ContainsAny(c.Host, " \r\n\t/") {
		return errors.New("the smtp host must be a hostname or address, with no scheme or path")
	}
	if c.Port < 0 || c.Port > 65535 {
		return fmt.Errorf("the smtp port must be between 1 and 65535, got %d", c.Port)
	}
	if _, ok := ParseSecurity(string(c.Security)); !ok {
		return fmt.Errorf("the smtp security mode must be %s, %s or %s, got %q", SecurityStartTLS, SecurityTLS, SecurityNone, c.Security)
	}
	if c.From != "" {
		a, err := netmail.ParseAddress(c.From)
		if err != nil || a.Name != "" || a.Address != c.From {
			return fmt.Errorf("the from address must be a bare address such as kraken@example.com, got %q", c.From)
		}
	}
	if err := noLineBreaks("from name", c.FromName); err != nil {
		return err
	}
	return noLineBreaks("username", c.Username)
}

func (c Config) port() int {
	if c.Port == 0 {
		return DefaultPort
	}
	return c.Port
}

func (c Config) security() Security {
	s, _ := ParseSecurity(string(c.Security))
	return s
}

// Message is one email: one recipient, a subject, and the same content as
// text and as HTML. Both parts are sent (multipart/alternative, text first)
// so a client that shows no HTML still reads the whole message.
type Message struct {
	To      string
	Subject string
	Text    string
	HTML    string
}

// Result is what a successful send leaves behind: the Message-ID the Panel
// stamped on the message and the relay's final reply to it, so a bounce or a
// relay-side log can be matched to this send.
type Result struct {
	MessageID   string
	ServerReply string
}

// Sender sends messages. The zero value is ready to use; the fields are the
// knobs a test turns.
type Sender struct {
	// Timeout bounds the dial and every reply after it. Zero is 15 seconds.
	Timeout time.Duration
	// RootCAs, when set, replaces the system roots for the TLS handshake. It
	// exists so a test can present a self-signed relay; the Panel never sets
	// it, and never skips verification.
	RootCAs *x509.CertPool
	// Now is the clock for the Date header; nil is time.Now.
	Now func() time.Time
}

func (s *Sender) timeout() time.Duration {
	if s.Timeout > 0 {
		return s.Timeout
	}
	return 15 * time.Second
}

func (s *Sender) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Send delivers one message through the configured relay. The error text is
// written for the operator who configured the relay and names the step that
// failed; it never contains the password.
func (s *Sender) Send(ctx context.Context, cfg Config, msg Message) (Result, error) {
	if !cfg.Configured() {
		return Result{}, errors.New("outbound email is not configured: set an smtp host and a from address first")
	}
	if err := cfg.Validate(); err != nil {
		return Result{}, err
	}
	to, err := netmail.ParseAddress(msg.To)
	if err != nil {
		return Result{}, fmt.Errorf("the recipient %q is not an email address", msg.To)
	}
	if err := noLineBreaks("subject", msg.Subject); err != nil {
		return Result{}, err
	}

	id := messageID(cfg.From)
	body, err := s.build(cfg, *to, msg, id)
	if err != nil {
		return Result{}, err
	}

	c, err := s.connect(ctx, cfg)
	if err != nil {
		return Result{}, err
	}
	defer c.Close()

	if cfg.Username != "" {
		if err := authenticate(c, cfg); err != nil {
			return Result{}, err
		}
	}
	if err := c.Mail(cfg.From); err != nil {
		return Result{}, fmt.Errorf("the relay refused the sender %s: %w", cfg.From, err)
	}
	if err := c.Rcpt(to.Address); err != nil {
		return Result{}, fmt.Errorf("the relay refused the recipient %s: %w", to.Address, err)
	}
	reply, err := data(c, body)
	if err != nil {
		return Result{}, err
	}
	// A failed QUIT after a 250 is not a failed send; the message is queued.
	_ = c.Quit()
	return Result{MessageID: id, ServerReply: reply}, nil
}

// connect dials the relay and, for STARTTLS, upgrades the connection. The
// whole conversation runs under one deadline.
func (s *Sender) connect(ctx context.Context, cfg Config) (*smtp.Client, error) {
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.port()))
	// The real clock, not s.now(): that one dates the message, this one bounds
	// the network.
	deadline := time.Now().Add(s.timeout())
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	dialer := &net.Dialer{Deadline: deadline}
	tlsCfg := &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12, RootCAs: s.RootCAs}

	var conn net.Conn
	var err error
	switch cfg.security() {
	case SecurityTLS:
		conn, err = (&tls.Dialer{NetDialer: dialer, Config: tlsCfg}).DialContext(ctx, "tcp", addr)
	default:
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return nil, fmt.Errorf("could not reach the relay at %s: %w", addr, err)
	}
	_ = conn.SetDeadline(deadline)

	c, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("the relay at %s did not greet: %w", addr, err)
	}
	if cfg.security() == SecurityStartTLS {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			c.Close()
			return nil, fmt.Errorf("the relay at %s does not offer STARTTLS; use implicit tls (usually port 465) or, for a relay on your own network only, none", addr)
		}
		if err := c.StartTLS(tlsCfg); err != nil {
			c.Close()
			return nil, fmt.Errorf("STARTTLS with %s failed: %w", addr, err)
		}
	}
	return c, nil
}

// authenticate picks the strongest mechanism the relay offers that we
// implement: PLAIN over TLS, else CRAM-MD5, else LOGIN. PLAIN and LOGIN both
// refuse an unencrypted connection to anything but this host — net/smtp's own
// rule for PLAIN, matched for LOGIN — so a password never leaves the machine
// in the clear.
func authenticate(c *smtp.Client, cfg Config) error {
	ok, params := c.Extension("AUTH")
	if !ok {
		return errors.New("the relay does not offer authentication; leave the username empty")
	}
	var auth smtp.Auth
	mechs := " " + strings.ToUpper(params) + " "
	switch {
	case strings.Contains(mechs, " PLAIN "):
		auth = smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)
	case strings.Contains(mechs, " CRAM-MD5 "):
		auth = smtp.CRAMMD5Auth(cfg.Username, cfg.Password)
	case strings.Contains(mechs, " LOGIN "):
		auth = &loginAuth{username: cfg.Username, password: cfg.Password, host: cfg.Host}
	default:
		return fmt.Errorf("the relay offers no authentication mechanism the panel speaks (it offers %s; the panel speaks PLAIN, CRAM-MD5 and LOGIN)", strings.TrimSpace(params))
	}
	if err := c.Auth(auth); err != nil {
		return fmt.Errorf("the relay rejected the credentials for %s: %w", cfg.Username, err)
	}
	return nil
}

// data runs the DATA step by hand rather than through Client.Data, so the
// relay's 250 reply — "2.0.0 OK queued as 4Yx…" — comes back to the caller.
// Client.Data reads and discards it.
func data(c *smtp.Client, body []byte) (string, error) {
	id, err := c.Text.Cmd("DATA")
	if err != nil {
		return "", fmt.Errorf("sending DATA: %w", err)
	}
	c.Text.StartResponse(id)
	_, _, err = c.Text.ReadResponse(354)
	c.Text.EndResponse(id)
	if err != nil {
		return "", fmt.Errorf("the relay refused the message body: %w", err)
	}
	w := c.Text.DotWriter()
	if _, err := w.Write(body); err != nil {
		return "", fmt.Errorf("writing the message: %w", err)
	}
	if err := w.Close(); err != nil {
		return "", fmt.Errorf("writing the message: %w", err)
	}
	_, reply, err := c.Text.ReadResponse(250)
	if err != nil {
		return "", fmt.Errorf("the relay did not accept the message: %w", err)
	}
	return reply, nil
}

// build renders the message on the wire: headers, then a multipart/alternative
// body with the text part first and the HTML part second, both quoted-printable.
func (s *Sender) build(cfg Config, to netmail.Address, msg Message, id string) ([]byte, error) {
	from := netmail.Address{Name: cfg.FromName, Address: cfg.From}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	h := textproto.MIMEHeader{}
	h.Set("From", from.String())
	h.Set("To", to.String())
	h.Set("Subject", mime.QEncoding.Encode("utf-8", msg.Subject))
	h.Set("Date", s.now().Format(time.RFC1123Z))
	h.Set("Message-ID", id)
	h.Set("MIME-Version", "1.0")
	// Marks the message as sent by a program, so auto-responders stay quiet.
	h.Set("Auto-Submitted", "auto-generated")
	h.Set("Content-Type", `multipart/alternative; boundary="`+mw.Boundary()+`"`)
	for _, k := range []string{"From", "To", "Subject", "Date", "Message-ID", "MIME-Version", "Auto-Submitted", "Content-Type"} {
		buf.WriteString(k + ": " + h.Get(k) + "\r\n")
	}
	buf.WriteString("\r\n")

	for _, part := range []struct{ ctype, body string }{
		{"text/plain; charset=utf-8", msg.Text},
		{"text/html; charset=utf-8", msg.HTML},
	} {
		if part.body == "" {
			continue
		}
		pw, err := mw.CreatePart(textproto.MIMEHeader{
			"Content-Type":              {part.ctype},
			"Content-Transfer-Encoding": {"quoted-printable"},
		})
		if err != nil {
			return nil, err
		}
		qp := quotedprintable.NewWriter(pw)
		if _, err := io.WriteString(qp, part.body); err != nil {
			return nil, err
		}
		if err := qp.Close(); err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// messageID mints a Message-ID in the sender's domain: 16 random bytes, so two
// Panels sending at the same instant cannot collide.
func messageID(from string) string {
	domain := "kraken.invalid"
	if at := strings.LastIndexByte(from, '@'); at >= 0 && at < len(from)-1 {
		domain = from[at+1:]
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("mail: rand: " + err.Error()) // crypto/rand failure is unrecoverable
	}
	return "<" + hex.EncodeToString(b) + "@" + domain + ">"
}

// noLineBreaks refuses a header value that could end its line and start
// another header — the one injection a mail sender has to care about.
func noLineBreaks(what, v string) error {
	if strings.ContainsAny(v, "\r\n") {
		return fmt.Errorf("the %s must be a single line", what)
	}
	return nil
}
