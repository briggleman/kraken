// Package powerbudget holds the timing of the Agent's power actions — how long
// each can take inside one PowerAction RPC — so the Panel's deadlines are
// derived from the same numbers the Agent runs on instead of drifting away from
// them as literals.
//
// The worst cases come from the Docker daemon's own behaviour (moby v28.5.2),
// not from the Agent's code alone:
//
//   - ContainerStop does NOT return at the grace. When the grace runs out the
//     daemon kills the container and waits for it to exit — up to 10s on
//     Linux, 75s on Windows — then settles for a further ~2s + 2s.
//   - ContainerKill waits for the exit the same way, so KILL is not free.
//   - A START can wait for a removed container's name to come free twice: once
//     for the exited container ensureContainer removes, once more if the create
//     still loses the name race.
//
// Windows is the reference: its kill wait is the long one, and a Panel deadline
// that fits Windows fits Linux.
package powerbudget

import (
	"time"

	"github.com/briggleman/kraken/internal/shared/agentpb"
)

const (
	// StopGrace is the graceful-stop window the Agent hands ContainerStop.
	StopGrace = 30 * time.Second
	// StopConfirm bounds the Agent's post-stop check that the daemon agrees
	// the container is down. ContainerStop has already waited; this is a
	// sanity check, not a second wait.
	StopConfirm = 2 * time.Second
	// DaemonKillWaitLinux / DaemonKillWaitWindows are how long the daemon waits
	// for a killed container to exit before giving up on it.
	DaemonKillWaitLinux   = 10 * time.Second
	DaemonKillWaitWindows = 75 * time.Second
	// DaemonKillSettle is the daemon's extra settling after a kill (2s + 2s).
	DaemonKillSettle = 4 * time.Second
	// NameFreeWait is the longest one wait for a removed container's name to
	// come free can sleep (the Agent's containerNameFreeAttempts-1 delays).
	NameFreeWait = 7500 * time.Millisecond
	// StartPullBudget is how long a start waits for an image refresh before
	// going ahead on the local image.
	StartPullBudget = 8 * time.Second
	// DeadlineMargin is what the Panel adds on top of the Agent's worst case:
	// the round trip, a slow daemon call or two.
	DeadlineMargin = 30 * time.Second
	// deadlineStep rounds Panel deadlines up to a readable number.
	deadlineStep = 15 * time.Second
)

// killWait is the daemon's post-kill wait on the given container OS.
func killWait(os string) time.Duration {
	if os == "windows" {
		return DaemonKillWaitWindows
	}
	return DaemonKillWaitLinux
}

// Worst is the longest the Agent can spend inside one power RPC for action on a
// node running containerOS ("linux" or "windows").
//
//   - STOP: the grace, the daemon's kill-and-wait on expiry, its settle, and
//     the Agent's confirmation.
//   - KILL: the daemon's kill-and-wait and settle.
//   - START: two name waits and the image refresh budget.
//   - RESTART: a STOP, then a START.
func Worst(action agentpb.PowerAction, containerOS string) time.Duration {
	stop := StopGrace + killWait(containerOS) + DaemonKillSettle + StopConfirm
	start := 2*NameFreeWait + StartPullBudget
	switch action {
	case agentpb.PowerAction_POWER_ACTION_STOP:
		return stop
	case agentpb.PowerAction_POWER_ACTION_KILL:
		return killWait(containerOS) + DaemonKillSettle
	case agentpb.PowerAction_POWER_ACTION_START:
		return start
	case agentpb.PowerAction_POWER_ACTION_RESTART:
		return stop + start
	}
	return 0
}

// Deadline is the Panel's deadline for one power RPC: the Windows worst case
// plus DeadlineMargin, rounded up to the next 15 seconds.
func Deadline(action agentpb.PowerAction) time.Duration {
	d := Worst(action, "windows") + DeadlineMargin
	if r := d % deadlineStep; r != 0 {
		d += deadlineStep - r
	}
	return d
}
