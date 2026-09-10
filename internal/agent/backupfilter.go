package agent

import (
	"log/slog"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// A backup captures the game's SAVE DATA, not the reinstallable install tree
// (#218). The Panel resolves that policy — the spec's `backup:` block, or the
// Panel's built-in ephemeral-exclude list when a spec declares none — into two
// already-expanded doublestar glob lists and ships them with CreateBackup, so
// the Agent never needs the spec. Both lists empty reproduces the historical
// whole-data-dir tar, which is what keeps every pre-#218 caller working.

// backupFilter decides which data-dir-relative POSIX paths an archive captures.
// A nil *backupFilter captures everything, so callers can pass one through
// unconditionally.
type backupFilter struct {
	include []string
	exclude []string
	// dropped names patterns that failed to compile. The Panel validates globs
	// at spec-save time, so this should be unreachable — but a bad pattern must
	// never abort a backup, and silently honoring "matches nothing" on an
	// include list would produce a green archive with no saves in it.
	dropped []string
}

// newBackupFilter compiles the two lists, dropping (and logging once) any
// pattern doublestar rejects. Returns nil when there is nothing to filter, so
// the walk pays no per-entry cost on the whole-dir path.
func newBackupFilter(include, exclude []string) *backupFilter {
	f := &backupFilter{}
	keep := func(pats []string) []string {
		out := make([]string, 0, len(pats))
		for _, p := range pats {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			if !doublestar.ValidatePattern(p) {
				f.dropped = append(f.dropped, p)
				continue
			}
			out = append(out, p)
		}
		return out
	}
	f.include = keep(include)
	f.exclude = keep(exclude)
	if len(f.dropped) > 0 {
		slog.Warn("backup: ignoring invalid glob pattern(s)", "patterns", f.dropped)
	}
	if len(f.include) == 0 && len(f.exclude) == 0 {
		return nil
	}
	return f
}

// keepFile reports whether the file at the data-dir-relative path name is
// captured: the include list selects (absent ⇒ everything), the exclude list
// then filters, so an exclude wins on a conflict.
func (f *backupFilter) keepFile(name string) bool {
	if f == nil {
		return true
	}
	if len(f.include) > 0 && !matchAnyGlob(f.include, name) {
		return false
	}
	return !matchAnyGlob(f.exclude, name)
}

// pruneDir reports whether the walk can skip the directory at name outright —
// the difference between skipping a 30 GB steamapps/ and walking every file in
// it only to filter them one at a time.
func (f *backupFilter) pruneDir(name string) bool {
	if f == nil {
		return false
	}
	// An exclude that matches the directory itself excludes everything beneath
	// it: doublestar's `**` spans zero segments, so "logs/**" matches "logs".
	if matchAnyGlob(f.exclude, name) {
		return true
	}
	if len(f.include) == 0 {
		return false
	}
	// Include pruning must still DESCEND through a directory that doesn't itself
	// match — "RSDragonwilds/Saved/**" captures nothing named "RSDragonwilds",
	// yet everything it captures lives underneath it. Prune only when no include
	// pattern could match anything below.
	for _, p := range f.include {
		if globCouldMatchUnder(p, name) {
			return false
		}
	}
	return true
}

func matchAnyGlob(pats []string, name string) bool {
	for _, p := range pats {
		// Patterns are pre-validated, so the only error Match can return is
		// ErrBadPattern; treating that as "no match" is the safe reading.
		if ok, err := doublestar.Match(p, name); ok && err == nil {
			return true
		}
	}
	return false
}

// globCouldMatchUnder reports whether pattern could match any path strictly
// beneath the directory dir (both data-dir-relative POSIX paths). It is a
// deliberately conservative test — a false positive only costs a wasted descent,
// while a false negative would prune away saves.
func globCouldMatchUnder(pattern, dir string) bool {
	pseg := strings.Split(pattern, "/")
	dseg := strings.Split(dir, "/")
	for i, s := range pseg {
		if s != "**" {
			continue
		}
		// From the first `**` on, the pattern constrains nothing: `**` absorbs
		// any number of segments, so anything under a dir whose path matches the
		// fixed prefix may still match. Compare only that prefix.
		if i == 0 {
			return true
		}
		if i >= len(dseg) {
			return matchGlob(strings.Join(pseg[:len(dseg)], "/"), dir)
		}
		return matchGlob(strings.Join(append(pseg[:i:i], "**"), "/"), dir)
	}
	// No `**`: the pattern matches at one fixed depth, so it can only match
	// below dir if it is deeper than dir and its leading segments match dir.
	if len(pseg) <= len(dseg) {
		return false
	}
	return matchGlob(strings.Join(pseg[:len(dseg)], "/"), dir)
}

func matchGlob(pattern, name string) bool {
	ok, err := doublestar.Match(pattern, name)
	return ok && err == nil
}
