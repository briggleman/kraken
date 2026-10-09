package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/briggleman/kraken/internal/panel/push"
)

// pushKeygen prints a fresh X25519 key pair for registering a test device: the
// public key goes to POST /devices, the private key to push-relay-stub -key.
func pushKeygen(args []string, out io.Writer) error {
	if len(args) > 0 {
		return fmt.Errorf("push-keygen takes no arguments")
	}
	priv, pub, err := push.GenerateKeyPair()
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "private_key: %s\npublic_key:  %s\n",
		base64.StdEncoding.EncodeToString(priv), base64.StdEncoding.EncodeToString(pub))
	return err
}

// The statuses the stub can be told to answer with, one per path through the
// Panel's relay client: accepted, token dead, refused, rate limited, down.
var stubAnswers = map[string]int{"ok": http.StatusOK, "410": http.StatusGone, "400": http.StatusBadRequest, "429": http.StatusTooManyRequests, "503": http.StatusServiceUnavailable}

// relayStub is a stand-in for the Kraken push relay. It speaks the relay's
// side of docs/design/push-alerts.md, but instead of forwarding to APNs it
// opens each envelope with a device private key and prints the alert.
type relayStub struct {
	key        []byte
	relayToken string
	out        io.Writer

	mu     sync.Mutex
	answer int // the forced status, or 0 to answer as a real relay would
	times  int // how many more requests the forced status applies to; <0 is every one
}

func (s *relayStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/push" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	now := time.Now().Format(time.RFC3339)
	if s.relayToken != "" && r.Header.Get("Authorization") != "Bearer "+s.relayToken {
		s.printf("%s  401  the request did not carry the relay token\n", now)
		http.Error(w, "missing or wrong relay token", http.StatusUnauthorized)
		return
	}

	var req struct {
		Token       string `json:"token"`
		Environment string `json:"environment"`
		Payload     string `json:"payload"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil || req.Token == "" || req.Payload == "" ||
		(req.Environment != push.EnvironmentProduction && req.Environment != push.EnvironmentSandbox) {
		s.printf("%s  400  malformed relay request\n", now)
		http.Error(w, "want {token, environment: production|sandbox, payload}", http.StatusBadRequest)
		return
	}

	status := s.status(r.URL.Query().Get("answer"))
	// A real relay cannot read the envelope, so a bad one is not its
	// business: it would accept the request and the app would show a generic
	// alert. The stub says so instead of refusing, which keeps the Panel's
	// view of the exchange the same as it would be against the real thing.
	p, err := push.Open(s.key, req.Payload)
	if err != nil {
		s.printf("%s  %d  could not open the envelope: %v\n", now, status, err)
	} else {
		s.printf("%s  %d  %s  %s  %s: %s\n", now, status, p.Class, p.Event, p.Title, p.Body)
	}
	w.WriteHeader(status)
}

// status decides this request's answer: the ?answer= query parameter first,
// then the -answer flag while its -times budget lasts, then 200.
func (s *relayStub) status(query string) int {
	if code, ok := stubAnswers[query]; ok {
		return code
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.answer == 0 || s.times == 0 {
		return http.StatusOK
	}
	if s.times > 0 {
		s.times--
	}
	return s.answer
}

func (s *relayStub) printf(format string, a ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = fmt.Fprintf(s.out, format, a...)
}

// pushRelayStub runs the stub relay until interrupted.
func pushRelayStub(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("push-relay-stub", flag.ContinueOnError)
	addr := fs.String("addr", "127.0.0.1:8787", "listen address")
	keyB64 := fs.String("key", "", "the test device's X25519 private key, base64 (generated when empty)")
	answer := fs.String("answer", "ok", "status to answer with: ok, 410, 400, 429 or 503")
	times := fs.Int("times", 0, "answer -answer for only this many requests, then 200 (0 = every request)")
	relayToken := fs.String("relay-token", "", "require Authorization: Bearer <token> (the Panel's KRAKEN_PUSH_RELAY_TOKEN)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	code, ok := stubAnswers[*answer]
	if !ok {
		return fmt.Errorf("-answer %q is not one of ok, 410, 400, 429, 503", *answer)
	}
	if *times < 0 {
		return fmt.Errorf("-times must be 0 or more")
	}

	s := &relayStub{relayToken: *relayToken, out: out, answer: code, times: -1}
	if code == http.StatusOK {
		s.answer = 0
	} else if *times > 0 {
		s.times = *times
	}

	if *keyB64 == "" {
		priv, pub, err := push.GenerateKeyPair()
		if err != nil {
			return err
		}
		s.key = priv
		_, _ = fmt.Fprintf(out, "generated a test device key pair (pass -key to reuse it next time)\n  private_key: %s\n  public_key:  %s   <- register the device with this\n",
			base64.StdEncoding.EncodeToString(priv), base64.StdEncoding.EncodeToString(pub))
	} else {
		priv, err := base64.StdEncoding.DecodeString(strings.TrimSpace(*keyB64))
		if err != nil || len(priv) != push.KeySize {
			return fmt.Errorf("-key must be base64 of a %d-byte X25519 private key", push.KeySize)
		}
		pub, err := push.PublicKeyOf(priv)
		if err != nil {
			return err
		}
		s.key = priv
		_, _ = fmt.Fprintf(out, "device public key: %s\n", base64.StdEncoding.EncodeToString(pub))
	}

	srv := &http.Server{Addr: *addr, Handler: s, ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	forced := "200"
	if s.answer != 0 {
		forced = strconv.Itoa(s.answer)
		if s.times > 0 {
			forced += " for the next " + strconv.Itoa(s.times) + " request(s), then 200"
		}
	}
	_, _ = fmt.Fprintf(out, "push relay stub (development only) on http://%s/v1/push, answering %s; set KRAKEN_PUSH_RELAY_URL=http://%s\n", *addr, forced, *addr)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil {
		return err
	}
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
