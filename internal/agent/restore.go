package agent

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Restore reproduces the archived state for everything the archive covers and
// leaves the rest of the data dir alone.
//
// Since #218 an archive is a scoped save-set (per-spec globs), which makes
// "extract over the live tree" wrong twice over: a file the game wrote after
// the backup survives inside a directory the archive fully describes (a rolling
// save slot newer than the restored one then wins on load, so the restore is
// silently ignored), and an extraction that aborts partway leaves a hybrid save
// directory behind.
//
// The mechanism is stage-then-swap: extract the whole archive into a scratch
// directory inside the server's own data dir — same volume as the live tree, so
// every swap is a rename rather than a copy, and the staged paths stay inside
// the jail withinHostDir enforces — and only then move the archived copies into
// place, keeping each displaced original aside until the whole set has landed.
// Extraction completes before the first swap, so the window in which the live
// tree is half-restored is renames only.
//
// The swap unit is deliberately NOT the archive's top-level entry. A scoped
// archive can describe one subtree of a top-level directory that also holds the
// install: palworld backs up `Pal/Saved/**`, and `Pal/` is where
// `Pal/Binaries/<os>/PalServer` lives, so replacing top-level `Pal` wholesale
// would delete the game. A directory counts as covered — and so is replaced
// wholesale, strays and all — only when the archive holds files DIRECTLY in it;
// a directory that appears only as an ancestor of a covered one is merged
// (created when missing, never replaced). A pre-#218 whole-tree archive still
// degenerates to a full replace, because in one of those every directory
// holding files is covered.

const (
	// restoreScratchPrefix names the staging directory a restore extracts into;
	// asideMarker suffixes the displaced originals it holds until the swap
	// completes. Both live inside the server's data dir, so the backup walk has
	// to know to skip them (see archiveTreeFiltered).
	restoreScratchPrefix = ".kraken-restore-"
	asideMarker          = ".kraken-aside-"
)

// isRestoreScratch reports whether a directory-entry name belongs to an
// in-flight restore.
func isRestoreScratch(name string) bool {
	return strings.HasPrefix(name, restoreScratchPrefix) || strings.Contains(name, asideMarker)
}

// restoreRename is os.Rename behind a seam, so a test can force a failure
// partway through the swap and assert the rollback puts the originals back.
var restoreRename = os.Rename

