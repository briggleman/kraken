//go:build windows

package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"github.com/briggleman/kraken/internal/agent/config"
)

// serviceName is the Windows service identity. Fixed (not configurable): one
// agent service per host matches the one-agent-per-Docker-daemon model, and a
// stable name is what install.ps1, docs, and operators grep for.
const serviceName = "kraken-agent"

// maxLogBytes rotates <state-dir>/agent.log once it grows past this size, so
// a long-lived service doesn't fill the disk. One .old generation is kept.
const maxLogBytes = 10 << 20 // 10 MiB

// isWindowsService reports whether the process was started by the Service
// Control Manager (as opposed to an interactive console).
func isWindowsService() bool {
	inService, err := svc.IsWindowsService()
	return err == nil && inService
}

// agentService adapts run() to the SCM handler protocol: report
// StartPending → Running, run the agent in a goroutine, and translate a Stop
// or Shutdown control into a context cancellation for a graceful stop.
type agentService struct {
	cfg    *config.Config
	logger *slog.Logger
}

func (a *agentService) Execute(_ []string, req <-chan svc.ChangeRequest, status chan<- svc.Status) (ssec bool, errno uint32) {
	const accepted = svc.AcceptStop | svc.AcceptShutdown
	status <- svc.Status{State: svc.StartPending}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(ctx, a.logger, a.cfg) }()

	status <- svc.Status{State: svc.Running, Accepts: accepted}
	for {
		select {
		case err := <-done:
			// The agent exited on its own — a listen failure, a fatal enroll
			// error, etc. Report it so SCM recovery actions can restart us.
			if err != nil {
				a.logger.Error("agent exited with error", "err", err)
				return false, 1
			}
			return false, 0
		case c := <-req:
			switch c.Cmd {
			case svc.Interrogate:
				status <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending}
				cancel()
				select {
				case <-done:
				case <-time.After(30 * time.Second):
					a.logger.Warn("graceful stop timed out; exiting")
				}
				return false, 0
			}
		}
	}
}

// runService runs the agent under the SCM with logs going to
// <state-dir>/agent.log (stdout is not connected for services).
func runService(cfg *config.Config) error {
	logFile, err := openServiceLog(cfg.StateDir)
	if err != nil {
		// Nowhere sensible to report this except the SCM error code.
		return err
	}
	defer func() { _ = logFile.Close() }()
	logger := slog.New(slog.NewJSONHandler(logFile, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)
	logger.Info("starting as a Windows service", "service", serviceName, "log", logFile.Name())

	if err := svc.Run(serviceName, &agentService{cfg: cfg, logger: logger}); err != nil {
		logger.Error("service run failed", "err", err)
		return err
	}
	return nil
}

// openServiceLog opens (appending) the service log file, rotating a too-large
// existing log aside first.
func openServiceLog(stateDir string) (*os.File, error) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("create state dir %s: %w", stateDir, err)
	}
	path := filepath.Join(stateDir, "agent.log")
	if st, err := os.Stat(path); err == nil && st.Size() > maxLogBytes {
		_ = os.Remove(path + ".old")
		_ = os.Rename(path, path+".old")
	}
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
}

// serviceControl implements --service install|uninstall|start|stop.
func serviceControl(action string, cfg *config.Config) error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to service manager (run from an elevated shell): %w", err)
	}
	defer func() { _ = m.Disconnect() }()

	switch action {
	case "install":
		return installService(m)
	case "uninstall":
		return uninstallService(m)
	case "start":
		s, err := m.OpenService(serviceName)
		if err != nil {
			return fmt.Errorf("open service %s (is it installed?): %w", serviceName, err)
		}
		defer func() { _ = s.Close() }()
		if err := s.Start(); err != nil {
			return fmt.Errorf("start %s: %w", serviceName, err)
		}
		fmt.Printf("service %s started (logs: %s)\n", serviceName, filepath.Join(cfg.StateDir, "agent.log"))
		return nil
	case "stop":
		s, err := m.OpenService(serviceName)
		if err != nil {
			return fmt.Errorf("open service %s (is it installed?): %w", serviceName, err)
		}
		defer func() { _ = s.Close() }()
		return stopService(s)
	default:
		return fmt.Errorf("unknown service action %q", action)
	}
}

// installService registers the agent as an auto-start service. The service
// command line is the current invocation minus the --service flag, so
// whatever configuration flags were typed at install time (--root, --addr, …)
// carry into the service definition verbatim.
func installService(m *mgr.Mgr) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable path: %w", err)
	}

	if s, err := m.OpenService(serviceName); err == nil {
		_ = s.Close()
		return fmt.Errorf("service %s already exists — run --service uninstall first to change its configuration", serviceName)
	}

	args := stripServiceFlag(os.Args[1:])
	s, err := m.CreateService(serviceName, exe, mgr.Config{
		DisplayName: "Kraken Agent",
		Description: "Kraken node daemon — runs game servers via the local Docker daemon.",
		StartType:   mgr.StartAutomatic,
		// Game containers need Docker up; delay past the boot rush the same
		// way the systemd unit orders After=docker.service. (Docker Desktop
		// starts late anyway; the agent serves degraded until it appears.)
		DelayedAutoStart: true,
	}, args...)
	if err != nil {
		return fmt.Errorf("create service: %w", err)
	}
	defer func() { _ = s.Close() }()

	// Mirror the systemd unit's Restart=on-failure: restart after 5s, and
	// reset the failure counter after a day of clean running.
	if err := s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 60 * time.Second},
	}, 86400); err != nil {
		return fmt.Errorf("set recovery actions: %w", err)
	}

	fmt.Printf("service %s installed (%s %s)\n", serviceName, exe, joinArgs(args))
	fmt.Printf("start it with: %s --service start\n", filepath.Base(exe))
	return nil
}

func uninstallService(m *mgr.Mgr) error {
	s, err := m.OpenService(serviceName)
	if err != nil {
		return fmt.Errorf("open service %s (is it installed?): %w", serviceName, err)
	}
	defer func() { _ = s.Close() }()
	if err := stopService(s); err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v\n", err)
	}
	if err := s.Delete(); err != nil {
		return fmt.Errorf("delete service: %w", err)
	}
	fmt.Printf("service %s uninstalled\n", serviceName)
	return nil
}

// stopService requests a stop and waits (bounded) for the service to report
// stopped. A service that is already stopped is not an error.
func stopService(s *mgr.Service) error {
	st, err := s.Query()
	if err != nil {
		return fmt.Errorf("query service: %w", err)
	}
	if st.State == svc.Stopped {
		return nil
	}
	st, err = s.Control(svc.Stop)
	if err != nil {
		return fmt.Errorf("stop service: %w", err)
	}
	deadline := time.Now().Add(45 * time.Second)
	for st.State != svc.Stopped {
		if time.Now().After(deadline) {
			return fmt.Errorf("service did not stop within 45s (state %d)", st.State)
		}
		time.Sleep(500 * time.Millisecond)
		if st, err = s.Query(); err != nil {
			return fmt.Errorf("query service: %w", err)
		}
	}
	fmt.Printf("service %s stopped\n", serviceName)
	return nil
}
