package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Self-update: the Panel streams a new agent binary over the mTLS gRPC
// channel (it embeds the agent builds matching its own version, so an agent
// can only ever be moved to the Panel's version). The update is transactional:
//
//  1. the incoming binary is checksum-verified before anything is touched
//  2. a sentinel (update.json in the state dir) records the attempt
//  3. the running binary is kept beside the new one as <exe>.old
//  4. after the swap the agent restarts; each boot increments the sentinel's
//     attempt counter, and past maxUpdateBootAttempts the agent swaps .old
//     back and records the failure — the service manager's restart-on-failure
//     loop is the retry engine, the sentinel bounds it
//  5. the sentinel (and .old) are cleared by a health milestone: the first
//     GetNodeInfo served to the Panel, or updateHealthyAfter of clean uptime
//
// A failed-and-reverted update is reported to the Panel in
// NodeInfo.last_update_error until a later update succeeds.

const (
	// maxUpdateBootAttempts is how many boots a freshly-updated binary gets to
	// reach the health milestone before the agent reverts to the previous one.
	maxUpdateBootAttempts = 3

	// updateHealthyAfter clears the update sentinel on clean uptime even if
	// the Panel hasn't polled (e.g. the Panel is briefly down during a fleet
	// update). Long enough that a crash-on-start binary can't reach it.
	updateHealthyAfter = 2 * time.Minute

	// maxUpdateBinaryBytes caps the accepted stream: an agent binary is ~17MB,
	// so anything past this is a corrupt or malicious push.
	maxUpdateBinaryBytes = 256 << 20

	updateSentinelName = "update.json"
	updateFailureName  = "update-failed.json"
)

// updateSentinel is the on-disk record of an in-flight update, written just
// before the binary swap and cleared by the health milestone (or a rollback).
type updateSentinel struct {
	From     string    `json:"from"`
	To       string    `json:"to"`
	Attempts int       `json:"attempts"`
	Started  time.Time `json:"started"`
}

// updateFailure is the on-disk record of the most recent failed update, shown
// to the Panel via NodeInfo until a later update succeeds.
type updateFailure struct {
	Target string    `json:"target"`
	Error  string    `json:"error"`
	Time   time.Time `json:"time"`
}

// SelfUpdater owns the agent's self-update state machine.
type SelfUpdater struct {
	version  string // the running build
	stateDir string
	exePath  string // resolved at construction: on Linux /proc/self/exe changes once the running file is renamed
	restart  func(exePath string)
	logger   *slog.Logger

	mu       sync.Mutex
	updating bool

	shaOnce sync.Once
	sha     string // hex SHA-256 of the running binary; "" if unreadable
}

// NewSelfUpdater wires the updater for the running binary. restart receives
// the executable path (already holding the new binary) and must not return:
// it hands control over (exec on POSIX) or exits for the service manager to
// restart the process (Windows service recovery actions).
//
// The executable path is resolved ONCE here: on Linux /proc/self/exe follows
// the running file, so after the swap renames it the path would point at the
// set-aside .old binary instead of the freshly installed one.
func NewSelfUpdater(version, stateDir string, restart func(exePath string), logger *slog.Logger) (*SelfUpdater, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("selfupdate: resolve executable: %w", err)
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return nil, fmt.Errorf("selfupdate: resolve executable symlink: %w", err)
	}
	return newSelfUpdaterAt(version, stateDir, exe, restart, logger), nil
}

// newSelfUpdaterAt is NewSelfUpdater with an explicit executable path (tests).
func newSelfUpdaterAt(version, stateDir, exePath string, restart func(exePath string), logger *slog.Logger) *SelfUpdater {
	return &SelfUpdater{
		version:  version,
		stateDir: stateDir,
		exePath:  exePath,
		restart:  restart,
		logger:   logger,
	}
}

func (u *SelfUpdater) sentinelPath() string { return filepath.Join(u.stateDir, updateSentinelName) }
func (u *SelfUpdater) failurePath() string  { return filepath.Join(u.stateDir, updateFailureName) }

// BinarySHA is the hex SHA-256 of the running binary, reported in NodeInfo so
// the Panel can compare artifact identity instead of version strings (a
// panel-only release leaves the agent binary byte-identical apart from the
// stamped version, and re-flagging the whole fleet for that trains operators
// to ignore the flag). Hashed once: the file at exePath only changes across a
// restart. "" when the binary can't be read.
func (u *SelfUpdater) BinarySHA() string {
	u.shaOnce.Do(func() {
		f, err := os.Open(u.exePath)
		if err != nil {
			u.logger.Warn("selfupdate: could not hash own binary", "err", err)
			return
		}
		defer func() { _ = f.Close() }()
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			u.logger.Warn("selfupdate: could not hash own binary", "err", err)
			return
		}
		u.sha = hex.EncodeToString(h.Sum(nil))
	})
	return u.sha
}

