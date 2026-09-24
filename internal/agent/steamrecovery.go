package agent

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// A SteamCMD update that downloads fine can still fail at the very end. Steam
// replaces a file by writing the new copy beside it as `<name>~RF<hex>.TMP` and
// then renaming it over the original — and it deletes the original first. When
// that rename fails, the `.TMP` is left behind and the target is gone. Every
// later pass re-stages the same file and fails the same rename, so the failure
// repeats exactly (`state is 0x602 after update job`), and `validate` cannot
// repair it.
//
// That is what happened on dragonwilds-01 on 2026-09-15: the server binary
// itself was missing, beside a 195 MB `RSDragonwildsServer-Win64-Shipping.exe~RF1561a.TMP`,
// for three days (#349). The rename failed because a game container still held
// the data dir (#351, now refused up front — see datadirguard.go); this file is
// about noticing the wreckage and clearing it.
//
// After a failure from the `state is 0x… after update job` family, the Agent
// scans the data dir natively for staging files. A `.TMP` whose target is
// missing is an orphan of a failed commit: it is deleted — the target is already
// gone, so nothing recoverable is lost — and the pass runs once more, if the
// Panel's deadline leaves room for it. A `.TMP` whose target exists is an
// ordinary in-flight artifact and is reported, never touched. Whatever was
// found is named in the failure message, retry or not.
//
// Deliberately not touched: steamapps/appmanifest_*.acf. A stale manifest is a
// plausible other cause of 0x6, but deleting it forces a full re-verify of the
// whole tree, and this round does not make that call automatically.

// steamStateRE captures the hex state from SteamCMD's "state is 0x… after
// update job" line, including its own "state is is" typo.
var steamStateRE = regexp.MustCompile(`(?i)state is (?:is )?(0x[0-9a-f]+) after update job`)

// steamFailureRetryable reports whether a SteamCMD failure line is one a second
// pass can fix once the tree is cleaned up: the "state is 0x… after update job"
// family. The rest — No subscription, Not for anonymous, a failed branch
// password, an invalid platform, a full disk — fail identically on a retry, and
// a retry there only doubles the install time.
func steamFailureRetryable(line string) bool {
	return steamStateRE.MatchString(line)
}

// steamStateBits are Steam's EAppState flags, highest first — the order they are
// rendered in. The names are what an operator reads in last_error, so they say
// what the bit means rather than repeating the enum.
var steamStateBits = []struct {
	bit  uint32
	name string
}{
	{0x800000, "update stopping"},
	{0x400000, "committing"},
	{0x200000, "staging"},
	{0x100000, "downloading"},
	{0x80000, "preallocating"},
	{0x40000, "adding files"},
	{0x20000, "validating"},
	{0x10000, "reconfiguring"},
	{0x1000, "backup running"},
	{0x800, "uninstalling"},
	{0x400, "update started"},
	{0x200, "paused before commit"},
	{0x100, "update running"},
	{0x80, "files corrupt"},
	{0x40, "app running"},
	{0x20, "files missing"},
	{0x10, "locked"},
	{0x8, "encrypted"},
	{0x4, "fully installed"},
	{0x2, "update required"},
	{0x1, "uninstalled"},
}

// decodeSteamState renders an EAppState value in English, e.g. 0x602 →
// "update started, paused before commit, update required". Bits Steam does not
// document are rendered together as hex rather than dropped, so nothing the
// operator might need to search for disappears.
func decodeSteamState(s uint32) string {
	if s == 0 {
		return "invalid"
	}
	var parts []string
	rest := s
	for _, b := range steamStateBits {
		if s&b.bit != 0 {
			parts = append(parts, b.name)
			rest &^= b.bit
		}
	}
	if rest != 0 {
		parts = append(parts, fmt.Sprintf("unknown bits 0x%x", rest))
	}
	return strings.Join(parts, ", ")
}

// describeSteamFailure returns the SteamCMD failure line with its state decoded
// in place, e.g. "Error! App '4019830' state is 0x602 (update started, paused
// before commit, update required) after update job." A line without a state is
// returned unchanged.
func describeSteamFailure(line string) string {
	loc := steamStateRE.FindStringSubmatchIndex(line)
	if loc == nil {
		return line
	}
	hex := line[loc[2]:loc[3]]
	v, err := strconv.ParseUint(hex[2:], 16, 32)
	if err != nil {
		return line
	}
	return line[:loc[3]] + " (" + decodeSteamState(uint32(v)) + ")" + line[loc[3]:]
}

