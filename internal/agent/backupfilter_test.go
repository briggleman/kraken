package agent

import "testing"

// Both lists empty must yield a nil filter — that is what keeps every pre-#218
// caller (and the whole-dir archiveTree path) byte-for-byte unchanged.
func TestNewBackupFilterEmptyIsNil(t *testing.T) {
	if f := newBackupFilter(nil, nil); f != nil {
		t.Fatalf("empty lists gave %+v, want nil", f)
	}
	if f := newBackupFilter([]string{"", "   "}, []string{""}); f != nil {
		t.Fatalf("blank-only lists gave %+v, want nil", f)
	}
	// A nil filter captures everything and prunes nothing.
	var nilf *backupFilter
	if !nilf.keepFile("anything/at/all") || nilf.pruneDir("steamapps") {
		t.Fatal("nil filter must capture everything")
	}
}

// An uncompilable pattern must be dropped, not abort the backup — and it must be
// recorded, because "matches nothing" on an include list is how a green backup
// ends up with no save in it.
func TestNewBackupFilterDropsInvalidPatterns(t *testing.T) {
	f := newBackupFilter([]string{"saves/**", "bad[pattern"}, nil)
	if f == nil {
		t.Fatal("filter is nil; the valid include should have survived")
	}
	if len(f.include) != 1 || f.include[0] != "saves/**" {
		t.Fatalf("include = %v, want [saves/**]", f.include)
	}
	if len(f.dropped) != 1 || f.dropped[0] != "bad[pattern" {
		t.Fatalf("dropped = %v, want [bad[pattern]", f.dropped)
	}
}

func TestBackupFilterKeepFile(t *testing.T) {
	cases := []struct {
		name             string
		include, exclude []string
		path             string
		want             bool
	}{
		// Include absent ⇒ everything, so the excludes carry the whole policy.
		{"no include keeps", nil, []string{"**/*.log"}, "savegame/world.db", true},
		{"no include excludes", nil, []string{"**/*.log"}, "logs/latest.log", false},
		{"exclude at root", nil, []string{"**/*.log"}, "latest.log", false},
		// Include present ⇒ only what it selects.
		{"include selects", []string{"savegame/**"}, nil, "savegame/world.db", true},
		{"include rejects", []string{"savegame/**"}, nil, "steamapps/appmanifest.acf", false},
		{"include nested deep", []string{"Pal/Saved/**"}, nil, "Pal/Saved/SaveGames/0/Level.sav", true},
		{"include exact file", []string{"enshrouded_server.json"}, nil, "enshrouded_server.json", true},
		{"include exact file is not a prefix", []string{"enshrouded_server.json"}, nil, "enshrouded_server.json.bak", false},
		// Exclude wins on a conflict — it is applied to what include selected.
		{"exclude beats include", []string{"Pal/Saved/**"}, []string{"Pal/Saved/Logs/**"}, "Pal/Saved/Logs/Pal.log", false},
		{"exclude misses", []string{"Pal/Saved/**"}, []string{"Pal/Saved/Logs/**"}, "Pal/Saved/SaveGames/x.sav", true},
		// Single-star stays within one path segment.
		{"star is one segment", []string{"*.json"}, nil, "server-settings.json", true},
		{"star does not cross a slash", []string{"*.json"}, nil, "config/server.json", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := newBackupFilter(c.include, c.exclude).keepFile(c.path); got != c.want {
				t.Fatalf("keepFile(%q) = %v, want %v", c.path, got, c.want)
			}
		})
	}
}