// LastFailure renders the most recent failed update for NodeInfo ("" = none).
func (u *SelfUpdater) LastFailure() string {
	b, err := os.ReadFile(u.failurePath())
	if err != nil {
		return ""
	}
	var f updateFailure
	if json.Unmarshal(b, &f) != nil {
		return ""
	}
	return fmt.Sprintf("update to %s failed at %s: %s", f.Target, f.Time.UTC().Format(time.RFC3339), f.Error)
}

// runningInContainer reports whether this agent's binary is immutable (a
// container image), in which case self-update must be refused: the swap would
// be lost on the next container restart and diverge from the image.
func runningInContainer() bool {
	if os.Getenv("KRAKEN_IN_CONTAINER") != "" {
		return true
	}
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	return false
}

// Begin validates an update request and claims the single-update-at-a-time
// slot. It returns the temp path the caller should stream the binary to.
func (u *SelfUpdater) Begin(version, targetOS, targetArch string) (tmpPath string, err error) {
	if runningInContainer() {
		return "", errors.New("agent runs in a container; its binary is immutable — pull the new image instead")
	}
	if targetOS != runtime.GOOS || targetArch != runtime.GOARCH {
		return "", fmt.Errorf("binary is %s/%s but this agent is %s/%s", targetOS, targetArch, runtime.GOOS, runtime.GOARCH)
	}
	if version == "" {
		return "", errors.New("update version is required")
	}
	if version == u.version {
		return "", fmt.Errorf("agent is already at %s", version)
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.updating {
		return "", errors.New("an update is already in progress")
	}
	u.updating = true
	return u.exePath + ".new", nil
}

// Abort releases the update slot after a failed receive, removing the partial
// temp file.
func (u *SelfUpdater) Abort(tmpPath string) {
	_ = os.Remove(tmpPath)
	u.mu.Lock()
	u.updating = false
	u.mu.Unlock()
}

// Commit verifies the streamed binary and performs the transactional swap:
// sentinel first, then current → .old, then new → current. On any failure the
// swap is undone and nothing keeps referencing the new binary. It does NOT
// restart — the caller responds to the Panel first, then calls Restart.
func (u *SelfUpdater) Commit(tmpPath, version, wantSHA256 string, gotSHA256 string, size int64) (fromVersion string, err error) {
	defer func() {
		if err != nil {
			u.Abort(tmpPath)
		}
	}()

	if !strings.EqualFold(strings.TrimSpace(wantSHA256), gotSHA256) {
		return "", fmt.Errorf("checksum mismatch: panel declared %s, received bytes hash to %s", wantSHA256, gotSHA256)
	}
	if size == 0 {
		return "", errors.New("received an empty binary")
	}

	// Sentinel before the swap: if we crash mid-swap the next boot still knows
	// an update was in flight.
	if err := u.writeSentinel(updateSentinel{From: u.version, To: version, Started: time.Now()}); err != nil {
		return "", err
	}

	oldPath := u.exePath + ".old"
	_ = os.Remove(oldPath) // stale rollback binary from a prior (successful) update
	if err := os.Rename(u.exePath, oldPath); err != nil {
		_ = os.Remove(u.sentinelPath())
		return "", fmt.Errorf("set aside current binary: %w", err)
	}
	if err := os.Rename(tmpPath, u.exePath); err != nil {
		// Undo: put the running binary back so restarts stay healthy.
		if rerr := os.Rename(oldPath, u.exePath); rerr != nil {
			u.logger.Error("selfupdate: swap failed AND undo failed — binary path is now empty; reinstall required",
				"swap_err", err, "undo_err", rerr, "exe", u.exePath)
		}
		_ = os.Remove(u.sentinelPath())
		return "", fmt.Errorf("install new binary: %w", err)
	}

	u.logger.Info("selfupdate: binary swapped, restart pending",
		"from", u.version, "to", version, "sha256", gotSHA256, "bytes", size)
	return u.version, nil
}

// Restart hands control to the new binary. Called after the RPC response has
// been sent; never returns.
func (u *SelfUpdater) Restart() {
	u.logger.Info("selfupdate: restarting", "exe", u.exePath)
	u.restart(u.exePath)
}

// MarkHealthy is the update health milestone: the running binary has proven
// itself (the Panel completed a NodeInfo poll, or clean uptime elapsed), so
// the in-flight sentinel for THIS version and the rollback binary are cleared.
// A cleared update also clears any older failure record. Safe to call often.
func (u *SelfUpdater) MarkHealthy() {
	st, err := u.readSentinel()
	if err != nil {
		return // no update in flight
	}
	if st.To != u.version {
		// Sentinel for some other version (e.g. we're the rolled-back binary
		// racing a boot-time cleanup) — not ours to clear.
		return
	}
	_ = os.Remove(u.sentinelPath())
	_ = os.Remove(u.failurePath())
	_ = os.Remove(u.exePath + ".old")
	u.logger.Info("selfupdate: update confirmed healthy", "version", u.version, "boot_attempts", st.Attempts)
}

// StartHealthTimer arms the uptime-based fallback milestone.
func (u *SelfUpdater) StartHealthTimer() {
	if _, err := u.readSentinel(); err != nil {
		return
	}
	time.AfterFunc(updateHealthyAfter, u.MarkHealthy)
}

func (u *SelfUpdater) readSentinel() (updateSentinel, error) {
	var st updateSentinel
	b, err := os.ReadFile(u.sentinelPath())
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return st, err
	}
	return st, nil
}