// stagingNameRE matches a SteamCMD staging file name. The prefix is greedy, so
// the suffix taken off is always the last `~RF<hex>.TMP` — a name that itself
// contains `~` (or even an earlier `~RF…`) keeps it in its target.
var stagingNameRE = regexp.MustCompile(`(?i)^(.+)~rf[0-9a-f]+\.tmp$`)

// stagingTarget derives the file a staging file was going to replace, by
// stripping its `~RF<hex>.TMP` suffix. ok is false for a name that is not a
// staging file.
func stagingTarget(name string) (target string, ok bool) {
	m := stagingNameRE.FindStringSubmatch(name)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// stagingFile is one SteamCMD staging file found in a server's data dir. Paths
// are slash-separated and relative to the data dir, the way an operator sees
// them in the Files tab.
type stagingFile struct {
	Rel          string
	Target       string
	TargetExists bool
}

// orphan reports whether this is the wreckage of a failed commit: the file it
// was replacing is already gone.
func (s stagingFile) orphan() bool { return !s.TargetExists }

// scanStagingFiles walks root on the host and returns every staging file in
// it, with whether its target exists. It never goes through a container, and
// it is error-tolerant: an unreadable directory is skipped, not fatal — a scan
// that finds less is still worth reporting.
func scanStagingFiles(root string) []stagingFile {
	var out []stagingFile
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return nil
		}
		target, ok := stagingTarget(d.Name())
		if !ok {
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return nil
		}
		_, terr := os.Lstat(filepath.Join(filepath.Dir(p), target))
		out = append(out, stagingFile{
			Rel:          filepath.ToSlash(rel),
			Target:       path.Join(path.Dir(filepath.ToSlash(rel)), target),
			TargetExists: terr == nil,
		})
		return nil
	})
	return out
}

// stagingTree is where the recovery finds and clears staging files: the host
// data dir for the Docker runtime, the in-memory tree for the fake.
type stagingTree interface {
	scan() []stagingFile
	remove(stagingFile) error
}

// hostStagingTree is a server's data dir as the Agent sees it (localDir — never
// the daemon's bindSource view).
type hostStagingTree struct{ root string }

func (h hostStagingTree) scan() []stagingFile { return scanStagingFiles(h.root) }

func (h hostStagingTree) remove(s stagingFile) error {
	return os.Remove(filepath.Join(h.root, filepath.FromSlash(s.Rel)))
}

// Windows' "the file is in use" errors: ERROR_SHARING_VIOLATION and
// ERROR_LOCK_VIOLATION. A delete fails with one of these while a process —
// typically a game container that is still running — holds the file open.
const (
	winErrorSharingViolation syscall.Errno = 32
	winErrorLockViolation    syscall.Errno = 33
)

// isFileLocked reports whether a delete failed because something still has the
// file open. On Linux an open file can be unlinked, so only EBUSY counts.
func isFileLocked(err error) bool {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	if runtime.GOOS == "windows" {
		return errno == winErrorSharingViolation || errno == winErrorLockViolation
	}
	return errno == syscall.EBUSY
}

// orphanCleanup is what clearing the staging files found.
type orphanCleanup struct {
	removed  []stagingFile
	locked   []stagingFile
	failed   []string // "<file>: <error>" for a removal that failed for another reason
	inFlight []stagingFile
}

// clearOrphans deletes every orphaned staging file and leaves the rest alone.
func clearOrphans(found []stagingFile, remove func(stagingFile) error) orphanCleanup {
	var c orphanCleanup
	for _, s := range found {
		if !s.orphan() {
			c.inFlight = append(c.inFlight, s)
			continue
		}
		switch err := remove(s); {
		case err == nil:
			c.removed = append(c.removed, s)
		case isFileLocked(err):
			c.locked = append(c.locked, s)
		default:
			c.failed = append(c.failed, fmt.Sprintf("could not remove orphaned staging file %s (target %s missing): %v", s.Rel, s.Target, err))
		}
	}
	return c
}

// maxNamedStagingFiles caps how many staging files one message names; a big
// update can leave many, and last_error is read in a table cell.
const maxNamedStagingFiles = 5

func nameStaging(files []stagingFile, each func(stagingFile) string) []string {
	var out []string
	for i, s := range files {
		if i == maxNamedStagingFiles {
			out = append(out, fmt.Sprintf("and %d more", len(files)-i))
			break
		}
		out = append(out, each(s))
	}
	return out
}