// restoreEntryPath validates one tar entry name and returns it as a relative
// slash path — "" for the archive root itself ("." or "./", which some tar
// writers emit and which carries nothing to write).
//
// The traversal check is per SEGMENT, not a substring scan: `world..bak` and
// `v1..2.cfg` are legal save names, and only a literal `..` segment is
// traversal. Absolute and volume-qualified names are rejected on every OS, not
// just the one where the separator or drive letter happens to be meaningful.
func restoreEntryPath(name string) (string, error) {
	if strings.Contains(name, `\`) {
		// Our archiver always emits slash-separated names, so a backslash here
		// comes from a foreign archive where it is either a separator (Windows)
		// or a legal filename character (Linux) — ambiguous enough to refuse.
		return "", fmt.Errorf("entry %q contains a backslash", name)
	}
	if strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("entry %q is an absolute path", name)
	}
	if len(name) >= 2 && name[1] == ':' && isDriveLetter(name[0]) {
		return "", fmt.Errorf("entry %q is volume-qualified", name)
	}
	segs := strings.Split(name, "/")
	out := make([]string, 0, len(segs))
	for _, seg := range segs {
		switch seg {
		case "", ".":
			continue // "./x" and "a//b" are sloppy, not dangerous, once collapsed
		case "..":
			return "", fmt.Errorf("entry %q has a %q path segment", name, "..")
		}
		out = append(out, seg)
	}
	if len(out) == 0 {
		return "", nil
	}
	return path.Join(out...), nil
}

func isDriveLetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// parentOf is the directory part of a relative slash path, "" for a path at the
// data root (path.Dir would answer ".", which is not a name we can join).
func parentOf(rel string) string {
	if i := strings.LastIndex(rel, "/"); i >= 0 {
		return rel[:i]
	}
	return ""
}

// stagedArchive is what extractArchive learned while staging: which directories
// the archive carries (with the modes to create them with), which of them hold
// files directly, and how many entries had to be skipped.
type stagedArchive struct {
	dirs      []string               // archived directories, in archive order (shallowest first)
	dirModes  map[string]os.FileMode // dir → mode to create it with
	fileDirs  map[string]bool        // directories holding archived files directly ("" = the data root)
	rootFiles []string               // archived files at the data root
	links     int                    // symlink/hardlink entries skipped
	irregular int                    // devices, fifos, sockets, pax headers skipped
}

// filePerm/dirPerm preserve the archived mode. valheim/vrising save-sets now
// deliberately include `BepInEx/**`, and a restore that drops the exec bit
// leaves a mod loader that cannot run. A header carrying no mode at all (some
// writers emit 0) falls back to the conventional defaults.
func filePerm(hdr *tar.Header) os.FileMode {
	if p := hdr.FileInfo().Mode().Perm(); p != 0 {
		return p
	}
	return 0o644
}

func dirPerm(hdr *tar.Header) os.FileMode {
	if p := hdr.FileInfo().Mode().Perm(); p != 0 {
		return p
	}
	return 0o755
}

// extractArchive writes every entry of tr into the staging dir.
func (d *DockerRuntime) extractArchive(tr *tar.Reader, serverID, staged string) (*stagedArchive, error) {
	st := &stagedArchive{dirModes: map[string]os.FileMode{}, fileDirs: map[string]bool{}}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("docker: read backup: %w", err)
		}
		// Validate the name BEFORE joining it into a filesystem path — protects
		// against Zip Slip even if the withinHostDir() check below ever
		// regresses. That check is the real second line of defense; this one is
		// the CodeQL-visible first line.
		rel, perr := restoreEntryPath(hdr.Name)
		if perr != nil {
			return nil, fmt.Errorf("docker: backup %w", perr)
		}
		if rel == "" {
			continue
		}
		dest := filepath.Join(staged, filepath.FromSlash(rel))
		if !d.withinHostDir(serverID, dest) {
			return nil, fmt.Errorf("docker: backup entry %q escapes data dir", hdr.Name)
		}
		switch {
		case hdr.Typeflag == tar.TypeSymlink || hdr.Typeflag == tar.TypeLink:
			// Our archiver emits neither (see archiveTree's symlink tally), so
			// these only arrive in a foreign archive. Materializing them as
			// empty files — what this used to do — is a silent corruption, and a
			// Windows junction's absolute target means nothing on a restore host.
			st.links++
		case hdr.Typeflag == tar.TypeDir:
			mode := dirPerm(hdr)
			if err := os.MkdirAll(dest, mode); err != nil {
				return nil, err
			}
			st.dirs = append(st.dirs, rel)
			st.dirModes[rel] = mode
		case hdr.Typeflag == tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				return nil, err
			}
			f, oerr := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, filePerm(hdr))
			if oerr != nil {
				return nil, oerr
			}
			if _, cerr := io.Copy(f, tr); cerr != nil {
				f.Close()
				return nil, fmt.Errorf("docker: restore %s: %w", rel, cerr)
			}
			if cerr := f.Close(); cerr != nil {
				return nil, cerr
			}
			parent := parentOf(rel)
			st.fileDirs[parent] = true
			if parent == "" {
				st.rootFiles = append(st.rootFiles, rel)
			}
		default:
			st.irregular++
		}
	}
	return st, nil
}

// swapUnits is the set of paths the restore replaces wholesale, in a stable
// order: every directory the archive holds files directly in (minus any nested
// inside another such directory — the outer swap carries it), plus the archived
// files at the data root, which have no directory to travel inside.
func (st *stagedArchive) swapUnits() []string {
	units := make([]string, 0, len(st.fileDirs)+len(st.rootFiles))
	for dir := range st.fileDirs {
		if dir == "" || st.coveredAncestor(dir) {
			continue
		}
		units = append(units, dir)
	}
	units = append(units, st.rootFiles...)
	sort.Strings(units)
	return units
}

// coveredAncestor reports whether a shallower directory of dir also holds
// archived files — that one's swap already carries dir inside it.
func (st *stagedArchive) coveredAncestor(dir string) bool {
	for {
		i := strings.LastIndex(dir, "/")
		if i < 0 {
			return false
		}
		dir = dir[:i]
		if st.fileDirs[dir] {
			return true
		}
	}
}

// insideAny reports whether rel is one of units or lives inside one.
func insideAny(rel string, units []string) bool {
	for _, u := range units {
		if rel == u || strings.HasPrefix(rel, u+"/") {
			return true
		}
	}
	return false
}

// swapped records one completed swap, so a later failure can be unwound.
type swapped struct {
	live  string
	aside string // "" when nothing occupied the live path
}

// applyRestore moves the staged copies into the live tree.
func (d *DockerRuntime) applyRestore(serverID, root, staged string, st *stagedArchive) error {
	units := st.swapUnits()

	// The archive's directories that no swap unit covers — the ancestors of the
	// covered ones (palworld's `Pal` and `Pal/Saved`) and any directory the
	// archive holds no files in. Additive only: nothing here is removed or
	// replaced, so it needs no rollback, and a leftover empty directory after a
	// failed restore is harmless.
	for _, dir := range st.dirs {
		if insideAny(dir, units) {
			continue
		}
		live := filepath.Join(root, filepath.FromSlash(dir))
		if !d.withinHostDir(serverID, live) {
			return fmt.Errorf("docker: restore entry %q escapes data dir", dir)
		}
		if err := os.MkdirAll(live, st.dirModes[dir]); err != nil {
			return err
		}
	}

	// One token per restore, shared by every aside: it makes the displaced
	// originals recognizable as one operation's leftovers if a crash lands
	// between the swap and the cleanup.
	token := strings.TrimPrefix(filepath.Base(staged), restoreScratchPrefix)
	var done []swapped
	unwind := func() {
		// Reverse order, so the tree comes back the way it was.
		for i := len(done) - 1; i >= 0; i-- {
			m := done[i]
			// The restored copy occupies the live path: clear it first, because
			// a rename never overwrites an existing destination on Windows.
			if err := os.RemoveAll(m.live); err != nil {
				slog.Error("restore rollback could not clear the restored path", "path", m.live, "err", err)
				continue
			}
			if m.aside == "" {
				continue // nothing was there before; removing it IS the rollback
			}
			if err := restoreRename(m.aside, m.live); err != nil {
				slog.Error("restore rollback could not put the original back; it is preserved beside it",
					"path", m.live, "aside", m.aside, "err", err)
			}
		}
	}

	for _, unit := range units {
		live := filepath.Join(root, filepath.FromSlash(unit))
		if !d.withinHostDir(serverID, live) {
			unwind()
			return fmt.Errorf("docker: restore entry %q escapes data dir", unit)
		}
		// A foreign archive can carry no directory entries at all, in which case
		// the merge phase above created nothing and the unit's parent chain may
		// be missing. Creating it is additive, like the merge.
		if err := os.MkdirAll(filepath.Dir(live), 0o755); err != nil {
			unwind()
			return fmt.Errorf("docker: restore stopped at %q: %w", unit, err)
		}
		aside := ""
		if _, err := os.Lstat(live); err == nil {
			// Moving the original aside is also what clears the destination: a
			// rename onto an existing path never clobbers on Windows.
			aside = live + asideMarker + token
			if rerr := restoreRename(live, aside); rerr != nil {
				unwind()
				return fmt.Errorf("docker: restore stopped at %q moving the existing copy aside: %w", unit, rerr)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			unwind()
			return fmt.Errorf("docker: restore stopped at %q: %w", unit, err)
		}
		if rerr := restoreRename(filepath.Join(staged, filepath.FromSlash(unit)), live); rerr != nil {
			if aside != "" {
				// This unit's own original goes back first; done holds the rest.
				if back := restoreRename(aside, live); back != nil {
					slog.Error("restore could not put the original back; it is preserved beside it",
						"path", live, "aside", aside, "err", back)
				}
			}
			unwind()
			return fmt.Errorf("docker: restore stopped at %q installing the restored copy: %w", unit, rerr)
		}
		done = append(done, swapped{live: live, aside: aside})
	}

	// Every unit landed: the displaced originals are now dead weight.
	for _, m := range done {
		if m.aside == "" {
			continue
		}
		if err := os.RemoveAll(m.aside); err != nil {
			slog.Warn("restore could not remove the displaced original", "path", m.aside, "err", err)
		}
	}
	return nil
}
