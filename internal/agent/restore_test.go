package agent

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// archiveEntry is one entry for buildArchive: a regular file (mode optional), a
// directory, or a link entry our archiver never emits but a foreign tarball can.
type archiveEntry struct {
	name string
	body string      // file content, or the link target for link entries
	mode os.FileMode // 0 → 0644 for files, 0755 for dirs
	typ  byte        // 0 → tar.TypeReg
}

func dirEntry(name string) archiveEntry {
	return archiveEntry{name: name + "/", typ: tar.TypeDir, mode: 0o755}
}

// buildArchive assembles a scoped save-set archive in memory, the shape
// CreateBackup writes: root-relative slash names, dir entries with a trailing
// slash.
func buildArchive(t *testing.T, entries ...archiveEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		typ := e.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		mode := e.mode
		if mode == 0 {
			mode = 0o644
		}
		hdr := &tar.Header{Name: e.name, Mode: int64(mode), Typeflag: typ}
		switch typ {
		case tar.TypeReg:
			hdr.Size = int64(len(e.body))
		case tar.TypeSymlink, tar.TypeLink:
			hdr.Linkname = e.body
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write header %s: %v", e.name, err)
		}
		if typ == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatalf("write body %s: %v", e.name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

// restoreFixture wires a file-ops runtime with a data dir laid out from live and
// an archive stored under the id it returns.
func restoreFixture(t *testing.T, sid string, live map[string]string, entries ...archiveEntry) (*DockerRuntime, string) {
	t.Helper()
	d := newFileOpsRuntime(t)
	if err := d.Create(context.Background(), mkSpec(sid)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	layTree(t, d.localDir(sid), live)
	archive := buildArchive(t, entries...)
	const id = "1700000000000__snap"
	if err := d.backups.Put(context.Background(), sid, id, bytes.NewReader(archive), int64(len(archive))); err != nil {
		t.Fatalf("Put archive: %v", err)
	}
	return d, id
}

func liveRead(t *testing.T, d *DockerRuntime, sid, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(d.localDir(sid), filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

func liveMissing(t *testing.T, d *DockerRuntime, sid, rel string) {
	t.Helper()
	if _, err := os.Lstat(filepath.Join(d.localDir(sid), filepath.FromSlash(rel))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s should be gone after the restore (err = %v)", rel, err)
	}
}

// noRestoreLeftovers asserts the staging dir and every displaced original were
// cleaned up — a restore that leaks them doubles the data dir on every run.
func noRestoreLeftovers(t *testing.T, d *DockerRuntime, sid string) {
	t.Helper()
	ents, err := os.ReadDir(d.localDir(sid))
	if err != nil {
		t.Fatalf("read data dir: %v", err)
	}
	for _, e := range ents {
		if isRestoreScratch(e.Name()) {
			t.Errorf("restore left %q behind in the data dir", e.Name())
		}
	}
}

// A scoped archive describes its directories completely: a save the game wrote
// after the backup (enshrouded's rolling slots) must NOT survive inside a
// restored save dir, or the game loads the newer stray and the restore is a
// no-op. Everything the archive doesn't cover — the install tree — survives.
func TestRestoreReplacesCoveredDirsAndKeepsTheRest(t *testing.T) {
	const sid = "s-scoped"
	d, id := restoreFixture(t, sid,
		map[string]string{
			"savegame/a.db":           "live-a",
			"savegame/stray-newer.db": "written after the backup",
			"cfg.json":                "live-cfg",
			"install-tree/big.bin":    "the install",
		},
		dirEntry("savegame"),
		archiveEntry{name: "savegame/a.db", body: "archived-a"},
		archiveEntry{name: "cfg.json", body: "archived-cfg"},
	)
	if err := d.RestoreBackup(context.Background(), sid, "", id); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	liveMissing(t, d, sid, "savegame/stray-newer.db")
	if v := liveRead(t, d, sid, "savegame/a.db"); v != "archived-a" {
		t.Errorf("savegame/a.db = %q, want archived-a", v)
	}
	if v := liveRead(t, d, sid, "cfg.json"); v != "archived-cfg" {
		t.Errorf("cfg.json = %q, want archived-cfg", v)
	}
	if v := liveRead(t, d, sid, "install-tree/big.bin"); v != "the install" {
		t.Errorf("install-tree/big.bin = %q; the restore must not touch what the archive doesn't cover", v)
	}
	noRestoreLeftovers(t, d, sid)
}

// palworld's save-set is `Pal/Saved/**` while `Pal/Binaries/…/PalServer` is the
// game itself, so the swap unit can't be the archive's top-level entry: the save
// dir is replaced (strays and all) and the sibling install inside the same
// top-level directory is left alone.
func TestRestoreKeepsAnInstallSharingTheTopLevelDir(t *testing.T) {
	const sid = "s-palworld"
	d, id := restoreFixture(t, sid,
		map[string]string{
			"Pal/Binaries/Linux/PalServer":    "the game binary",
			"Pal/Saved/SaveGames/0/Level.sav": "live-level",
			"Pal/Saved/SaveGames/0/stray.sav": "written after the backup",
			"Pal/Saved/Logs/server.log":       "logs",
		},
		dirEntry("Pal"), dirEntry("Pal/Saved"), dirEntry("Pal/Saved/SaveGames"), dirEntry("Pal/Saved/SaveGames/0"),
		archiveEntry{name: "Pal/Saved/SaveGames/0/Level.sav", body: "archived-level"},
	)
	if err := d.RestoreBackup(context.Background(), sid, "", id); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if v := liveRead(t, d, sid, "Pal/Binaries/Linux/PalServer"); v != "the game binary" {
		t.Errorf("the install under Pal/ was destroyed: PalServer = %q", v)
	}
	if v := liveRead(t, d, sid, "Pal/Saved/SaveGames/0/Level.sav"); v != "archived-level" {
		t.Errorf("Level.sav = %q, want archived-level", v)
	}
	liveMissing(t, d, sid, "Pal/Saved/SaveGames/0/stray.sav")
	// Pal/Saved is only an ancestor of the covered dir, so the excluded Logs
	// tree beside it is merged past, not deleted.
	if v := liveRead(t, d, sid, "Pal/Saved/Logs/server.log"); v != "logs" {
		t.Errorf("Pal/Saved/Logs/server.log = %q, want logs", v)
	}
}

// valheim/vrising save-sets include BepInEx/**, so a restore that drops the
// exec bit leaves a mod loader that cannot start.
func TestRestorePreservesFileModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not modelled on Windows")
	}
	const sid = "s-modes"
	d, id := restoreFixture(t, sid, nil,
		dirEntry("BepInEx"),
		archiveEntry{name: "BepInEx/run.sh", body: "#!/bin/sh\n", mode: 0o755},
		archiveEntry{name: "BepInEx/config.cfg", body: "k=v", mode: 0o600},
	)
	if err := d.RestoreBackup(context.Background(), sid, "", id); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	for rel, want := range map[string]os.FileMode{"BepInEx/run.sh": 0o755, "BepInEx/config.cfg": 0o600} {
		fi, err := os.Stat(filepath.Join(d.localDir(sid), filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("stat %s: %v", rel, err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("%s mode = %o, want %o", rel, got, want)
		}
	}
}

// The mode fallback is checked separately from the filesystem, so it is covered
// on Windows too (where the permission-bit assertions above cannot run).
func TestRestoreModeFallbacks(t *testing.T) {
	if got := filePerm(&tar.Header{Mode: 0o755, Typeflag: tar.TypeReg}); got != 0o755 {
		t.Errorf("filePerm(0755) = %o, want 755", got)
	}
	if got := filePerm(&tar.Header{Typeflag: tar.TypeReg}); got != 0o644 {
		t.Errorf("filePerm(no mode) = %o, want 644", got)
	}
	if got := dirPerm(&tar.Header{Mode: 0o700, Typeflag: tar.TypeDir}); got != 0o700 {
		t.Errorf("dirPerm(0700) = %o, want 700", got)
	}
	if got := dirPerm(&tar.Header{Typeflag: tar.TypeDir}); got != 0o755 {
		t.Errorf("dirPerm(no mode) = %o, want 755", got)
	}
}

// Link entries (a foreign archive only — ours emits none) are skipped, never
// materialized as the 0-byte files the old extraction produced.
func TestRestoreSkipsLinkEntries(t *testing.T) {
	const sid = "s-links"
	d, id := restoreFixture(t, sid, nil,
		dirEntry("savegame"),
		archiveEntry{name: "savegame/a.db", body: "archived-a"},
		archiveEntry{name: "savegame/link", body: "a.db", typ: tar.TypeSymlink},
		archiveEntry{name: "savegame/hard", body: "savegame/a.db", typ: tar.TypeLink},
	)
	if err := d.RestoreBackup(context.Background(), sid, "", id); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if v := liveRead(t, d, sid, "savegame/a.db"); v != "archived-a" {
		t.Errorf("savegame/a.db = %q, want archived-a", v)
	}
	liveMissing(t, d, sid, "savegame/link")
	liveMissing(t, d, sid, "savegame/hard")
}

// The traversal check is per path segment: doubled dots inside a NAME are how
// games version their saves, and rejecting them refuses a legitimate restore.
func TestRestoreEntryPath(t *testing.T) {
	cases := []struct {
		name string
		want string // "" plus wantErr=false means "the archive root, skip it"
		err  bool
	}{
		{name: "world..bak", want: "world..bak"},
		{name: "savegame/v1..2.cfg", want: "savegame/v1..2.cfg"},
		{name: "savegame/", want: "savegame"},
		{name: "./cfg.json", want: "cfg.json"},
		{name: "a//b", want: "a/b"},
		{name: ".", want: ""},
		{name: "./", want: ""},
		{name: "../escape", err: true},
		{name: "savegame/../../escape", err: true},
		{name: "savegame/..", err: true},
		{name: "/etc/passwd", err: true},
		{name: `C:\windows\system32`, err: true},
		{name: "C:/windows/system32", err: true},
		{name: `savegame\a.db`, err: true},
	}
	for _, tc := range cases {
		got, err := restoreEntryPath(tc.name)
		if tc.err {
			if err == nil {
				t.Errorf("restoreEntryPath(%q) = %q, want an error", tc.name, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("restoreEntryPath(%q): %v", tc.name, err)
			continue
		}
		if got != tc.want {
			t.Errorf("restoreEntryPath(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// A restore that dies partway must not leave a hybrid tree: the units already
// swapped go back to what they were. The failure is forced through the rename
// seam on the second unit ("savegame" — units are swapped in sorted order, so
// "cfg.json" lands first).
func TestRestoreRollsBackAMidSwapFailure(t *testing.T) {
	const sid = "s-rollback"
	d, id := restoreFixture(t, sid,
		map[string]string{
			"savegame/a.db":     "live-a",
			"savegame/stray.db": "live-stray",
			"cfg.json":          "live-cfg",
		},
		dirEntry("savegame"),
		archiveEntry{name: "savegame/a.db", body: "archived-a"},
		archiveEntry{name: "cfg.json", body: "archived-cfg"},
	)
	orig := restoreRename
	t.Cleanup(func() { restoreRename = orig })
	restoreRename = func(oldpath, newpath string) error {
		// Fail the savegame unit's move-aside; the rollback's own renames move
		// the aside copies back and must still work.
		if strings.HasSuffix(oldpath, string(os.PathSeparator)+"savegame") {
			return errors.New("forced sharing violation")
		}
		return orig(oldpath, newpath)
	}
	err := d.RestoreBackup(context.Background(), sid, "", id)
	if err == nil {
		t.Fatal("RestoreBackup should have failed at the savegame swap")
	}
	if !strings.Contains(err.Error(), "savegame") {
		t.Errorf("the error should name where the restore stopped, got %v", err)
	}
	if v := liveRead(t, d, sid, "cfg.json"); v != "live-cfg" {
		t.Errorf("cfg.json = %q; the already-swapped unit was not rolled back", v)
	}
	if v := liveRead(t, d, sid, "savegame/a.db"); v != "live-a" {
		t.Errorf("savegame/a.db = %q, want the untouched live copy", v)
	}
	if v := liveRead(t, d, sid, "savegame/stray.db"); v != "live-stray" {
		t.Errorf("savegame/stray.db = %q, want the untouched live copy", v)
	}
	noRestoreLeftovers(t, d, sid)
}

// A tarball written by something other than our archiver can carry no directory
// entries at all, so the swap has to create the parent chain itself.
func TestRestoreArchiveWithoutDirEntries(t *testing.T) {
	const sid = "s-nodirs"
	d, id := restoreFixture(t, sid, nil,
		archiveEntry{name: "Pal/Saved/SaveGames/0/Level.sav", body: "archived-level"},
	)
	if err := d.RestoreBackup(context.Background(), sid, "", id); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if v := liveRead(t, d, sid, "Pal/Saved/SaveGames/0/Level.sav"); v != "archived-level" {
		t.Errorf("Level.sav = %q, want archived-level", v)
	}
}

// A traversal entry aborts the restore during staging, before anything in the
// live tree has been touched.
func TestRestoreRejectsTraversalBeforeSwapping(t *testing.T) {
	const sid = "s-traversal"
	d, id := restoreFixture(t, sid,
		map[string]string{"cfg.json": "live-cfg"},
		archiveEntry{name: "cfg.json", body: "archived-cfg"},
		archiveEntry{name: "../escape.txt", body: "escaped"},
	)
	if err := d.RestoreBackup(context.Background(), sid, "", id); err == nil {
		t.Fatal("RestoreBackup should reject an archive with a traversal entry")
	}
	if v := liveRead(t, d, sid, "cfg.json"); v != "live-cfg" {
		t.Errorf("cfg.json = %q; a rejected archive must not swap anything in", v)
	}
	if _, err := os.Lstat(filepath.Join(filepath.Dir(d.localDir(sid)), "escape.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the traversal entry escaped the data dir (err = %v)", err)
	}
	noRestoreLeftovers(t, d, sid)
}

// A scheduled backup can fire while a restore is staging or swapping. The walk
// skips the scratch names whatever the globs say, so the archive never holds a
// second copy of the saves — or a half-swapped tree.
func TestArchiveSkipsRestoreScratch(t *testing.T) {
	root := plantTree(t, map[string]string{
		"savegame/a.db":                          "live-a",
		".kraken-restore-1234/savegame/a.db":     "staged",
		"savegame.kraken-aside-1234/a.db":        "displaced",
		"savegame/nested.kraken-aside-1234/x.db": "displaced deeper",
	})
	_, got := archiveWith(t, root, nil, nil)
	wantFiles(t, got, []string{"savegame/a.db"})
}