// pruneDir is the load-bearing half: skipping a 30 GB steamapps/ instead of
// walking it, WITHOUT skipping a parent that merely contains the save tree.
func TestBackupFilterPruneDir(t *testing.T) {
	cases := []struct {
		name             string
		include, exclude []string
		dir              string
		want             bool
	}{
		// Exclude-driven pruning: `**` spans zero segments, so "logs/**"
		// matches the directory "logs" itself.
		{"exclude prunes its dir", nil, []string{"logs/**"}, "logs", true},
		{"exclude prunes nested dir", nil, []string{"**/Logs/**"}, "Pal/Saved/Logs", true},
		{"exclude leaves siblings", nil, []string{"logs/**"}, "savegame", false},
		{"file-shaped exclude never prunes", nil, []string{"**/*.log"}, "savegame", false},
		// Include-driven pruning must DESCEND through the intermediate dirs of
		// its own pattern, and only there.
		{"descends the project dir", []string{"RSDragonwilds/Saved/**"}, nil, "RSDragonwilds", false},
		{"descends the saved dir", []string{"RSDragonwilds/Saved/**"}, nil, "RSDragonwilds/Saved", false},
		{"descends below the saved dir", []string{"RSDragonwilds/Saved/**"}, nil, "RSDragonwilds/Saved/SaveGames/0", false},
		{"prunes a sibling of saved", []string{"RSDragonwilds/Saved/**"}, nil, "RSDragonwilds/Binaries", true},
		{"prunes the install tree", []string{"RSDragonwilds/Saved/**"}, nil, "steamapps", true},
		// A leading `**` constrains nothing, so nothing can be pruned by it.
		{"leading doublestar descends all", []string{"**/SaveGames/**"}, nil, "a/b/c", false},
		// A mid-pattern `**` likewise unconstrains everything below its prefix.
		{"mid doublestar descends its subtree", []string{"a/**/c/**"}, nil, "a/b/x", false},
		{"mid doublestar prunes outside its prefix", []string{"a/**/c/**"}, nil, "b", true},
		// A fixed-depth include can only match at its own depth.
		{"fixed depth descends its parent", []string{"a/b/c.sav"}, nil, "a/b", false},
		{"fixed depth prunes at its depth", []string{"a/b/c.sav"}, nil, "a/b/c.sav", true},
		{"fixed depth root file prunes every dir", []string{"world.db"}, nil, "savegame", true},
		// Several includes: any one that could match keeps the walk going.
		{"any include wins", []string{"steamapps/x", "save/**"}, nil, "save", false},
		// An exclude beats an include that would otherwise descend.
		{"exclude beats a descending include", []string{"Pal/Saved/**"}, []string{"Pal/Saved/Logs/**"}, "Pal/Saved/Logs", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := newBackupFilter(c.include, c.exclude).pruneDir(c.dir); got != c.want {
				t.Fatalf("pruneDir(%q) = %v, want %v", c.dir, got, c.want)
			}
		})
	}
}

// The built-in exclude list the Panel sends for a spec with no backup block has
// to behave the same way on both path spellings the images produce.
func TestBackupFilterBuiltinShapedPatterns(t *testing.T) {
	f := newBackupFilter(nil, []string{
		"steamapps/downloading/**", "**/logs/**", "**/Logs/**", "**/*.log", "**/Crashes/**",
	})
	for _, p := range []string{
		"logs/latest.log",
		"Pal/Saved/Logs/Pal.log",
		"Pal/Saved/Crashes/UECC-x/report.txt",
		"PalServer.log",
		"steamapps/downloading/2394010/chunk",
	} {
		if f.keepFile(p) {
			t.Errorf("keepFile(%q) = true, want excluded", p)
		}
	}
	for _, p := range []string{
		"Pal/Saved/SaveGames/0/Level.sav",
		"savegame/world.db",
		"steamapps/appmanifest_2394010.acf",
	} {
		if !f.keepFile(p) {
			t.Errorf("keepFile(%q) = false, want captured", p)
		}
	}
	// The whole point: these prune rather than walk-and-filter.
	for _, d := range []string{"logs", "Pal/Saved/Logs", "steamapps/downloading"} {
		if !f.pruneDir(d) {
			t.Errorf("pruneDir(%q) = false, want pruned", d)
		}
	}
	// ...and the save tree's own parents are still descended.
	for _, d := range []string{"Pal", "Pal/Saved", "Pal/Saved/SaveGames", "steamapps"} {
		if f.pruneDir(d) {
			t.Errorf("pruneDir(%q) = true, want descended", d)
		}
	}
}