// describe renders the cleanup for the failure message.
func (c orphanCleanup) describe() string {
	var parts []string
	parts = append(parts, nameStaging(c.removed, func(s stagingFile) string {
		return "removed orphaned staging file " + s.Rel + " (its target " + s.Target + " was missing)"
	})...)
	parts = append(parts, nameStaging(c.locked, func(s stagingFile) string {
		return "orphaned staging file " + s.Rel + " (target " + s.Target + " missing) is still locked — a container may be holding it"
	})...)
	parts = append(parts, c.failed...)
	parts = append(parts, describeInFlight(c.inFlight)...)
	if len(parts) == 0 {
		return "no SteamCMD staging files (*~RF*.TMP) in the data dir"
	}
	return strings.Join(parts, "; ")
}

func describeInFlight(files []stagingFile) []string {
	return nameStaging(files, func(s stagingFile) string {
		return "in-flight staging file " + s.Rel + " (target present) — not touched"
	})
}

// describeLeftovers renders a report-only scan taken after the retry failed.
func describeLeftovers(found []stagingFile) string {
	var orphans, inFlight []stagingFile
	for _, s := range found {
		if s.orphan() {
			orphans = append(orphans, s)
		} else {
			inFlight = append(inFlight, s)
		}
	}
	parts := nameStaging(orphans, func(s stagingFile) string {
		return "orphaned staging file " + s.Rel + " (target " + s.Target + " missing) is back after the retry — something still holds the data dir"
	})
	parts = append(parts, describeInFlight(inFlight)...)
	return strings.Join(parts, "; ")
}

// retryFits is the budget gate for the one retry. The second pass runs only
// when the caller set a deadline and the time left before it is at least as
// long as the first pass took — on a 30 GB tree a retry that cannot finish
// turns a recoverable failure into "install stream interrupted", which is
// worse. No deadline means no way to know, and no retry.
func retryFits(deadline time.Time, hasDeadline bool, now time.Time, firstPass time.Duration) bool {
	return hasDeadline && deadline.Sub(now) >= firstPass
}

// installPass runs one install container to completion. steamErr is the
// SteamCMD failure line it reported ("" for a clean pass). err is a failure of
// the pass itself — it could not be created, it exited non-zero, the context
// ended — which recovery does not try to fix and which the pass has already
// reported.
type installPass func(ctx context.Context) (steamErr string, err error)

// runInstallWithRecovery runs pass and, on a retryable SteamCMD failure, clears
// orphaned staging files from tree and runs it once more if the budget allows.
// It returns the failure message to report ("" for success); err passes a
// pass's own failure straight through. note receives the install-console lines.
func runInstallWithRecovery(ctx context.Context, pass installPass, tree stagingTree, note func(string), now func() time.Time) (failure string, err error) {
	started := now()
	steamErr, err := pass(ctx)
	if err != nil || steamErr == "" {
		return "", err
	}
	firstPass := now().Sub(started)
	msg := describeSteamFailure(steamErr)
	if !steamFailureRetryable(steamErr) {
		return msg, nil
	}

	cleanup := clearOrphans(tree.scan(), tree.remove)
	for _, s := range cleanup.removed {
		note("[kraken] removed orphaned SteamCMD staging file " + s.Rel + " — its target " + s.Target + " is missing, the mark of an update that could not be committed")
	}
	for _, s := range cleanup.locked {
		note("[kraken] orphaned SteamCMD staging file " + s.Rel + " is still locked and could not be removed — a container may be holding the data dir")
	}
	report := cleanup.describe()
	if len(cleanup.removed) == 0 {
		// Nothing changed on disk, so a second pass would be byte-identical to
		// the first. Report what was found instead.
		return msg + "; " + report, nil
	}
	deadline, hasDeadline := ctx.Deadline()
	if !retryFits(deadline, hasDeadline, now(), firstPass) {
		return msg + "; " + report + fmt.Sprintf("; not enough time left for a second pass (the first took %s) — run the install again", firstPass.Round(time.Second)), nil
	}

	note(fmt.Sprintf("[kraken] retrying the install pass once, now that %d orphaned staging file(s) are cleared", len(cleanup.removed)))
	first := steamErr
	steamErr, err = pass(ctx)
	if err != nil || steamErr == "" {
		return "", err
	}
	out := describeSteamFailure(steamErr) + "; this was the one retry — the first pass failed with " + steamStateOnly(first) + ", then " + report
	if left := describeLeftovers(tree.scan()); left != "" {
		out += "; " + left
	}
	return out, nil
}

// steamStateOnly shortens a first-pass failure line to its "state is 0x…"
// phrase for the retry's message, which already carries the full second line.
func steamStateOnly(line string) string {
	if m := steamStateRE.FindString(line); m != "" {
		return strings.TrimSuffix(m, " after update job")
	}
	return line
}
