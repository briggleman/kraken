package api

import (
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
// The named deadlines are checked by name, so a call site that drifts back to
// a literal shorter than its budget — schedule.go at 20s, say — fails here.
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
