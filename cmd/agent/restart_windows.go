//go:build windows

package main

import (
	"log/slog"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// restartAgent brings the process back up on the binary now at exePath.
// Windows has no exec(2), so under the SCM the restart is a two-step handoff:
// schedule an explicit delayed `Start-Service`, then stop this service
// gracefully. The service must NOT restart by exiting into the SCM's
// failure-recovery actions: those are a finite budget (three restarts, reset
// only after a day without failures), and a bad day exhausts it — the
// 2026-09-10 bind crash-loop on abyss-win spent the budget by breakfast, after
// which every self-update exit stranded the service STOPPED with a healthy new
// binary on disk. A clean stop consumes no failure slot, so recovery actions
// stay reserved for what they exist for: real crashes, including the
// boot-attempt/rollback loop of an updated binary that can't start.
//
// From an interactive console the operator restarts by hand.
func restartAgent(exePath string) {
	if !isWindowsService() {
		slog.Info("selfupdate: binary swapped — restart the agent to run it", "exe", exePath)
		os.Exit(1)
	}
	if err := scheduleServiceStart(); err != nil {
		// No starter means a clean stop would strand the service — fall back to
		// the crash exit so SCM recovery (budget permitting) restarts us.
		slog.Warn("selfupdate: could not schedule the service start — exiting for SCM recovery instead", "err", err)
		os.Exit(1)
	}
	if err := requestOwnServiceStop(); err != nil {
		slog.Warn("selfupdate: graceful self-stop failed — exiting for SCM recovery instead", "err", err)
		os.Exit(1)
	}
	slog.Info("selfupdate: graceful stop requested; the scheduled start relaunches onto the swapped binary", "exe", exePath)
	// The stop control lands in the service handler, which cancels the run
	// context and exits the process cleanly. Restart's contract is to never
	// return, so this goroutine parks until that happens.
	select {}
}

// scheduleServiceStart spawns a detached helper that starts the service again
// once this process has stopped. A retry loop rather than one delayed shot:
// the SCM refuses a start while the service is still STOP_PENDING, and the
// graceful stop can take up to the handler's 30s budget.
func scheduleServiceStart() error {
	script := "for ($i = 0; $i -lt 20; $i++) { Start-Sleep -Seconds 3; " +
		"try { Start-Service '" + serviceName + "' -ErrorAction Stop; break } catch {} }"
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow: true,
		// Detached: the helper must survive this process exiting.
		CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP,
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
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
