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

	"github.com/briggleman/kraken/internal/shared/agentpb"
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
// pass could fix once the tree is cleaned up: the "state is 0x… after update
// job" family. The rest — No subscription, Not for anonymous, a failed branch
// password, an invalid platform, the phrase-form "Disk write failure" — fail
// identically on a retry.
//
// Retryable by phrase is not the same as retried. A state line also covers the
// disk-space and disk-write shapes (0x202, 0x606), where a second pass would
// fail the same way; what stops a wasted retry there is the orphan gate — the
// pass reruns only when an orphaned staging file was actually removed.
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
	{0x200, "update paused"},
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
// "update started, update paused, update required". Bits Steam does not
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

// steamState extracts the hex state and its value from a failure line.
func steamState(line string) (hex string, v uint32, ok bool) {
	m := steamStateRE.FindStringSubmatch(line)
	if m == nil {
		return "", 0, false
	}
	n, err := strconv.ParseUint(m[1][2:], 16, 32)
	if err != nil {
		return "", 0, false
	}
	return m[1], uint32(n), true
}

// describeSteamFailure returns the SteamCMD failure line with its state decoded
// in place, e.g. "Error! App '4019830' state is 0x602 (update started, update
// paused, update required) after update job." A line without a state is
// returned unchanged.
func describeSteamFailure(line string) string {
	loc := steamStateRE.FindStringSubmatchIndex(line)
	if loc == nil {
		return line
	}
	_, v, ok := steamState(line)
	if !ok {
		return line
	}
	return line[:loc[3]] + " (" + decodeSteamState(v) + ")" + line[loc[3]:]
}

// stateSummary is a failure line's state in short, for the retry's message:
// "state 0x602 = update started, update paused, update required".
func stateSummary(line string) string {
	hex, v, ok := steamState(line)
	if !ok {
		return sentence(line)
	}
	return "state " + hex + " = " + decodeSteamState(v)
}

// sentence trims the trailing period SteamCMD ends its lines with, so the
// failure message can be joined into one sentence.
func sentence(s string) string {
	return strings.TrimRight(strings.TrimSpace(s), ".")
}

// joinClauses joins the non-empty parts of a failure message with "; ".
func joinClauses(parts ...string) string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, "; ")
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
//
// A target counts as missing only when the filesystem says it does not exist.
// A permission or I/O error on it is not proof of absence, and deleting a
// `.TMP` whose target is merely unreadable could throw away the only good copy.
// The Agent's own restore scratch (`.kraken-restore-*`, `*.kraken-aside-*`) is
// skipped, as the backup walk skips it.
func scanStagingFiles(root string) []stagingFile {
	var out []stagingFile
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if p != root && isRestoreScratch(d.Name()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
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
			TargetExists: !errors.Is(terr, fs.ErrNotExist),
		})
		return nil
	})
	return out
}

// stagingTree is where the recovery finds and clears staging files.
type stagingTree interface {
	scan() []stagingFile
	remove(stagingFile) error
}

// hostStagingTree is a server's data dir as the Agent sees it (localDir — never
// the daemon's bindSource view). The fake runtime uses it too, over a temp dir.
type hostStagingTree struct{ root string }

func (h hostStagingTree) scan() []stagingFile {
	if h.root == "" {
		return nil
	}
	return scanStagingFiles(h.root)
}

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
	failed   []orphanFailure
	inFlight []stagingFile
}

// orphanFailure is an orphan whose removal failed for a reason other than a lock.
type orphanFailure struct {
	file stagingFile
	err  error
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
			c.failed = append(c.failed, orphanFailure{file: s, err: err})
		}
	}
	return c
}

// maxNamedStagingFiles caps how many staging files one message names per
// category; a big update can leave many, and last_error is read in a table
// cell.
const maxNamedStagingFiles = 5

func nameCapped[T any](items []T, each func(T) string) []string {
	var out []string
	for i, it := range items {
		if i == maxNamedStagingFiles {
			out = append(out, fmt.Sprintf("and %d more", len(items)-i))
			break
		}
		out = append(out, each(it))
	}
	return out
}

// describe renders the cleanup for the failure message.
func (c orphanCleanup) describe() string {
	var parts []string
	parts = append(parts, nameCapped(c.removed, func(s stagingFile) string {
		return "removed orphaned staging file " + s.Rel + " (its target " + s.Target + " was missing)"
	})...)
	parts = append(parts, nameCapped(c.locked, func(s stagingFile) string {
		return "orphaned staging file " + s.Rel + " (target " + s.Target + " missing) is still locked — a container may be holding it"
	})...)
	parts = append(parts, nameCapped(c.failed, func(f orphanFailure) string {
		return fmt.Sprintf("could not remove orphaned staging file %s (target %s missing): %v", f.file.Rel, f.file.Target, f.err)
	})...)
	parts = append(parts, describeInFlight(c.inFlight)...)
	if len(parts) == 0 {
		return "no SteamCMD staging files (*~RF*.TMP) in the data dir"
	}
	return strings.Join(parts, "; ")
}

func describeInFlight(files []stagingFile) []string {
	return nameCapped(files, func(s stagingFile) string {
		return "in-flight staging file " + s.Rel + " (target present) — not touched"
	})
}

