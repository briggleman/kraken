package api

import (
	"slices"
	"strings"
	"testing"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/briggleman/kraken/internal/shared/spec"
)

// A spec that declares a backup block owns the whole policy: its globs are
// passed through and the built-in excludes are NOT merged underneath. Merging
// would make "capture all of this" inexpressible.
func TestBackupGlobsForSpecWithBlock(t *testing.T) {
	sp := &spec.Spec{Slug: "palworld", Backup: &spec.Backup{
		Include: []string{"Pal/Saved/**"},
		Exclude: []string{"Pal/Saved/Logs/**"},
	}}
	inc, exc := backupGlobsFor(sp)
	if !slices.Equal(inc, []string{"Pal/Saved/**"}) {
		t.Fatalf("include = %v", inc)
	}
	if !slices.Equal(exc, []string{"Pal/Saved/Logs/**"}) {
		t.Fatalf("exclude = %v", exc)
	}
	for _, builtin := range builtinBackupExcludes {
		if slices.Contains(exc, builtin) {
			t.Fatalf("built-in %q leaked into a spec that declares its own policy", builtin)
		}
	}
	// The returned slices must be copies — the caller hands them to a proto
	// message, and aliasing the spec's own slices invites action at a distance.
	inc[0] = "mutated"
	if sp.Backup.Include[0] != "Pal/Saved/**" {
		t.Fatal("backupGlobsFor aliased the spec's include slice")
	}
}

// No block ⇒ the built-in policy: capture EVERYTHING (empty include) minus the
// ephemeral-only excludes. An include list here would risk dropping saves.
func TestBackupGlobsForSpecWithoutBlock(t *testing.T) {
	inc, exc := backupGlobsFor(&spec.Spec{Slug: "windrose"})
	if len(inc) != 0 {
		t.Fatalf("include = %v, want empty — the fallback must never guess a save dir", inc)
	}
	if !slices.Equal(exc, builtinBackupExcludes) {
		t.Fatalf("exclude = %v, want the built-in list", exc)
	}
	exc[0] = "mutated"
	if builtinBackupExcludes[0] == "mutated" {
		t.Fatal("backupGlobsFor handed out the built-in list itself, not a copy")
	}
}

// An unreadable spec resolves like a spec with no block rather than skipping the
// filtering: the built-ins only drop ephemera, so they are the safe answer.
func TestBackupGlobsForNilSpec(t *testing.T) {
	inc, exc := backupGlobsFor(nil)
	if len(inc) != 0 || !slices.Equal(exc, builtinBackupExcludes) {
		t.Fatalf("nil spec gave include=%v exclude=%v", inc, exc)
	}
}

// The built-in list is a promise: every entry is unambiguously ephemeral or
// re-downloadable. Guard the two ways that promise gets broken — an include
// pattern sneaking in (this list is excludes only), and a pattern that could
// swallow save data because it names no specific ephemeral thing.
func TestBuiltinBackupExcludesStayConservative(t *testing.T) {
	for _, p := range builtinBackupExcludes {
		if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, `\`) {
			t.Errorf("built-in %q is not a data-dir-relative POSIX glob", p)
		}
		// "**" or "**/*" would exclude the entire data dir.
		if p == "**" || p == "**/*" || p == "*" {
			t.Errorf("built-in %q would exclude everything", p)
		}
	}
	// Sanity: the shapes an operator would expect to keep must not be excluded
	// by the fallback policy. Mirrors the agent-side matcher.
	mustKeep := []string{
		"savegame/world.db",
		"Pal/Saved/SaveGames/0/Level.sav",
		"save/worlds_local/Dedicated.db",
		"saves/world.zip",
		"steamapps/appmanifest_2394010.acf",
		"steamapps/workshop/content/1234/mod.pak",
		"BepInEx/plugins/Mod.dll",
		"enshrouded_server.json",
	}
	for _, path := range mustKeep {
		for _, p := range builtinBackupExcludes {
			if ok, err := doublestar.Match(p, path); ok && err == nil {
				t.Errorf("built-in exclude %q would drop %q", p, path)
			}
		}
	}
}
