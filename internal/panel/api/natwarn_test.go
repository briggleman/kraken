package api

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/briggleman/kraken/internal/panel/config"
	"github.com/briggleman/kraken/internal/panel/store/memory"
)

func armedDetector() *natDetector {
	d := &natDetector{}
	d.arm(true)
	return d
}

// The signature is unanimity: every audited request so far resolving to the
// same private address. It takes a full sample to say so, and it says so once.
func TestNATDetectorWarnsOnceAfterAFullSample(t *testing.T) {
	d := armedDetector()
	for i := range natSampleSize - 1 {
		if d.observe("192.168.65.1") {
			t.Fatalf("warned on sample %d, before the sample was complete", i+1)
		}
	}
	if !d.observe("192.168.65.1") {
		t.Fatalf("no warning after %d identical private clients", natSampleSize)
	}
	for range 50 {
		if d.observe("192.168.65.1") {
			t.Fatal("warned twice — this is a once-per-process diagnostic, not a log line per request")
		}
	}
}

// Two distinct clients means the Panel can tell callers apart, which is the
// thing the warning would be complaining it cannot do. One is enough to settle
// it for the life of the process.
func TestNATDetectorStopsAtTheSecondDistinctClient(t *testing.T) {
	d := armedDetector()
	for range natSampleSize - 1 {
		d.observe("10.0.0.1")
	}
	d.observe("10.0.0.2")
	for range natSampleSize * 2 {
		if d.observe("10.0.0.1") {
			t.Fatal("warned although two distinct clients had been seen")
		}
	}
}

// A public client address is a real client. Nothing to diagnose, and nothing
// left to watch for.
func TestNATDetectorIgnoresPublicClients(t *testing.T) {
	d := armedDetector()
	if d.observe("198.51.100.7") {
		t.Fatal("warned about a public address")
	}
	for range natSampleSize * 2 {
		if d.observe("192.168.65.1") {
			t.Fatal("warned after a public client had already answered the question")
		}
	}
}

// An operator who has named their proxy has already been told this story, and
// the addresses they see now come from the forwarded chain rather than the peer.
func TestNATDetectorIsDisarmedWhenAProxyIsTrusted(t *testing.T) {
	d := &natDetector{}
	d.arm(false)
	for range natSampleSize * 2 {
		if d.observe("192.168.65.1") {
			t.Fatal("warned although a trusted proxy is configured")
		}
	}
}

// The warning reaches the log once, names the address, points at the page that
// explains the fix, and — because the heuristic cannot tell a NAT from a Panel
// with one operator on it — offers BOTH readings rather than asserting the
// alarming one. It stays quiet for an address the operator has already
// exempted from the per-IP limiters, because they plainly know.
func TestNATWarningIsLoggedOnceAndSuppressedForAnExemptAddress(t *testing.T) {
	logged := func(skip []string) string {
		var buf bytes.Buffer
		cfg := &config.Config{Env: "test", SessionTTL: time.Hour, RateLimitIPSkip: skip}
		s := New(cfg, memory.New(), slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{
			Level: slog.LevelWarn,
		})))
		buf.Reset() // drop anything New itself logged
		for range natSampleSize + 5 {
			s.noteClientIP("192.168.65.1")
		}
		return buf.String()
	}

	out := logged(nil)
	if n := strings.Count(out, natWarnMarker); n != 1 {
		t.Fatalf("the NAT warning was logged %d times, want exactly 1:\n%s", n, out)
	}
	if !strings.Contains(out, "192.168.65.1") {
		t.Fatalf("the warning does not name the address:\n%s", out)
	}
	if !strings.Contains(out, "reverse-proxy") {
		t.Fatalf("the warning does not point at the docs:\n%s", out)
	}
	// Both readings, or the message is wrong for the single-operator Panel that
	// produces exactly the same twenty samples — and the fix it names would be
	// a misconfiguration talked into existence by a log line.
	if !strings.Contains(out, "If more than one person reaches this Panel") {
		t.Fatalf("the warning asserts the NAT rather than offering it as a reading:\n%s", out)
	}
	if !strings.Contains(out, "If you are the only client") {
		t.Fatalf("the warning does not say the benign reading is expected:\n%s", out)
	}

	if out := logged([]string{"192.168.65.1"}); strings.Contains(out, natWarnMarker) {
		t.Fatalf("warned about an address the operator had already exempted:\n%s", out)
	}
}

// natWarnMarker is a phrase unique to the warning, used to count occurrences.
const natWarnMarker = "rewriting every client to that address"
