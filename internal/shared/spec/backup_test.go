package spec

import (
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// A spec with no backup block is the common case and must stay valid; the
// distinction the Panel relies on is nil vs. present, so an EMPTY block still
// has to survive as a declared (non-nil) block.
func TestBackupBlockOptional(t *testing.T) {
	s := validSpec()
	if s.Backup != nil {
		t.Fatal("baseline spec should carry no backup block")
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("spec without a backup block: %v", err)
	}
	inc, exc := s.Backup.Patterns() // nil receiver must be safe
	if inc != nil || exc != nil {
		t.Fatalf("nil block gave include=%v exclude=%v", inc, exc)
	}

	s.Backup = &Backup{}
	if err := s.Validate(); err != nil {
		t.Fatalf("spec with an empty backup block: %v", err)
	}
}

// The block round-trips through YAML the way a bundled spec is authored: the
// keys are `backup.include` / `backup.exclude`, and a quoted glob survives.
func TestBackupBlockYAMLRoundTrip(t *testing.T) {
	const doc = `
name: Palworld
slug: palworld
platforms:
  - { kind: linux-native, image: img }
install: { script: "true" }
startup:
  command: ./PalServer.sh
  stop: { type: signal, value: SIGINT }
ports:
  - { name: game, protocol: udp, default: 8211, required: true }
resources: { min_memory_mb: 8192 }
backup:
  include:
    - Pal/Saved/**
  exclude:
    - Pal/Saved/Logs/**
    - "**/*.log"
`
	var s Spec
	if err := yaml.Unmarshal([]byte(doc), &s); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	inc, exc := s.Backup.Patterns()
	if len(inc) != 1 || inc[0] != "Pal/Saved/**" {
		t.Fatalf("include = %v", inc)
	}
	if len(exc) != 2 || exc[0] != "Pal/Saved/Logs/**" || exc[1] != "**/*.log" {
		t.Fatalf("exclude = %v", exc)
	}

	// Marshal → unmarshal keeps the block identical (the UI's spec editor
	// round-trips the document verbatim, so a lossy field would drop saves).
	out, err := yaml.Marshal(&s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var again Spec
	if err := yaml.Unmarshal(out, &again); err != nil {
		t.Fatalf("re-unmarshal: %v", err)
	}
	inc2, exc2 := again.Backup.Patterns()
	if strings.Join(inc2, ",") != strings.Join(inc, ",") || strings.Join(exc2, ",") != strings.Join(exc, ",") {
		t.Fatalf("round-trip changed the block: include=%v exclude=%v", inc2, exc2)
	}
}

// A glob that cannot compile — or one written as a path the matcher will never
// see — must be refused at save time. Silently keeping it would mean an include
// list that matches nothing and a backup with no save in it.
func TestBackupBlockRejectsBadGlobs(t *testing.T) {
	cases := []struct {
		name  string
		block *Backup
		want  string
	}{
		{"unclosed class in include", &Backup{Include: []string{"saves/[abc"}}, "not a valid glob"},
		{"unclosed class in exclude", &Backup{Exclude: []string{"**/*.[log"}}, "not a valid glob"},
		{"empty pattern", &Backup{Include: []string{"saves/**", "  "}}, "pattern is empty"},
		{"absolute path", &Backup{Include: []string{"/data/savegame/**"}}, "data-dir-relative"},
		{"windows path", &Backup{Include: []string{`C:\data\save\**`}}, "POSIX separators"},
		{"windows path in exclude", &Backup{Exclude: []string{`logs\**`}}, "POSIX separators"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := validSpec()
			s.Backup = c.block
			err := s.Validate()
			if err == nil {
				t.Fatalf("expected a validation error for %+v", c.block)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %q should mention %q", err, c.want)
			}
		})
	}
}

// Every glob shape the bundled specs and the Panel's built-in list use must pass
// validation — a false rejection here would break spec saves outright.
func TestBackupBlockAcceptsShippedPatterns(t *testing.T) {
	s := validSpec()
	s.Backup = &Backup{
		Include: []string{"savegame/**", "enshrouded_server.json", "saves/**", "*.json", "mods/**", "Pal/Saved/**", "save/**", "BepInEx/**"},
		Exclude: []string{"steamapps/downloading/**", "**/logs/**", "**/Logs/**", "**/*.log", "**/*.dmp", "**/CrashDumps/**", "**/Crashes/**"},
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("shipped patterns rejected: %v", err)
	}
}
