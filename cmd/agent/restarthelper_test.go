package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// fakeSCM scripts the service states the helper observes: `before` until a
// Start succeeds, `after` from then on. The last state of a script repeats, so
// a service that never changes is a one-element script.
type fakeSCM struct {
	before    []string
	after     []string
	startErrs []error // returned by successive Start calls; nil = success
	started   bool
	starts    int
	qi        int
}

func (f *fakeSCM) State() (string, error) {
	seq := f.before
	if f.started {
		seq = f.after
	}
	if f.qi >= len(seq) {
		return seq[len(seq)-1], nil
	}
	s := seq[f.qi]
	f.qi++
	return s, nil
}

func (f *fakeSCM) Start() error {
	var err error
	if f.starts < len(f.startErrs) {
		err = f.startErrs[f.starts]
	}
	f.starts++
	if err == nil || errors.Is(err, errServiceAlreadyRunning) {
		f.started, f.qi = true, 0
	}
	return err
}

// fakeClock advances only when the helper sleeps, so a 3-minute wait costs the
// test nothing and the deadlines are still exercised exactly.
func fakeClock() (restartHelperClock, *time.Duration) {
	var elapsed time.Duration
	base := time.Date(2026, 9, 11, 13, 29, 30, 0, time.UTC)
	return restartHelperClock{
		now:   func() time.Time { return base.Add(elapsed) },
		sleep: func(d time.Duration) { elapsed += d },
	}, &elapsed
}

func runHelper(t *testing.T, scm *fakeSCM) (log []string, elapsed time.Duration, err error) {
	t.Helper()
	logf := func(format string, args ...any) { log = append(log, fmt.Sprintf(format, args...)) }
	clk, took := fakeClock()
	err = runRestartHelper(scm, logf, clk)
	return log, *took, err
}

func TestRestartHelperStopsStartsAndConfirmsRunning(t *testing.T) {
	scm := &fakeSCM{
		before: []string{"running", "stop pending", "stopped"},
		after:  []string{"start pending", "running"},
	}
	log, _, err := runHelper(t, scm)
	if err != nil {
		t.Fatalf("helper failed: %v\n%s", err, strings.Join(log, "\n"))
	}
	if scm.starts != 1 {
		t.Errorf("Start called %d times, want 1", scm.starts)
	}
	joined := strings.Join(log, "\n")
	for _, want := range []string{"service is stop pending", "service is stopped", "start requested", "restart complete"} {
		if !strings.Contains(joined, want) {
			t.Errorf("log missing %q:\n%s", want, joined)
		}
	}
}

// The old process is "running" until its graceful stop lands. That must read as
// "keep waiting", not as "already restarted" — otherwise the helper would exit
// happy while the service it was meant to relaunch goes down behind it.
func TestRestartHelperTreatsInitialRunningAsTheOldProcess(t *testing.T) {
	scm := &fakeSCM{
		before: []string{"running", "running", "running", "stopped"},
		after:  []string{"running"},
	}
	_, _, err := runHelper(t, scm)
	if err != nil {
		t.Fatalf("helper failed: %v", err)
	}
	if scm.starts != 1 {
		t.Errorf("Start called %d times, want 1 — initial running must not count as done", scm.starts)
	}
}

// An operator (or SCM recovery) that starts the service between our stop and
// our start is a success, and the helper must not fire a second Start into it.
func TestRestartHelperStandsDownWhenSomeoneElseRestarted(t *testing.T) {
	// A fast operator: the helper's polls see stop pending, then running — the
	// "stopped" instant in between is skipped over entirely.
	scm := &fakeSCM{before: []string{"stop pending", "running"}}
	log, _, err := runHelper(t, scm)
	if err != nil {
		t.Fatalf("helper failed: %v", err)
	}
	if scm.starts != 0 {
		t.Errorf("Start called %d times, want 0", scm.starts)
	}
	if !strings.Contains(strings.Join(log, "\n"), "restarted by someone else") {
		t.Errorf("log should say someone else restarted it:\n%s", strings.Join(log, "\n"))
	}
}

