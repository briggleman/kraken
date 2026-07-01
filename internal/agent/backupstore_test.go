package agent

import (
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
