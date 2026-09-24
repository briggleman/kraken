package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/briggleman/kraken/internal/agent"
	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// TestPowerDeadlinesCoverTheAgentsBudget holds every Panel power deadline to
// the longest the Agent can spend inside that action, on either container OS
// — the Agent's own figure (agent.PowerRPCBudget), built from the values it
// actually runs on plus the daemon's kill waits. A deadline shorter than that
// cancels an action the Agent would have finished; the direct START (15s) and
// the scheduled restart (20s, less than the stop grace alone) used to.
//
// This checks the deadline values — powerTimeout and the named deadlines
// built from it. TestPowerCallSitesUseTheNamedDeadlines checks that every
// call site actually uses one of them.
func TestPowerDeadlinesCoverTheAgentsBudget(t *testing.T) {
	start := agentpb.PowerAction_POWER_ACTION_START
	stop := agentpb.PowerAction_POWER_ACTION_STOP
	restart := agentpb.PowerAction_POWER_ACTION_RESTART
	kill := agentpb.PowerAction_POWER_ACTION_KILL

	deadlines := []struct {
		name     string
		deadline time.Duration
		action   agentpb.PowerAction
	}{
		{"power handler START", powerTimeout(start), start},
		{"power handler STOP", powerTimeout(stop), stop},
		{"power handler RESTART", powerTimeout(restart), restart},
		{"power handler KILL", powerTimeout(kill), kill},
		{"scheduled restart (schedule.go)", scheduledRestartTimeout, restart},
		{"pre-update stop (updateThenStart)", preUpdateStopTimeout, stop},
		{"post-update start (updateThenStart)", postUpdateStartTimeout, start},
	}
	for _, d := range deadlines {
		for _, os := range []string{"windows", "linux"} {
			if budget := agent.PowerRPCBudget(d.action, os); d.deadline < budget {
				t.Errorf("%s: deadline %v is shorter than the Agent's %s worst case %v", d.name, d.deadline, os, budget)
			}
		}
	}

	// The budgets are real numbers — a refactor that zeroed them would make
	// every check above pass vacuously. Windows is the long one.
	for _, a := range []agentpb.PowerAction{start, stop, restart, kill} {
		if agent.PowerRPCBudget(a, "windows") <= 0 {
			t.Errorf("%v: empty budget", a)
		}
	}
	if agent.PowerRPCBudget(stop, "windows") <= agent.PowerRPCBudget(stop, "linux") {
		t.Error("the Windows stop budget should exceed Linux's (the daemon's 75s kill wait)")
	}
}

// TestPowerCallSitesUseTheNamedDeadlines — every PowerAction RPC the Panel
// sends must be bounded by powerTimeout or one of the named deadlines, never a
// literal: a literal is exactly how the scheduled restart sat at 20s while the
// Agent's stop grace alone was 30s. It reads this package's source, finds
// each `.PowerAction(` call, and requires the nearest context.WithTimeout above
// it to use a named deadline.
func TestPowerCallSitesUseTheNamedDeadlines(t *testing.T) {
	named := []string{"powerTimeout(", "scheduledRestartTimeout", "preUpdateStopTimeout", "postUpdateStartTimeout"}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(string(b), "\n")
		for i, line := range lines {
			if !strings.Contains(line, ".PowerAction(") || strings.Contains(line, "func ") {
				continue
			}
			calls++
			timeout := ""
			for j := i; j >= 0 && j >= i-8; j-- {
				if strings.Contains(lines[j], "context.WithTimeout(") {
					timeout = lines[j]
					break
				}
			}
			ok := false
			for _, n := range named {
				ok = ok || strings.Contains(timeout, n)
			}
			if !ok {
				t.Errorf("%s:%d: PowerAction bounded by %q, not a named power deadline", f, i+1, strings.TrimSpace(timeout))
			}
		}
	}
	if calls < 5 {
		t.Errorf("found only %d PowerAction call sites; the scan is not seeing the code", calls)
	}
}