func TestRestartHelperAcceptsAlreadyRunningFromStart(t *testing.T) {
	scm := &fakeSCM{
		before:    []string{"stopped"},
		after:     []string{"running"},
		startErrs: []error{errServiceAlreadyRunning},
	}
	_, _, err := runHelper(t, scm)
	if err != nil {
		t.Fatalf("helper failed: %v", err)
	}
}

func TestRestartHelperRetriesTransientStartFailures(t *testing.T) {
	scm := &fakeSCM{
		before:    []string{"stopped"},
		after:     []string{"running"},
		startErrs: []error{errors.New("the service database is locked"), nil},
	}
	log, _, err := runHelper(t, scm)
	if err != nil {
		t.Fatalf("helper failed: %v", err)
	}
	if scm.starts != 2 {
		t.Errorf("Start called %d times, want 2", scm.starts)
	}
	if !strings.Contains(strings.Join(log, "\n"), "start failed: the service database is locked — retrying") {
		t.Errorf("log should record the retried failure:\n%s", strings.Join(log, "\n"))
	}
}

func TestRestartHelperGivesUpWhenTheServiceNeverStops(t *testing.T) {
	scm := &fakeSCM{before: []string{"running"}}
	_, elapsed, err := runHelper(t, scm)
	if err == nil || !strings.Contains(err.Error(), "did not reach stopped") {
		t.Fatalf("err = %v, want 'did not reach stopped'", err)
	}
	if elapsed < restartHelperStopWait {
		t.Errorf("gave up after %s, before the %s stop budget", elapsed, restartHelperStopWait)
	}
	if scm.starts != 0 {
		t.Errorf("Start called %d times on a service that never stopped", scm.starts)
	}
}

func TestRestartHelperGivesUpWhenStartKeepsFailing(t *testing.T) {
	scm := &fakeSCM{before: []string{"stopped"}}
	// Every Start fails — a script longer than the retry budget.
	for i := 0; i < 200; i++ {
		scm.startErrs = append(scm.startErrs, errors.New("access denied"))
	}
	_, _, err := runHelper(t, scm)
	if err == nil || !strings.Contains(err.Error(), "could not start the service") || !strings.Contains(err.Error(), "access denied") {
		t.Fatalf("err = %v, want 'could not start the service … access denied'", err)
	}
}

// A new binary that reaches start pending and then dies is the update's
// problem (boot attempts → rollback), but the helper must say so rather than
// report a restart it did not deliver.
func TestRestartHelperReportsANewBinaryThatWontStayUp(t *testing.T) {
	scm := &fakeSCM{
		before: []string{"stopped"},
		after:  []string{"start pending", "stopped"},
	}
	_, _, err := runHelper(t, scm)
	if err == nil || !strings.Contains(err.Error(), "stopped again right after starting") {
		t.Fatalf("err = %v, want 'stopped again right after starting'", err)
	}
}

// "stopped" read immediately after Start, before the SCM has flipped to start
// pending, is a race with the SCM, not a dead binary — the helper keeps waiting.
func TestRestartHelperToleratesStoppedBeforeStartPendingRegisters(t *testing.T) {
	scm := &fakeSCM{
		before: []string{"stopped"},
		after:  []string{"stopped", "start pending", "running"},
	}
	_, _, err := runHelper(t, scm)
	if err != nil {
		t.Fatalf("helper failed on the stopped→start pending race: %v", err)
	}
}

func TestRestartHelperGivesUpWhenRunningNeverArrives(t *testing.T) {
	scm := &fakeSCM{
		before: []string{"stopped"},
		after:  []string{"start pending"},
	}
	_, elapsed, err := runHelper(t, scm)
	if err == nil || !strings.Contains(err.Error(), "did not reach running") {
		t.Fatalf("err = %v, want 'did not reach running'", err)
	}
	if elapsed < restartHelperStartWait {
		t.Errorf("gave up after %s, before the %s start budget", elapsed, restartHelperStartWait)
	}
}
