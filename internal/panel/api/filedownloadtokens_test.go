package api

import (
	"errors"
	"testing"
	"time"
)

// The registry's own contract, away from HTTP: a token is good exactly once,
// stops working when it ages out, and leaves nothing behind either way.
func TestDownloadTokenRegistrySingleUse(t *testing.T) {
	reg := newDownloadTokenRegistry()
	tok, exp, err := reg.issue("srv-1", "user-1", downloadKindRaw, []string{"/data/server.cfg"}, downloadTokenTTL)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if tok == "" {
		t.Fatal("issue returned an empty token")
	}
	if time.Until(exp) > downloadTokenTTL+time.Second {
		t.Fatalf("expiry %s is beyond the %s TTL", exp, downloadTokenTTL)
	}
	if n := reg.count(); n != 1 {
		t.Fatalf("outstanding grants = %d, want 1", n)
	}

	g, err := reg.redeem(tok)
	if err != nil {
		t.Fatalf("first redeem: %v", err)
	}
	if g.serverID != "srv-1" || g.userID != "user-1" || g.kind != downloadKindRaw {
		t.Fatalf("grant = %+v, want the values it was minted with", g)
	}
	if len(g.paths) != 1 || g.paths[0] != "/data/server.cfg" {
		t.Fatalf("grant paths = %v, want the bound path", g.paths)
	}
	// Redemption deletes: the table is empty and the same token is now unknown.
	if n := reg.count(); n != 0 {
		t.Fatalf("outstanding grants after redeem = %d, want 0", n)
	}
	if _, err := reg.redeem(tok); !errors.Is(err, errDownloadTokenUnknown) {
		t.Fatalf("second redeem err = %v, want %v", err, errDownloadTokenUnknown)
	}
}

// backdate ages every outstanding grant past its expiry, the way 60 seconds of
// wall-clock would. Issuing with a negative TTL would not do: issue sweeps, so
// the grant would be gone rather than expired, and "expired" is the branch
// under test.
func backdate(reg *downloadTokenRegistry) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	for k, g := range reg.grants {
		g.expires = time.Now().Add(-time.Second)
		reg.grants[k] = g
	}
}

func TestDownloadTokenRegistryExpiryAndUnknown(t *testing.T) {
	reg := newDownloadTokenRegistry()
	tok, _, err := reg.issue("srv-1", "user-1", downloadKindZip, []string{"/data/saves"}, downloadTokenTTL)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	backdate(reg)
	if _, err := reg.redeem(tok); !errors.Is(err, errDownloadTokenExpired) {
		t.Fatalf("redeem of an expired token = %v, want %v", err, errDownloadTokenExpired)
	}
	// Even a rejected redemption consumes the grant.
	if n := reg.count(); n != 0 {
		t.Fatalf("outstanding grants after an expired redeem = %d, want 0", n)
	}
	for _, bad := range []string{"", "not-a-token", "00"} {
		if _, err := reg.redeem(bad); !errors.Is(err, errDownloadTokenUnknown) {
			t.Fatalf("redeem(%q) = %v, want %v", bad, err, errDownloadTokenUnknown)
		}
	}
}

// Issuing sweeps grants nobody can redeem any more, so an idle Panel does not
// accumulate them.
func TestDownloadTokenRegistrySweepsExpired(t *testing.T) {
	reg := newDownloadTokenRegistry()
	for range 5 {
		if _, _, err := reg.issue("srv-1", "user-1", downloadKindRaw, []string{"/data/x"}, downloadTokenTTL); err != nil {
			t.Fatalf("issue: %v", err)
		}
	}
	backdate(reg)
	if _, _, err := reg.issue("srv-1", "user-1", downloadKindRaw, []string{"/data/x"}, downloadTokenTTL); err != nil {
		t.Fatalf("issue: %v", err)
	}
	if n := reg.count(); n != 1 {
		t.Fatalf("outstanding grants = %d, want 1 (the live one)", n)
	}
}

func TestCanonicalFilePath(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"/data/server.cfg", "/data/server.cfg", true},
		{"/data//saves///world.sav", "/data/saves/world.sav", true},
		{`\data\saves`, "/data/saves", true},
		{"/data/saves/../server.cfg", "/data/server.cfg", true},
		{"C:/data/server.cfg", "C:/data/server.cfg", true},
		{"  /data/server.cfg  ", "/data/server.cfg", true},
		{"", "", false},
		{"..", "", false},
		{"../../etc/passwd", "", false},
	}
	for _, c := range cases {
		got, ok := canonicalFilePath(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("canonicalFilePath(%q) = (%q, %v), want (%q, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
	if _, ok := canonicalFilePaths(nil); ok {
		t.Error("canonicalFilePaths(nil) accepted an empty set")
	}
	tooMany := make([]string, maxTokenPaths+1)
	for i := range tooMany {
		tooMany[i] = "/data/x"
	}
	if _, ok := canonicalFilePaths(tooMany); ok {
		t.Errorf("canonicalFilePaths accepted %d paths, over the %d cap", len(tooMany), maxTokenPaths)
	}
}
