package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExpandBackupPath(t *testing.T) {
	cases := []struct {
		name, in, slug, want string
	}{
		{"slug token", "/media/games/{{SLUG}}/backup", "palworld", "/media/games/palworld/backup"},
		{"no token", "/mnt/nas/backups", "palworld", "/mnt/nas/backups"},
		{"empty path", "", "palworld", ""},
		{"repeated token", "{{SLUG}}/{{SLUG}}", "valheim", "valheim/valheim"},
		{"windows path", `Z:\kraken\{{SLUG}}`, "factorio", `Z:\kraken\factorio`},
		// A hostile slug can't inject path separators or traversal: separators
		// become '-' and leading dots/dashes are trimmed, so it stays one segment.
		{"sanitized traversal", "/backups/{{SLUG}}", "../../etc", "/backups/etc"},
		{"sanitized separators", "/backups/{{SLUG}}", "a/b\\c", "/backups/a-b-c"},
		{"empty slug falls back", "/backups/{{SLUG}}", "", "/backups/unknown"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := expandBackupPath(c.in, c.slug); got != c.want {
				t.Fatalf("expandBackupPath(%q, %q) = %q, want %q", c.in, c.slug, got, c.want)
			}
		})
	}
}

func TestLocalTargetServerDir(t *testing.T) {
	// Default (zero-config) target namespaces per server.
	nested := &localBackupTarget{dir: "/var/backups"}
	if got, want := nested.serverDir("srv1"), filepath.Join("/var/backups", "srv1"); got != want {
		t.Fatalf("nested serverDir = %q, want %q", got, want)
	}
	// A configured (flat) target writes archives directly in the path — no subdir.
	flat := &localBackupTarget{dir: "/media/games/palworld/backup", flat: true}
	if got, want := flat.serverDir("srv1"), "/media/games/palworld/backup"; got != want {
		t.Fatalf("flat serverDir = %q, want %q", got, want)
	}
}

func TestStaticPrefix(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/media/games/{{SLUG}}/backup", "/media/games"},
		{"/mnt/nas/backups", "/mnt/nas/backups"}, // no token → unchanged
		{`Z:\kraken\{{SLUG}}`, `Z:\kraken`},
		{"{{SLUG}}/backup", string(filepath.Separator)},
	}
	for _, c := range cases {
		if got := staticPrefix(c.in); got != c.want {
			t.Fatalf("staticPrefix(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestLocalVerifyCreatesAndProbes(t *testing.T) {
	// A dir that doesn't exist yet is legitimate — verify creates it (Put would).
	dir := filepath.Join(t.TempDir(), "backups", "sub")
	tgt := &localBackupTarget{dir: dir, flat: true}
	if err := tgt.verify(); err != nil {
		t.Fatalf("verify on creatable dir: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("verify did not create the dir: %v", err)
	}
	// A templated path probes only its static prefix — the {{SLUG}} part doesn't
	// exist until a backup runs and must not be created here.
	root := t.TempDir()
	tgt = &localBackupTarget{dir: filepath.Join(root, "games", "{{SLUG}}"), flat: true}
	if err := tgt.verify(); err != nil {
		t.Fatalf("verify on templated path: %v", err)
	}
	if ents, _ := os.ReadDir(filepath.Join(root, "games")); len(ents) != 0 {
		t.Fatalf("verify created past the static prefix: %v", ents)
	}
}

func TestLocalVerifyFailsOnUncreatableDir(t *testing.T) {
	// A path whose parent is a regular FILE cannot be created on any OS — the
	// cross-platform stand-in for a mapped drive letter the service can't see.
	root := t.TempDir()
	blocker := filepath.Join(root, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	tgt := &localBackupTarget{dir: filepath.Join(blocker, "backups"), flat: true}
	if err := tgt.verify(); err == nil {
		t.Fatal("verify succeeded on an uncreatable dir")
	}
	if (&localBackupTarget{}).verify() == nil {
		t.Fatal("verify succeeded on an empty dir")
	}
}

func TestVerifyPrefix(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/games/backups/{{SLUG}}", "/games/backups"},
		{`Z:\games\{{SLUG}}`, `Z:\games`},
		{"/mnt/nas/backups", "/mnt/nas/backups"}, // no token: unchanged
		{"{{SLUG}}", ""},                         // nothing static — probe nothing
		{"{{SLUG}}/backup", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := verifyPrefix(c.in); got != c.want {
			t.Fatalf("verifyPrefix(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
