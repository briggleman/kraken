package agent

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The data dir is archived while the server may be running, so the walk has to
// tolerate a live tree: files that grow or shrink between stat and copy, files
// the game holds locked, symlinks/junctions SteamCMD leaves behind, and the odd
// unreadable subdirectory. Every degradation is tallied rather than aborting
// the whole backup — except an unopenable regular file, which fails it: in a
// game data dir that file is overwhelmingly likely to be the live save, and a
// green backup missing the save is worse than a red one.

// smallFileMax is the size at (or below) which a file is read fully into memory
// before its tar header is written, making the captured copy atomic — saves and
// configs, the payload that matters, land un-torn. Larger files (the install
// tree) stream. A var so tests can lower it.
var smallFileMax int64 = 8 << 20

// maxSamplePaths caps the example paths carried in an archiveStats summary.
const maxSamplePaths = 8

// archiveStats tallies what a live-tree archive had to tolerate.
type archiveStats struct {
	files          int // regular files captured
	grew           int // grew mid-copy; the header-size prefix was captured
	truncated      int // shrank mid-copy; the tail was zero-filled
	symlinks       int // symlinks/junctions skipped (see #220 for restore support)
	irregular      int // sockets, fifos, unknown reparse tags skipped
	unreadableDirs int // subdirectories the walk could not read
	samples        []string
}

func (s *archiveStats) note(counter *int, path string) {
	*counter++
	if len(s.samples) < maxSamplePaths {
		s.samples = append(s.samples, path)
	}
}

func (s *archiveStats) clean() bool {
	return s.grew+s.truncated+s.symlinks+s.irregular+s.unreadableDirs == 0
}

// summary renders the degradations for the backup's BackupInfo.error field;
// empty when the capture was clean.
func (s *archiveStats) summary() string {
	if s.clean() {
		return ""
	}
	var parts []string
	add := func(n int, what string) {
		if n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, what))
		}
	}
	add(s.grew, "file(s) grew during capture (prefix archived)")
	add(s.truncated, "file(s) shrank during capture (tail zero-filled)")
	add(s.symlinks, "symlink(s) skipped")
	add(s.irregular, "irregular entr(ies) skipped")
	add(s.unreadableDirs, "unreadable dir(s) skipped")
	return "degraded capture: " + strings.Join(parts, ", ") + " — e.g. " + strings.Join(s.samples, ", ")
}

// archiveTree tar+gzips the tree rooted at root into w. Entry names are
// root-relative POSIX paths, so restore is a straight extraction.
func archiveTree(root string, w io.Writer) (archiveStats, error) {
	var st archiveStats
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	walkErr := filepath.WalkDir(root, func(fp string, e fs.DirEntry, werr error) error {
		if werr != nil {
			// An unreadable entry (usually a directory ReadDir failure): count
			// and move on — same tolerant model as dirSizeMB's walk.
			st.note(&st.unreadableDirs, fp)
			if e != nil && e.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		rel, rerr := filepath.Rel(root, fp)
		if rerr != nil || rel == "." {
			return rerr
		}
		name := filepath.ToSlash(rel)
		switch {
		case e.IsDir():
			info, ierr := e.Info()
			if ierr != nil {
				st.note(&st.unreadableDirs, name)
				return fs.SkipDir
			}
			hdr, herr := tar.FileInfoHeader(info, "")
			if herr != nil {
				st.note(&st.unreadableDirs, name)
				return fs.SkipDir
			}
			hdr.Name = name + "/"
			return tw.WriteHeader(hdr)
		case e.Type()&fs.ModeSymlink != 0:
			// Restore materializes symlink entries as empty files, and a Windows
			// junction's target is an absolute host path that means nothing on a
			// restore host — skipping is the honest capture until #220.
			st.note(&st.symlinks, name)
			return nil
		case !e.Type().IsRegular():
			// Includes every unrecognized Windows reparse tag (junctions, cloud
			// placeholders) — tar.FileInfoHeader would reject these anyway.
			st.note(&st.irregular, name)
			return nil
		}
		return writeFileEntry(tw, fp, name, &st)
	})
	if walkErr != nil {
		_ = tw.Close()
		_ = gz.Close()
		return st, walkErr
	}
	if err := tw.Close(); err != nil {
		_ = gz.Close()
		return st, err
	}
	// gz.Close writes the trailer — a full disk surfaces here, and swallowing it
	// would store a truncated .tar.gz that only fails months later on restore.
	return st, gz.Close()
}

// writeFileEntry captures one regular file. The header is built from the open
// handle's fstat (not the walk's readdir-cached info, which can be minutes
// stale on a large tree), shrinking the size-vs-content race to microseconds.
func writeFileEntry(tw *tar.Writer, fp, name string, st *archiveStats) error {
	f, err := openForBackup(fp)
	if err != nil {
		return fmt.Errorf("open %s: %w", name, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat %s: %w", name, err)
	}
	hdr, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return fmt.Errorf("header %s: %w", name, err)
	}
	hdr.Name = name
	if info.Size() <= smallFileMax {
		// Read first, then size the header from what was read: an atomic copy.
		buf, rerr := io.ReadAll(f)
		if rerr != nil {
			return fmt.Errorf("read %s: %w", name, rerr)
		}
		hdr.Size = int64(len(buf))
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if _, err := tw.Write(buf); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
		st.files++
		return nil
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	grew, truncated, werr := writeBody(tw, f, hdr.Size)
	if werr != nil {
		return fmt.Errorf("write %s: %w", name, werr)
	}
	if grew {
		st.note(&st.grew, name)
	}
	if truncated {
		st.note(&st.truncated, name)
	}
	st.files++
	return nil
}

// writeBody copies exactly size bytes of r into the current tar entry, whose
// header (and therefore size) is already committed. A reader that grew past
// size is cut off at size; one that shrank has its tail zero-filled so the
// entry stays valid — a short entry would poison the archive at Close (this is
// what GNU tar's "file shrank, padding with zeros" does).
func writeBody(tw *tar.Writer, r io.Reader, size int64) (grew, truncated bool, err error) {
	n, err := io.CopyN(tw, r, size)
	switch {
	case err == io.EOF:
		truncated = true
		if perr := writeZeros(tw, size-n); perr != nil {
			return false, true, perr
		}
	case err != nil:
		return false, false, err
	default:
		// Exactly size bytes copied — probe one byte to see whether it grew.
		var b [1]byte
		if m, _ := r.Read(b[:]); m > 0 {
			grew = true
		}
	}
	return grew, truncated, nil
}

var zeroBlock [32 * 1024]byte

func writeZeros(w io.Writer, n int64) error {
	for n > 0 {
		chunk := int64(len(zeroBlock))
		if n < chunk {
			chunk = n
		}
		m, err := w.Write(zeroBlock[:chunk])
		if err != nil {
			return err
		}
		n -= int64(m)
	}
	return nil
}

// openForBackup opens a file for archiving with brief retries: a locked file is
// usually the game holding it for the duration of one write.
func openForBackup(fp string) (*os.File, error) {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(250 * time.Millisecond)
		}
		var f *os.File
		if f, err = openShared(fp); err == nil {
			return f, nil
		}
	}
	return nil, err
}
