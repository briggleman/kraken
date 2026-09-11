package main

import (
	"errors"
	"fmt"
	"time"
)

// The restart helper is the second half of a Windows self-update. Windows has
// no exec(2), and a service process cannot start its own service — the SCM only
// considers the service stopped once the handler has returned, and by then the
// process is gone. So the updating agent spawns the freshly-swapped binary as a
// detached `--service restart-helper` process, stops itself cleanly, and the
// helper waits for that stop to land, starts the service through the SCM API,
// and confirms it is running.
//
// It replaced a detached `powershell.exe … Start-Service` one-liner that failed
// silently on abyss-win for every update it was asked to perform (#271): the
// helper process launched, the service stopped cleanly, and nothing ever
// started it again — and because the stop was clean, SCM recovery correctly
// stayed out of it. The helper is therefore the ONLY restart path, which is
// why this one is our own binary, logs every step to <state>/restart-helper.log,
// and proves it is alive (the ready marker) before the agent lets go.
//
// The state machine below is OS-neutral so it is tested on every platform; only
// the SCM adapter and the process spawn are Windows-only (restart_windows.go).
const (
	restartHelperLogName   = "restart-helper.log"
	restartHelperReadyName = "restart-helper.ready"

	// restartHelperPoll is how often the helper re-reads the service state.
	restartHelperPoll = 2 * time.Second
	// restartHelperStopWait bounds the wait for the old process to stop. The
	// handler's own graceful budget is 30s; the rest is headroom for a slow
	// Docker client teardown.
	restartHelperStopWait = 3 * time.Minute
	// restartHelperStartWait bounds start retries and the wait for Running.
	restartHelperStartWait = 2 * time.Minute
	// restartHelperReadyWait is how long the updating agent waits for the helper
	// to write its ready marker before concluding the new binary cannot even
	// run and falling back to the crash-exit → SCM recovery path.
	restartHelperReadyWait = 5 * time.Second
)

// restartSCM is the slice of the SCM the helper needs, expressed in the same
// OS-neutral state names serviceStateName produces.
type restartSCM interface {
	// State reports the service's current run state ("stopped", "running",
	// "stop pending", "start pending", …).
	State() (string, error)
	// Start asks the SCM to start the service. It returns
	// errServiceAlreadyRunning when the service is already up.
	Start() error
}

// errServiceAlreadyRunning is the adapter's translation of
// ERROR_SERVICE_ALREADY_RUNNING: someone (an operator, SCM recovery) beat us to
// the start, which is a success as far as the helper is concerned.
var errServiceAlreadyRunning = errors.New("service is already running")

// restartHelperClock is the helper's view of time, injectable so the tests run
// the three-minute state machine instantly.
type restartHelperClock struct {
	now   func() time.Time
	sleep func(time.Duration)
}

func realRestartHelperClock() restartHelperClock {
	return restartHelperClock{now: time.Now, sleep: time.Sleep}
}

// runRestartHelper drives the SCM through stop → start → running, logging each
// state change through logf. It returns nil once the service is running again
// (whoever started it), and an error naming the phase that gave up.
func runRestartHelper(scm restartSCM, logf func(format string, args ...any), clk restartHelperClock) error {
	// Phase 1: wait for the updating process to stop. "running" seen FIRST is
	// the old process still shutting down and means keep waiting; "running"
	// seen after a stop was observed means someone else already restarted the
	// service, and there is nothing left for us to do.
	deadline := clk.now().Add(restartHelperStopWait)
	last, sawStopping := "", false
	for {
		st, err := scm.State()
		switch {
		case err != nil:
			logf("query service state: %v", err)
		case st != last:
			logf("service is %s", st)
			last = st
		}
		if err == nil {
			if st == "stopped" {
				break
			}
			if st == "stop pending" {
				sawStopping = true
			}
			if st == "running" && sawStopping {
				logf("service was restarted by someone else — nothing to do")
				return nil
			}
		}
		if clk.now().After(deadline) {
			return fmt.Errorf("service did not reach stopped within %s (last state %q)", restartHelperStopWait, last)
		}
		clk.sleep(restartHelperPoll)
	}

	// Phase 2: start it. Transient SCM refusals (the database is locked, the
	// previous stop is still being recorded) are retried until the deadline.
	deadline = clk.now().Add(restartHelperStartWait)
	for {
		err := scm.Start()
		if err == nil {
			logf("start requested")
			break
		}
		if errors.Is(err, errServiceAlreadyRunning) {
			logf("service was already started by someone else")
			break
		}
		logf("start failed: %v — retrying", err)
		if clk.now().After(deadline) {
			return fmt.Errorf("could not start the service within %s: %w", restartHelperStartWait, err)
		}
		clk.sleep(restartHelperPoll)
	}

	// Phase 3: confirm the new binary is actually up. A service that reports
	// start pending (or running) and then drops back to stopped is a new binary
	// that will not stay up — SCM recovery and the update's own boot-attempt
	// rollback own that from here; the helper's job is to say so.
	last, sawPending := "", false
	for {
		st, err := scm.State()
		switch {
		case err != nil:
			logf("query service state: %v", err)
		case st != last:
			logf("service is %s", st)
			last = st
		}
		if err == nil {
			switch st {
			case "running":
				logf("restart complete — the service is running the swapped binary")
				return nil
			case "start pending":
				sawPending = true
			case "stopped":
				if sawPending {
					return errors.New("service stopped again right after starting — the new binary is not staying up; SCM recovery and the update rollback take it from here")
				}
			}
		}
		if clk.now().After(deadline) {
			return fmt.Errorf("service did not reach running within %s (last state %q)", restartHelperStartWait, last)
		}
		clk.sleep(restartHelperPoll)
	}
}
