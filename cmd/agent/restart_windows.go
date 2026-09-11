//go:build windows

package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"github.com/briggleman/kraken/internal/agent/config"
	"github.com/briggleman/kraken/internal/shared/version"
)

// restartAgent brings the process back up on the binary now at exePath.
// Windows has no exec(2), so under the SCM the restart is a two-step handoff:
// launch the restart helper, then stop this service gracefully. The service
// must NOT restart by exiting into the SCM's failure-recovery actions: those
// are a finite budget (three restarts, reset only after a day without
// failures), and a bad day exhausts it — the 2026-09-10 bind crash-loop on
// abyss-win spent the budget by breakfast, after which every self-update exit
// stranded the service STOPPED with a healthy new binary on disk. A clean stop
// consumes no failure slot, so recovery actions stay reserved for what they
// exist for: real crashes, including the boot-attempt/rollback loop of an
// updated binary that can't start.
//
// The helper is the swapped binary itself, run as `--service restart-helper`
// (see restarthelper.go). It used to be a detached PowerShell `Start-Service`
// one-liner, which launched fine and then never started the service — twice on
// abyss-win, silently (#271). Because the stop is clean there is no second
// restart path, so the helper has to be something we can see: our own code,
// writing <state>/restart-helper.log, and confirmed alive before we let go.
//
// From an interactive console the operator restarts by hand.
func restartAgent(exePath, stateDir string) {
	if !isWindowsService() {
		slog.Info("selfupdate: binary swapped — restart the agent to run it", "exe", exePath)
		os.Exit(1)
	}
	if err := spawnRestartHelper(exePath, stateDir); err != nil {
		// No confirmed helper means a clean stop would strand the service —
		// fall back to the crash exit so SCM recovery (budget permitting)
		// restarts us.
		slog.Warn("selfupdate: could not launch the restart helper — exiting for SCM recovery instead", "err", err)
		os.Exit(1)
	}
	if err := requestOwnServiceStop(); err != nil {
		slog.Warn("selfupdate: graceful self-stop failed — exiting for SCM recovery instead", "err", err)
		os.Exit(1)
	}
	slog.Info("selfupdate: graceful stop requested; the restart helper relaunches the service onto the swapped binary",
		"exe", exePath, "helper_log", filepath.Join(stateDir, restartHelperLogName))
	// The stop control lands in the service handler, which cancels the run
	// context and exits the process cleanly. Restart's contract is to never
	// return, so this goroutine parks until that happens.
	select {}
}

// spawnRestartHelper launches the swapped binary as a detached, hidden
// `--service restart-helper` process carrying this invocation's configuration
// flags (so it resolves the same state dir), then waits for the helper's ready
// marker. A helper that never writes it is a binary that cannot run at all —
// exactly the case where letting go would strand the service.
//
// The process breaks away from any job the service happens to live in, since a
// kill-on-close job is one way a child dies with its parent; a job that forbids
// breakaway refuses the flag, so the launch is retried without it.
func spawnRestartHelper(exePath, stateDir string) error {
	ready := filepath.Join(stateDir, restartHelperReadyName)
	_ = os.Remove(ready) // a stale marker from a helper that crashed last time

	args := append(stripServiceFlag(os.Args[1:]), "--service", "restart-helper")
	base := uint32(windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP)
	var err error
	for i, flags := range []uint32{base | windows.CREATE_BREAKAWAY_FROM_JOB, base} {
		cmd := exec.Command(exePath, args...)
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: flags}
		if err = cmd.Start(); err == nil {
			_ = cmd.Process.Release()
			if i > 0 {
				slog.Info("selfupdate: restart helper launched without job breakaway (the job forbids it)")
			}
			break
		}
		slog.Warn("selfupdate: restart helper launch attempt failed", "attempt", i+1, "err", err)
	}
	if err != nil {
		return fmt.Errorf("launch %s --service restart-helper: %w", exePath, err)
	}

	deadline := time.Now().Add(restartHelperReadyWait)
	for {
		if _, serr := os.Stat(ready); serr == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("restart helper did not report ready within %s (no %s) — the swapped binary may not be runnable",
				restartHelperReadyWait, ready)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// requestOwnServiceStop sends this service a Stop control through the SCM —
// the identical path an operator's `--service stop` takes, so the shutdown is
// reported to the SCM as a clean SERVICE_STOPPED rather than a crash.
func requestOwnServiceStop() error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer func() { _ = m.Disconnect() }()
	s, err := m.OpenService(serviceName)
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()
	_, err = s.Control(svc.Stop)
	return err
}

// restartHelperControl is the `--service restart-helper` entry point: the
// detached process the updating agent spawned. It writes the ready marker
// first (the agent is waiting on it), then drives the SCM through
// stop → start → running, logging every step to <state>/restart-helper.log —
// the first place to look when a node does not come back after an update.
func restartHelperControl(m *mgr.Mgr, cfg *config.Config) error {
	logFile, err := openRestartHelperLog(cfg.StateDir)
	if err != nil {
		return err
	}
	defer func() { _ = logFile.Close() }()
	logf := func(format string, args ...any) {
		fmt.Fprintf(logFile, "%s %s\n", time.Now().Format(time.RFC3339Nano), fmt.Sprintf(format, args...))
	}
	exe, _ := os.Executable()
	logf("restart helper started (pid %d, %s %s)", os.Getpid(), exe, version.String())

	ready := filepath.Join(cfg.StateDir, restartHelperReadyName)
	if err := os.WriteFile(ready, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		logf("FAILED to write ready marker %s: %v", ready, err)
		return fmt.Errorf("write ready marker: %w", err)
	}
	defer func() { _ = os.Remove(ready) }()

	s, err := m.OpenService(serviceName)
	if err != nil {
		logf("FAILED to open service %s: %v", serviceName, err)
		return fmt.Errorf("open service %s: %w", serviceName, err)
	}
	defer func() { _ = s.Close() }()

	if err := runRestartHelper(&mgrRestartSCM{s}, logf, realRestartHelperClock()); err != nil {
		logf("FAILED: %v", err)
		return err
	}
	logf("done")
	return nil
}

// openRestartHelperLog appends to the helper log, truncating a log that has
// grown past a megabyte — the helper runs once per update, so this is years of
// history, and one file is easier to hand an operator than a rotation.
func openRestartHelperLog(stateDir string) (*os.File, error) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("create state dir %s: %w", stateDir, err)
	}
	path := filepath.Join(stateDir, restartHelperLogName)
	flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	if st, err := os.Stat(path); err == nil && st.Size() > 1<<20 {
		flags = os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	}
	return os.OpenFile(path, flags, 0o600)
}

// mgrRestartSCM adapts an open mgr.Service to the OS-neutral restartSCM the
// state machine is written (and tested) against.
type mgrRestartSCM struct{ s *mgr.Service }

func (a *mgrRestartSCM) State() (string, error) {
	st, err := a.s.Query()
	if err != nil {
		return "", err
	}
	return serviceStateName(st.State), nil
}

func (a *mgrRestartSCM) Start() error {
	err := a.s.Start()
	if errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING) {
		return errServiceAlreadyRunning
	}
	return err
}