func (u *SelfUpdater) writeSentinel(st updateSentinel) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(u.stateDir, 0o700); err != nil {
		return fmt.Errorf("selfupdate: create state dir: %w", err)
	}
	if err := os.WriteFile(u.sentinelPath(), b, 0o600); err != nil {
		return fmt.Errorf("selfupdate: write sentinel: %w", err)
	}
	return nil
}

// CheckBoot runs once at startup, before serving. It advances the in-flight
// update's attempt counter and, past the attempt budget, reverts to the .old
// binary. It returns true when the caller must restart immediately (the
// binary on disk changed under us).
func (u *SelfUpdater) CheckBoot() (restartNow bool) {
	st, err := u.readSentinel()
	if err != nil {
		return false
	}

	st.Attempts++
	if st.Attempts <= maxUpdateBootAttempts {
		if err := u.writeSentinel(st); err != nil {
			u.logger.Warn("selfupdate: could not advance boot counter", "err", err)
		}
		u.logger.Info("selfupdate: booting freshly-updated binary",
			"version", u.version, "attempt", st.Attempts, "of", maxUpdateBootAttempts)
		return false
	}

	// Attempt budget exhausted: the updated binary never reached the health
	// milestone. Revert to the previous binary.
	oldPath := u.exePath + ".old"
	if _, serr := os.Stat(oldPath); serr != nil {
		// Nothing to revert to; stop counting and record what happened.
		u.recordFailure(st.To, fmt.Sprintf("binary failed to stabilize after %d boots and no rollback binary was found", st.Attempts-1))
		_ = os.Remove(u.sentinelPath())
		u.logger.Error("selfupdate: update failed but rollback binary is missing; continuing on the current binary",
			"target", st.To)
		return false
	}

	failedPath := u.exePath + ".failed"
	_ = os.Remove(failedPath)
	if err := os.Rename(u.exePath, failedPath); err != nil {
		u.logger.Error("selfupdate: rollback: could not set aside failed binary", "err", err)
		return false
	}
	if err := os.Rename(oldPath, u.exePath); err != nil {
		// Try to restore the (bad but present) binary rather than leave nothing.
		_ = os.Rename(failedPath, u.exePath)
		u.logger.Error("selfupdate: rollback: could not restore previous binary", "err", err)
		return false
	}
	u.recordFailure(st.To, fmt.Sprintf("binary failed to stabilize after %d boots; reverted to %s", st.Attempts-1, st.From))
	_ = os.Remove(u.sentinelPath())
	u.logger.Error("selfupdate: update failed — reverted to previous binary",
		"target", st.To, "reverted_to", st.From, "boots", st.Attempts-1)
	return true
}

func (u *SelfUpdater) recordFailure(target, msg string) {
	b, err := json.Marshal(updateFailure{Target: target, Error: msg, Time: time.Now()})
	if err != nil {
		return
	}
	_ = os.WriteFile(u.failurePath(), b, 0o600)
}

// hashingWriter tees the update stream into a file while hashing it, capping
// the accepted size.
type hashingWriter struct {
	f    *os.File
	h    hash.Hash
	size int64
}

func newBinaryWriter(path string) (*hashingWriter, error) {
	// 0755: this file becomes the executable.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return nil, err
	}
	return &hashingWriter{f: f, h: sha256.New()}, nil
}

func (w *hashingWriter) Write(p []byte) (int, error) {
	if w.size+int64(len(p)) > maxUpdateBinaryBytes {
		return 0, fmt.Errorf("binary exceeds %d bytes", int64(maxUpdateBinaryBytes))
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	if n > 0 {
		_, _ = w.h.Write(p[:n])
	}
	return n, err
}

func (w *hashingWriter) Close() error { return w.f.Close() }

func (w *hashingWriter) SumHex() string { return hex.EncodeToString(w.h.Sum(nil)) }
