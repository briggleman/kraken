package agent

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// plantTree writes each rel→content pair under a fresh temp root.
func plantTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	layTree(t, root, files)
	return root
}

// layTree writes each rel→content pair under an existing root.
func layTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		fp := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(fp), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fp, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// capturedFiles round-trips an archive and returns just the file entries (tar
// directory entries end in "/"), sorted.
func capturedFiles(t *testing.T, archive []byte) []string {
	t.Helper()
	var out []string
	for name := range tarRoundTrip(t, archive) {
		if !hasSuffixSlash(name) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func hasSuffixSlash(s string) bool { return len(s) > 0 && s[len(s)-1] == '/' }

func archiveWith(t *testing.T, root string, include, exclude []string) (archiveStats, []string) {
	t.Helper()
	var buf bytes.Buffer
	st, err := archiveTreeFiltered(root, &buf, newBackupFilter(include, exclude))
	if err != nil {
		t.Fatalf("archiveTreeFiltered: %v", err)
	}
	return st, capturedFiles(t, buf.Bytes())
}

func wantFiles(t *testing.T, got, want []string) {
	t.Helper()
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("captured %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("captured %v, want %v", got, want)
		}
	}
}

// A tree shaped like a real Steam data dir: a save tree, the install tree, and
// the logs that grow mid-archive.
func steamShapedTree(t *testing.T) string {
	return plantTree(t, map[string]string{
		"Pal/Saved/SaveGames/0/Level.sav":                   "world",
		"Pal/Saved/Config/LinuxServer/PalWorldSettings.ini": "cfg",
		"Pal/Saved/Logs/Pal.log":                            "noisy",
		"Pal/Binaries/Linux/PalServer-Linux-Shipping":       "elf",
		"steamapps/appmanifest_2394010.acf":                 "manifest",
		"steamapps/downloading/2394010/chunk":               "partial",
		"PalServer.sh":                                      "launcher",
	})
}

// Include-only: exactly the include list is captured, and everything the walk
// could not reach through it is either filtered or pruned away.
func TestArchiveFilterIncludeOnly(t *testing.T) {
	root := steamShapedTree(t)
	st, got := archiveWith(t, root, []string{"Pal/Saved/**"}, nil)
	wantFiles(t, got, []string{
		"Pal/Saved/SaveGames/0/Level.sav",
		"Pal/Saved/Config/LinuxServer/PalWorldSettings.ini",
		"Pal/Saved/Logs/Pal.log",
	})
	if st.files != 3 {
		t.Errorf("files = %d, want 3", st.files)
	}
	// PalServer.sh is the only unreachable FILE the walk still visits — the
	// install subtrees are pruned before their contents are ever read.
	if st.filtered != 1 {
		t.Errorf("filtered = %d, want 1 (PalServer.sh)", st.filtered)
	}
	if st.prunedDirs != 2 { // Pal/Binaries and steamapps
		t.Errorf("prunedDirs = %d, want 2", st.prunedDirs)
	}
	// Filtering is intentional, so it must not read as a degraded capture.
	if !st.clean() || st.summary() != "" {
		t.Errorf("filtering reported as degradation: %+v (%q)", st, st.summary())
	}
}

// Exclude-only: the whole tree minus the excluded paths — the shape of the
// Panel's built-in fallback policy.
func TestArchiveFilterExcludeOnly(t *testing.T) {
	root := steamShapedTree(t)
	st, got := archiveWith(t, root, nil, []string{"**/Logs/**", "**/*.log", "steamapps/downloading/**"})
	wantFiles(t, got, []string{
		"Pal/Saved/SaveGames/0/Level.sav",
		"Pal/Saved/Config/LinuxServer/PalWorldSettings.ini",
		"Pal/Binaries/Linux/PalServer-Linux-Shipping",
		"steamapps/appmanifest_2394010.acf",
		"PalServer.sh",
	})
	if st.prunedDirs != 2 { // Pal/Saved/Logs and steamapps/downloading
		t.Errorf("prunedDirs = %d, want 2", st.prunedDirs)
	}
	if st.filtered != 0 {
		t.Errorf("filtered = %d, want 0 — both excluded subtrees should be pruned, not walked", st.filtered)
	}
}

// Both lists: the include narrows, the exclude then wins on the overlap. This is
// the palworld/dragonwilds spec shape.
func TestArchiveFilterIncludeAndExclude(t *testing.T) {
	root := steamShapedTree(t)
	_, got := archiveWith(t, root, []string{"Pal/Saved/**"}, []string{"Pal/Saved/Logs/**"})
	wantFiles(t, got, []string{
		"Pal/Saved/SaveGames/0/Level.sav",
		"Pal/Saved/Config/LinuxServer/PalWorldSettings.ini",
	})
}

// Pruning has to actually prune, not walk-and-filter: a 30 GB steamapps/ must
// never be read. Proven without hooking the walk — a file planted two levels
// inside the excluded dir would show up in `filtered` if the walk had descended,
// and the deeper directory would not be counted as a second pruned dir.
func TestArchiveFilterPrunesInsteadOfWalking(t *testing.T) {
	root := plantTree(t, map[string]string{
		"savegame/world.db":      "world",
		"logs/nested/deep/a.log": "noise",
		"logs/nested/deep/b.log": "noise",
		"logs/top.log":           "noise",
	})
	st, got := archiveWith(t, root, nil, []string{"logs/**"})
	wantFiles(t, got, []string{"savegame/world.db"})
	if st.prunedDirs != 1 {
		t.Errorf("prunedDirs = %d, want 1 — logs/ must be skipped whole, not descended", st.prunedDirs)
	}
	if st.filtered != 0 {
		t.Errorf("filtered = %d, want 0 — a nonzero count means the walk read inside logs/", st.filtered)
	}
	// The pruned subtree leaves no directory entries behind either.
	for name := range tarRoundTrip(t, mustArchive(t, root, nil, []string{"logs/**"})) {
		if name == "logs/" || name == "logs/nested/" {
			t.Errorf("pruned dir %q still in the archive", name)
		}
	}
}

func mustArchive(t *testing.T, root string, include, exclude []string) []byte {
	t.Helper()
	var buf bytes.Buffer
	if _, err := archiveTreeFiltered(root, &buf, newBackupFilter(include, exclude)); err != nil {
		t.Fatalf("archiveTreeFiltered: %v", err)
	}
	return buf.Bytes()
}

// The include-descend problem: an include whose saves are nested must not prune
// the intermediate directories on its own path, while still pruning their
// siblings. Both halves in one tree.
func TestArchiveFilterDescendsToANestedInclude(t *testing.T) {
	root := plantTree(t, map[string]string{
		"RSDragonwilds/Saved/SaveGames/world.sav":              "world",
		"RSDragonwilds/Saved/Config/LinuxServer/Dedicated.ini": "cfg",
		"RSDragonwilds/Binaries/Linux/Server":                  "elf",
		"RSDragonwilds/Content/Paks/pakchunk0.pak":             "assets",
		"steamapps/appmanifest.acf":                            "manifest",
	})
	st, got := archiveWith(t, root, []string{"RSDragonwilds/Saved/**"}, nil)
	wantFiles(t, got, []string{
		"RSDragonwilds/Saved/SaveGames/world.sav",
		"RSDragonwilds/Saved/Config/LinuxServer/Dedicated.ini",
	})
	// Binaries, Content and steamapps pruned; RSDragonwilds and its Saved tree
	// descended even though neither directory matches the pattern itself.
	if st.prunedDirs != 3 {
		t.Errorf("prunedDirs = %d, want 3", st.prunedDirs)
	}
	if st.filtered != 0 {
		t.Errorf("filtered = %d, want 0", st.filtered)
	}
}

// Built-in-style `**` patterns behave the same at the root and nested, so the
// Panel's one list covers both spellings the images produce.
func TestArchiveFilterDoublestarPatterns(t *testing.T) {
	root := plantTree(t, map[string]string{
		"crash.dmp":                     "dump",
		"latest.log":                    "log",
		"savegame/world.db":             "world",
		"savegame/notes.log":            "log",
		"AbioticFactor/Saved/Crashes/x": "dump",
		"AbioticFactor/Saved/world.sav": "world",
	})
	_, got := archiveWith(t, root, nil, []string{"**/*.log", "**/*.dmp", "**/Crashes/**"})
	wantFiles(t, got, []string{
		"savegame/world.db",
		"AbioticFactor/Saved/world.sav",
	})
}

// Empty glob lists must reproduce the historical whole-dir backup exactly — the
// compatibility guarantee that lets every pre-#218 caller keep working.
func TestArchiveFilterEmptyGlobsCaptureEverything(t *testing.T) {
	root := steamShapedTree(t)
	stFiltered, gotFiltered := archiveWith(t, root, nil, nil)

	var buf bytes.Buffer
	stPlain, err := archiveTree(root, &buf)
	if err != nil {
		t.Fatalf("archiveTree: %v", err)
	}
	wantFiles(t, gotFiltered, capturedFiles(t, buf.Bytes()))
	if stFiltered.files != stPlain.files || stFiltered.files != 7 {
		t.Fatalf("files: filtered=%d plain=%d, want 7", stFiltered.files, stPlain.files)
	}
	if stFiltered.filtered != 0 || stFiltered.prunedDirs != 0 {
		t.Fatalf("empty globs filtered something: %+v", stFiltered)
	}
}

// An include list that matches nothing must FAIL the backup rather than store an
// empty archive: a green backup with no save in it is the worst outcome here.
func TestArchiveDataDirFailsWhenGlobsMatchNothing(t *testing.T) {
	d := newFileOpsRuntime(t)
	const sid = "globs-miss"
	if err := d.Create(t.Context(), mkSpec(sid)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := d.WriteFile(t.Context(), sid, "savegame/world.db", []byte("world")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	var buf bytes.Buffer
	_, err := d.archiveDataDir(sid, &buf, newBackupFilter([]string{"wrong-dir/**"}, nil))
	if err == nil {
		t.Fatal("archiveDataDir succeeded with an include list that matches nothing")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("matched no files")) {
		t.Errorf("error should name the glob miss, got %v", err)
	}
}
