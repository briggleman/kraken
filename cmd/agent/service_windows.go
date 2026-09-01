//go:build windows

package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
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

// serviceMain handles --service control actions and SCM-launched runs.
// Returns true when it fully handled the invocation (main should return);
// false means this is an interactive console start.
func serviceMain(cfg *config.Config, action string) bool {
	if action != "" {
		if err := serviceControl(action, cfg); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return true
	}
	if isWindowsService() {
		// Running under the SCM: stdout goes nowhere, so logs are written to
		// <state-dir>/agent.log, and shutdown is driven by SCM stop requests
		// instead of console signals.
		if err := runService(cfg); err != nil {
			os.Exit(1)
		}
		return true
	}
	return false
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

// Service identity strings, re-asserted on every registration so an older
// install's wording converges on the current one.
const (
	serviceDisplayName = "Kraken Agent"
	serviceDescription = "Kraken node daemon — runs game servers via the local Docker daemon."
)

// installService registers the agent as an auto-start service, or brings an
// already-registered one's configuration current. The service command line is
// the current invocation minus the --service flag, so whatever configuration
// flags were typed (--root, --addr, …) carry into the service definition
// verbatim.
//
// It is idempotent on purpose (#184). It used to refuse when the service
// existed, which meant the recovery actions were asserted exactly once per
// service lifetime: a service registered before those settings existed kept its
// legacy config through every binary upgrade forever, because upgrades only
// swap the .exe. That is how abyss-win went offline after its fourth
// self-update of a day — three restart slots' worth of legacy config, then
// nothing. Re-running --service install is now the healing path.
func installService(m *mgr.Mgr) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable path: %w", err)
	}
	args := stripServiceFlag(os.Args[1:])

	if s, err := m.OpenService(serviceName); err == nil {
		defer func() { _ = s.Close() }()
		return updateService(s, exe, args)
	}

	s, err := m.CreateService(serviceName, exe, mgr.Config{
		DisplayName: serviceDisplayName,
		Description: serviceDescription,
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

	if err := setRecoveryActions(s); err != nil {
		return err
	}

	fmt.Printf("service %s installed (%s %s)\n", serviceName, exe, joinArgs(args))
	fmt.Printf("start it with: %s --service start\n", filepath.Base(exe))
	return nil
}

// updateService re-registers an existing service's configuration in place:
// command line, delayed auto-start, identity strings, and — the reason this
// path exists — the recovery actions and the non-crash-failure flag.
//
// The existing Config is read and mutated rather than built from scratch so the
// fields this command has no opinion about (service type, error control, and
// especially the account the service runs as) survive untouched. UpdateConfig
// needs a finished BinaryPathName, which CreateService would have assembled
// itself — serviceCommandLine builds the identical string.
//
// Note that the command line is rewritten from THIS invocation's flags: run it
// the way the service was installed (install.ps1 always passes --root), or the
// service loses flags it was registered with. The new command line is printed
// so a mistake is visible immediately.
func updateService(s *mgr.Service, exe string, args []string) error {
	cfg, err := s.Config()
	if err != nil {
		return fmt.Errorf("read service config: %w", err)
	}
	before := cfg.BinaryPathName

	cfg.BinaryPathName = serviceCommandLine(exe, args, syscall.EscapeArg)
	cfg.DisplayName = serviceDisplayName
	cfg.Description = serviceDescription
	cfg.StartType = mgr.StartAutomatic
	cfg.DelayedAutoStart = true
	if err := s.UpdateConfig(cfg); err != nil {
		return fmt.Errorf("update service config: %w", err)
	}

	if err := setRecoveryActions(s); err != nil {
		return err
	}

	fmt.Printf("service %s updated (%s)\n", serviceName, cfg.BinaryPathName)
	if before != cfg.BinaryPathName {
		fmt.Printf("  command line was: %s\n", before)
	}
	fmt.Println("  recovery actions re-asserted: restart after 5s/30s/60s, reset after a day, including non-crash failures")
	return nil
}

// setRecoveryActions applies the current recovery policy to a service, whether
// it was just created or already existed.
func setRecoveryActions(s *mgr.Service) error {
	// Mirror the systemd unit's Restart=on-failure: restart after 5s, and
	// reset the failure counter after a day of clean running.
	if err := s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 60 * time.Second},
	}, 86400); err != nil {
		return fmt.Errorf("set recovery actions: %w", err)
	}
	// By default Windows runs recovery actions only when the process CRASHES
	// (dies without reporting SERVICE_STOPPED). An agent that exits with an
	// error through the service handler — bad config, listen failure, an
	// updated binary that can't stabilize — reports STOPPED with an exit code,
	// which is a "non-crash failure" that recovery ignores unless this flag is
	// set. Without it the self-update revert path never runs: the boot-attempt
	// budget needs SCM to keep restarting the failing build (found by the
	// live Behemoth drill, issue #91).
	if err := s.SetRecoveryActionsOnNonCrashFailures(true); err != nil {
		return fmt.Errorf("set recovery-on-noncrash-failures flag: %w", err)
	}
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
