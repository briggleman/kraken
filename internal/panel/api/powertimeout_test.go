package api

import (
	"testing"

	"github.com/briggleman/kraken/internal/agent"
	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// TestPowerTimeoutsCoverTheAgentsBudget holds every Panel power deadline to the
// longest the Agent can spend inside that action: stop grace + confirmation for
// a stop, name wait + image-refresh budget for a start, both for a restart. A
// deadline shorter than that cancels an action the Agent would have finished —
// which is what the direct START (15s) and the scheduled restart (20s, less
// than the stop grace alone) used to do.
func TestPowerTimeoutsCoverTheAgentsBudget(t *testing.T) {
	for _, action := range []agentpb.PowerAction{
		agentpb.PowerAction_POWER_ACTION_START,
		agentpb.PowerAction_POWER_ACTION_STOP,
		agentpb.PowerAction_POWER_ACTION_RESTART,
		agentpb.PowerAction_POWER_ACTION_KILL,
	} {
		budget := agent.PowerRPCBudget(action)
		if got := powerTimeout(action); got < budget {
			t.Errorf("%v: Panel deadline %v is shorter than the Agent's worst case %v", action, got, budget)
		}
	}
	// The scheduler's restart goes through the same deadline; this is the one
	// that used to be 20s.
	if got, want := powerTimeout(agentpb.PowerAction_POWER_ACTION_RESTART), agent.PowerRPCBudget(agentpb.PowerAction_POWER_ACTION_RESTART); got < want {
		t.Errorf("scheduled restart deadline %v < Agent budget %v", got, want)
	}
	// Sanity: the budgets are real numbers, not zero — a refactor that zeroed
	// them would make this test pass vacuously.
	if agent.PowerRPCBudget(agentpb.PowerAction_POWER_ACTION_RESTART) <= agent.PowerRPCBudget(agentpb.PowerAction_POWER_ACTION_STOP) ||
		agent.PowerRPCBudget(agentpb.PowerAction_POWER_ACTION_START) == 0 {
		t.Error("PowerRPCBudget looks empty")
	}
}