// describeLeftovers renders a report-only scan taken after the retry failed.
// An orphan the first cleanup had removed that is there again came back on the
// second pass — the commit failed the same way, so something still holds the
// data dir. Any other orphan is new, and is only reported.
func describeLeftovers(found []stagingFile, removed []stagingFile) string {
	cleared := make(map[string]bool, len(removed))
	for _, s := range removed {
		cleared[s.Rel] = true
	}
	var back, fresh, inFlight []stagingFile
	for _, s := range found {
		switch {
		case !s.orphan():
			inFlight = append(inFlight, s)
		case cleared[s.Rel]:
			back = append(back, s)
		default:
			fresh = append(fresh, s)
		}
	}
	parts := nameCapped(back, func(s stagingFile) string {
		return "orphaned staging file " + s.Rel + " came back on the second pass — something still holds the data dir"
	})
	parts = append(parts, nameCapped(fresh, func(s stagingFile) string {
		return "orphaned staging file " + s.Rel + " (target " + s.Target + " missing) — left in place"
	})...)
	parts = append(parts, describeInFlight(inFlight)...)
	return strings.Join(parts, "; ")
}

// The budget gate's headroom. After the install stream ends the Panel still
// re-renders the config and starts the server on the same 30-minute context,
// so a retry must leave room for that as well as for a second pass that may
// run a little slower than the first.
const (
	// retryPassFactorPct is the second pass's expected length as a percentage
	// of the first's.
	retryPassFactorPct = 125
	// retryReserve is what is kept back for the Panel's config apply and start
	// after the install stream.
	retryReserve = 2 * time.Minute
)

// retryFits is the budget gate for the one retry: the second pass runs only
// when the caller set a deadline and the time left before it covers
// firstPass × 1.25 plus retryReserve. On a 30 GB tree a retry that cannot
// finish turns a recoverable failure into "install stream interrupted", which
// is worse than not retrying.
func retryFits(deadline time.Time, now time.Time, firstPass time.Duration) bool {
	need := firstPass*retryPassFactorPct/100 + retryReserve
	return deadline.Sub(now) >= need
}

// steamGuardEnv is the install env var the Panel sets to a one-time Steam
// Guard code (steamInstallEnv in the Panel's handlers_server.go).
const steamGuardEnv = "STEAM_GUARD"

// noRetryReason says why an install request must never be retried
// automatically, or "" when it may be.
func noRetryReason(req *agentpb.InstallServerRequest) string {
	if req.GetEnv()[steamGuardEnv] != "" {
		return "this install used a one-time Steam Guard code, which a second pass cannot replay"
	}
	return ""
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
//
// noRetry, when set, is why a second pass must not run at all — an install
// that authenticated with a one-time Steam Guard code cannot replay it, and a
// second pass would fail on the login and bury the first pass's report. The
// orphans are still cleared, so the next install starts clean.
func runInstallWithRecovery(ctx context.Context, pass installPass, tree stagingTree, note func(string), now func() time.Time, noRetry string) (failure string, err error) {
	started := now()
	steamErr, err := pass(ctx)
	if err != nil || steamErr == "" {
		return "", err
	}
	firstPass := now().Sub(started)
	if !steamFailureRetryable(steamErr) {
		return describeSteamFailure(steamErr), nil
	}
	msg := sentence(describeSteamFailure(steamErr))

	cleanup := clearOrphans(tree.scan(), tree.remove)
	for _, s := range cleanup.removed {
		note("[kraken] removed orphaned SteamCMD staging file " + s.Rel + " — its target " + s.Target + " is missing, the mark of an update that could not be committed")
	}
	for _, s := range cleanup.locked {
		note("[kraken] orphaned SteamCMD staging file " + s.Rel + " is still locked and could not be removed — a container may be holding the data dir")
	}
	report := cleanup.describe()
	deadline, hasDeadline := ctx.Deadline()
	switch {
	case len(cleanup.removed) == 0:
		// Nothing changed on disk, so a second pass would be byte-identical to
		// the first. Report what was found instead.
		return joinClauses(msg, report), nil
	case len(cleanup.locked) > 0:
		// A lock means something still holds the data dir: the retry would
		// fail the same way, at twice the cost.
		return joinClauses(msg, report, "no automatic retry while a staging file is locked — stop whatever holds the data dir, then run the install again"), nil
	case noRetry != "":
		return joinClauses(msg, report, noRetry+", so no automatic retry — run the install again"), nil
	case !hasDeadline:
		return joinClauses(msg, report, "no deadline on this install, so no automatic retry — run the install again"), nil
	case !retryFits(deadline, now(), firstPass):
		return joinClauses(msg, report, fmt.Sprintf("not enough time left for a second pass (the first took %s) — run the install again", firstPass.Round(time.Second))), nil
	}

	note(fmt.Sprintf("[kraken] retrying the install pass once, now that %d orphaned staging file(s) are cleared", len(cleanup.removed)))
	first := steamErr
	steamErr, err = pass(ctx)
	if err != nil {
		return "", err
	}
	names := strings.Join(nameCapped(cleanup.removed, func(s stagingFile) string { return s.Rel }), ", ")
	if steamErr == "" {
		// The only other record of what was removed is the line before the
		// second pass, which a long pass can scroll out of the install buffer.
		// Say it again, last.
		note(fmt.Sprintf("[kraken] recovered: removed %d orphaned staging file(s) before this pass: %s", len(cleanup.removed), names))
		return "", nil
	}
	return joinClauses(
		sentence(describeSteamFailure(steamErr)),
		fmt.Sprintf("second pass after removing %d orphaned staging file(s) (first pass: %s): %s", len(cleanup.removed), stateSummary(first), names),
		describeLeftovers(tree.scan(), cleanup.removed),
	), nil
}
