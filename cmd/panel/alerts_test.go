package main

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/briggleman/kraken/internal/panel/config"
	"github.com/briggleman/kraken/internal/panel/store/memory"
)

func TestInsecureRelayToken(t *testing.T) {
	cases := []struct {
		url, token string
		want       bool
	}{
		{"http://push.example.com", "t", true},
		{"http://203.0.113.7:8787", "t", true},
		{"https://push.example.com", "t", false},
		{"http://push.example.com", "", false},
		{"http://127.0.0.1:8787", "t", false},
		{"http://[::1]:8787", "t", false},
		{"http://localhost:8787", "t", false},
		{"http://relay.localhost", "t", false},
		{"http://192.168.0.88:8787", "t", false},
		{"http://10.1.2.3", "t", false},
		{"http://172.18.0.5", "t", false},
		{"http://relay.lan:8787", "t", true}, // a name is not known to be private
	}
	for _, c := range cases {
		if got := insecureRelayToken(c.url, c.token); got != c.want {
			t.Errorf("insecureRelayToken(%q, token=%v) = %v, want %v", c.url, c.token != "", got, c.want)
		}
	}
}

// The startup line says whether push is on and to which host, and never the
// token or the relay's path.
func TestBuildAlertsLogsTheRelayHostOnly(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	st := memory.New()

	d := buildAlerts(context.Background(), &config.Config{}, st, logger)
	if d.Enabled() || !strings.Contains(buf.String(), "push alerts off") {
		t.Fatalf("no relay: enabled %v, log %q", d.Enabled(), buf.String())
	}

	buf.Reset()
	cfg := &config.Config{PushRelayURL: "http://push.example.com/kraken?answer=503", PushRelayToken: "s3cret-relay-token"}
	d = buildAlerts(context.Background(), cfg, st, logger)
	out := buf.String()
	if !d.Enabled() || !strings.Contains(out, "push alerts on") || !strings.Contains(out, "relay_host=push.example.com") {
		t.Fatalf("relay set: enabled %v, log %q", d.Enabled(), out)
	}
	if strings.Contains(out, "s3cret-relay-token") || strings.Contains(out, "answer=503") || strings.Contains(out, "/kraken") {
		t.Fatalf("the startup log leaks the token or the relay's path: %q", out)
	}
	if !strings.Contains(out, "sent in the clear") {
		t.Fatalf("no warning for a token over plain http to a public host: %q", out)
	}
}
