// Package smtptest is a small SMTP relay for tests: enough of RFC 5321 to take
// one message from net/smtp (EHLO, STARTTLS, AUTH, MAIL, RCPT, DATA, QUIT) and
// record what it was handed. It verifies nothing about AUTH beyond that it
// happened; what the tests need to know is which path the client took. It is
// used by the mail package's tests and by the API's handler tests, which is
// why it is a package rather than a _test.go file.
package smtptest

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net"
	"strings"
	"sync"
	"time"
)

// Options shape the fake relay.
type Options struct {
	// TLS, when set, is offered as STARTTLS, or used from the first byte when
	// ImplicitTLS is set. SelfSigned builds one.
	TLS         *tls.Config
	ImplicitTLS bool
	// AdvertiseAuth, when non-empty, is the AUTH line EHLO advertises, such as
	// "PLAIN LOGIN". Any AUTH exchange is then accepted after one round of
	// prompts; the recorded message says it happened and with which mechanism.
	AdvertiseAuth string
	// RejectRecipient, when set, is the reply every RCPT TO gets, such as
	// "550 5.1.1 no such user", so a test can see the relay refuse.
	RejectRecipient string
}

// Message is one delivery as the relay saw it.
type Message struct {
	From      string
	To        []string
	Data      string // the message as sent, dot-unstuffed, CRLF line ends
	TLS       bool   // the connection was encrypted when DATA arrived
	AuthMech  string // the AUTH mechanism the client picked, or ""
	HelloName string // what the client said in EHLO
}

// Server is one running fake relay.
type Server struct {
	// Addr is host:port to dial, always on loopback.
	Addr string
	Host string
	Port int

	opts Options
	ln   net.Listener
	mu   sync.Mutex
	msgs []Message
	wg   sync.WaitGroup
}

// Start listens on a loopback port and serves until Close.
func Start(opts Options) (*Server, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	if opts.ImplicitTLS {
		if opts.TLS == nil {
			ln.Close()
			return nil, fmt.Errorf("smtptest: ImplicitTLS needs a TLS config")
		}
		ln = tls.NewListener(ln, opts.TLS)
	}
	s := &Server{Addr: ln.Addr().String(), opts: opts, ln: ln}
	host, port, _ := net.SplitHostPort(s.Addr)
	s.Host = host
	fmt.Sscan(port, &s.Port)
	s.wg.Add(1)
	go s.serve()
	return s, nil
}

// Close stops listening and waits for the connections in flight.
func (s *Server) Close() {
	s.ln.Close()
	s.wg.Wait()
}

// Messages returns every message accepted so far.
func (s *Server) Messages() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Message(nil), s.msgs...)
}

func (s *Server) serve() {
	defer s.wg.Done()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handle(conn)
		}()
	}
}

func (s *Server) handle(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	_, tlsOn := conn.(*tls.Conn)
	r := bufio.NewReader(conn)
	w := bufio.NewWriter(conn)
	reply := func(lines ...string) bool {
		for _, l := range lines {
			w.WriteString(l + "\r\n")
		}
		return w.Flush() == nil
	}
	readLine := func() (string, bool) {
		l, err := r.ReadString('\n')
		if err != nil {
			return "", false
		}
		return strings.TrimRight(l, "\r\n"), true
	}

	if !reply("220 smtptest ESMTP ready") {
		return
	}
	var cur Message
	for {
		line, ok := readLine()
		if !ok {
			return
		}
		verb, arg, _ := strings.Cut(line, " ")
		switch strings.ToUpper(verb) {
		case "EHLO", "HELO":
			cur.HelloName = arg
			ext := []string{"250-smtptest"}
			if s.opts.TLS != nil && !tlsOn {
				ext = append(ext, "250-STARTTLS")
			}
			if s.opts.AdvertiseAuth != "" {
				ext = append(ext, "250-AUTH "+s.opts.AdvertiseAuth)
			}
			ext = append(ext, "250 8BITMIME")
			if !reply(ext...) {
				return
			}
		case "STARTTLS":
			if s.opts.TLS == nil || tlsOn {
				if !reply("502 5.5.1 not available") {
					return
				}
				continue
			}
			if !reply("220 2.0.0 go ahead") {
				return
			}
			tc := tls.Server(conn, s.opts.TLS)
			if err := tc.Handshake(); err != nil {
				return
			}
			conn = tc
			tlsOn = true
			r = bufio.NewReader(conn)
			w = bufio.NewWriter(conn)
		case "AUTH":
			mech, initial, _ := strings.Cut(arg, " ")
			cur.AuthMech = strings.ToUpper(mech)
			// One prompt when the client sent nothing with the command (LOGIN
			// and CRAM-MD5 do), then accept whatever comes back.
			if initial == "" {
				if !reply("334 UGFzc3dvcmQ6") {
					return
				}
				if _, ok := readLine(); !ok {
					return
				}
			}
			if !reply("235 2.7.0 accepted") {
				return
			}
		case "MAIL":
			cur.From = pathOf(strings.TrimPrefix(arg, "FROM:"))
			if !reply("250 2.1.0 ok") {
				return
			}
		case "RCPT":
			if s.opts.RejectRecipient != "" {
				if !reply(s.opts.RejectRecipient) {
					return
				}
				continue
			}
			cur.To = append(cur.To, pathOf(strings.TrimPrefix(arg, "TO:")))
			if !reply("250 2.1.5 ok") {
				return
			}
		case "DATA":
			if !reply("354 go ahead, end with <CRLF>.<CRLF>") {
				return
			}
			var b strings.Builder
			for {
				l, ok := readLine()
				if !ok {
					return
				}
				if l == "." {
					break
				}
				b.WriteString(strings.TrimPrefix(l, ".") + "\r\n")
			}
			cur.Data = b.String()
			cur.TLS = tlsOn
			s.mu.Lock()
			s.msgs = append(s.msgs, cur)
			n := len(s.msgs)
			s.mu.Unlock()
			cur = Message{HelloName: cur.HelloName, AuthMech: cur.AuthMech}
			if !reply(fmt.Sprintf("250 2.0.0 OK queued as smtptest-%d", n)) {
				return
			}
		case "RSET", "NOOP":
			if !reply("250 2.0.0 ok") {
				return
			}
		case "QUIT":
			reply("221 2.0.0 bye")
			return
		default:
			if !reply("500 5.5.1 unknown command") {
				return
			}
		}
	}
}

// pathOf reads the address out of a MAIL FROM / RCPT TO argument: the part in
// angle brackets, with any ESMTP parameters after it (BODY=8BITMIME) dropped.
func pathOf(arg string) string {
	arg = strings.TrimSpace(arg)
	if i := strings.IndexByte(arg, '>'); i >= 0 {
		arg = arg[:i]
	}
	arg, _, _ = strings.Cut(arg, " ")
	return strings.TrimPrefix(arg, "<")
}

// SelfSigned makes a certificate for host good for an hour, returning the TLS
// config the fake relay serves with and the pool a client needs to trust it.
func SelfSigned(host string) (*tls.Config, *x509.CertPool, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(host); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, err
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	srv := &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		MinVersion:   tls.VersionTLS12,
	}
	return srv, pool, nil
}
