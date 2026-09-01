package main

import (
	"fmt"
	"strings"
	"time"
)

// The SCM recovery policy `--service install` asserts, in one place so the
// status command's EXPECTED column cannot drift from what install writes.
// service_windows.go builds the mgr.RecoveryAction list from these; a status
// run that compared against its own copy of the numbers would report "in sync"
// against nothing at all.
var serviceRestartDelays = []time.Duration{
	5 * time.Second,
	30 * time.Second,
	60 * time.Second,
}

// serviceFailureResetPeriod is how long a service must run cleanly before the
// failure counter resets (SCM's reset= period).
const serviceFailureResetPeriod = 24 * time.Hour

// Exit codes for `--service status`, documented in --help and the Windows
// README so the healing instructions are scriptable: run status, and anything
// nonzero means run `--service install`.
const (
	exitServiceInSync      = 0 // every field matches what install would write
	exitServiceDrift       = 1 // installed, but at least one field differs
	exitServiceUnavailable = 2 // not installed, or the SCM could not be read
)

// serviceRecoveryStep is one SCM failure action, rendered OS-neutrally so the
// comparison below needs nothing from golang.org/x/sys.
type serviceRecoveryStep struct {
	Action string // "restart", "reboot", "run command", "none"
	Delay  time.Duration
}

// serviceFacts is a snapshot of the SCM settings `--service install` asserts —
// the actual one read back from the SCM, the expected one computed from this
// build's own policy. Plain data on purpose: the comparison is then a pure
// function that runs (and is tested) on every platform, while only the reading
// is Windows-only.
type serviceFacts struct {
	CommandLine      string
	StartType        string // "automatic", "manual", "disabled", "boot", "system"
	DelayedAutoStart bool
	Recovery         []serviceRecoveryStep
	ResetPeriod      time.Duration
	NonCrashFailures bool
}

// expectedServiceFacts is the configuration a `--service install` run from THIS
// invocation would leave behind. cmdLine is the caller's business because only
// the Windows build can escape it the way the SCM stores it (syscall.EscapeArg,
// via serviceCommandLine).
func expectedServiceFacts(cmdLine string) serviceFacts {
	steps := make([]serviceRecoveryStep, 0, len(serviceRestartDelays))
	for _, d := range serviceRestartDelays {
		steps = append(steps, serviceRecoveryStep{Action: "restart", Delay: d})
	}
	return serviceFacts{
		CommandLine:      cmdLine,
		StartType:        "automatic",
		DelayedAutoStart: true,
		Recovery:         steps,
		ResetPeriod:      serviceFailureResetPeriod,
		NonCrashFailures: true,
	}
}

// serviceConfigRow is one line of the status table: what the SCM holds, what
// install would write, and whether they agree.
type serviceConfigRow struct {
	Field    string
	Actual   string
	Expected string
	Match    bool
}

// formatRecovery renders a failure-action list as one comparable string. The
// order matters — SCM runs the actions in sequence, and a service left with
// only the first restart slot is exactly the #184 specimen.
func formatRecovery(steps []serviceRecoveryStep) string {
	if len(steps) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(steps))
	for _, s := range steps {
		parts = append(parts, fmt.Sprintf("%s after %s", s.Action, s.Delay))
	}
	return strings.Join(parts, ", ")
}

// compareServiceConfig is the whole decision: actual vs expected, field by
// field, in the order the status table prints them. Pure so it can be tested
// off Windows, where the SCM read that feeds it cannot run at all.
func compareServiceConfig(actual, expected serviceFacts) []serviceConfigRow {
	row := func(field, got, want string) serviceConfigRow {
		return serviceConfigRow{Field: field, Actual: got, Expected: want, Match: got == want}
	}
	yesNo := func(b bool) string {
		if b {
			return "yes"
		}
		return "no"
	}
	return []serviceConfigRow{
		row("command line", actual.CommandLine, expected.CommandLine),
		row("start type", actual.StartType, expected.StartType),
		row("delayed auto-start", yesNo(actual.DelayedAutoStart), yesNo(expected.DelayedAutoStart)),
		row("recovery actions", formatRecovery(actual.Recovery), formatRecovery(expected.Recovery)),
		row("failure-count reset", actual.ResetPeriod.String(), expected.ResetPeriod.String()),
		row("recovery on non-crash failures", yesNo(actual.NonCrashFailures), yesNo(expected.NonCrashFailures)),
	}
}

// serviceConfigDrift names the fields that differ, one line each. Empty means
// in sync, which is what decides the exit code; non-empty doubles as the summary
// an operator reads once the table has scrolled past.
func serviceConfigDrift(rows []serviceConfigRow) []string {
	var out []string
	for _, r := range rows {
		if !r.Match {
			out = append(out, fmt.Sprintf("%s: is %q, install would set %q", r.Field, r.Actual, r.Expected))
		}
	}
	return out
}

// formatServiceConfigTable renders the rows for the console: a match marker per
// row, the value once when the two sides agree, and both values on their own
// lines when they don't (these values include full command lines — squeezing
// them into aligned columns makes the interesting case unreadable).
func formatServiceConfigTable(rows []serviceConfigRow) []string {
	width := 0
	for _, r := range rows {
		if len(r.Field) > width {
			width = len(r.Field)
		}
	}
	out := make([]string, 0, len(rows)*2)
	for _, r := range rows {
		if r.Match {
			out = append(out, fmt.Sprintf("  ok     %-*s  %s", width, r.Field, r.Actual))
			continue
		}
		out = append(out,
			fmt.Sprintf("  DRIFT  %-*s  actual:   %s", width, r.Field, r.Actual),
			fmt.Sprintf("         %-*s  expected: %s", width, "", r.Expected))
	}
	return out
}
