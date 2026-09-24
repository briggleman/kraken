package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/docker/docker/api/types/container"

	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// removeAndAwait force-removes a container and does not return until its name
// is free for reuse.
//
// It removes by ID rather than by name on purpose: the name is the thing being
// raced for, and an ID cannot resolve to some container created after the
// inspect that produced it. A removal that finds nothing has already done its
// job and is not an error. Neither is a removal the daemon reports as already
// in progress — a 409 "removal of container … is already in progress" when two
// removals overlap. That is the slow-removal case the wait below exists for,
// so it waits instead of failing.
func removeAndAwait(ctx context.Context, c containerRemovalAPI, id, name string) error {
	if err := c.ContainerRemove(ctx, id, container.RemoveOptions{Force: true}); err != nil && !isNotFound(err) && !isNameConflict(err) {
		return fmt.Errorf("docker: remove %s: %w", name, err)
	}
	if name == "" {
		return nil // no name to reuse
	}
	if err := awaitNameFree(ctx, func(ctx context.Context) (bool, error) {
		_, err := c.ContainerInspect(ctx, name)
		switch {
		case err == nil:
			return true, nil
		case isNotFound(err):
			return false, nil
		default:
			return false, err
		}
	}, containerNameFreeAttempts, containerNameFreeDelay); err != nil {
		return fmt.Errorf("docker: waiting for %s to be reusable: %w", name, err)
	}
	return nil
}

// nameHolderAction is what ensureContainer does about a container that won the
// race for its server's container name.
type nameHolderAction int

const (
	// holderAdopt: the holder is this server's own container and it is live.
	// Another start got there first — a watchdog fast-restart, a double-clicked
	// Start — so the server is already ensured. Removing it would kill a healthy
	// game, read to its monitor as a crash, and set off a restart storm.
	holderAdopt nameHolderAction = iota
	// holderRemove: the holder is not running — an orphan from a removal that
	// never landed. Clear it and create again.
	holderRemove
	// holderRefuse: the holder is live and not labelled as this server's. The
	// Agent never kills what it cannot account for; it names it instead.
	holderRefuse
)

// decideNameHolder is the policy for a container found holding serverID's
// container name. It is pure so the three outcomes are testable without a
// daemon.
func decideNameHolder(info container.InspectResponse, serverID string) nameHolderAction {
	live := info.State != nil && (info.State.Running || info.State.Restarting || info.State.Paused)
	if !live {
		return holderRemove
	}
	if info.Config != nil && info.Config.Labels[labelServerID] == serverID {
		return holderAdopt
	}
	return holderRefuse
}

// resolveNameConflict handles a create that lost the race for name. adopted is
// true when a live container of this server's already holds it, and then there
// is nothing to create. Otherwise, on a nil error, the name is free and the
// caller creates again.
func resolveNameConflict(ctx context.Context, c containerRemovalAPI, serverID, name string) (adopted bool, err error) {
	info, err := c.ContainerInspect(ctx, name)
	if err != nil {
		if isNotFound(err) {
			return false, nil // freed in the meantime
		}
		return false, err
	}
	switch decideNameHolder(info, serverID) {
	case holderAdopt:
		return true, nil
	case holderRefuse:
		h := dataDirHolder{ID: info.ID, Name: name}
		return false, fmt.Errorf("container %s holds this server's container name and is running, but is not labelled as this server's; refusing to remove it — stop or rename it", h)
	}
	return false, removeAndAwait(ctx, c, info.ID, name)
}

// stopGrace is the graceful-stop window ContainerStop gets before the daemon
// kills the container.
const stopGrace = 30 * time.Second

// stopConfirmMargin is how long, after ContainerStop returns, the Agent waits
// for the daemon to report the container not running. ContainerStop has
// already waited out the grace and killed on expiry, so this is a
// confirmation, not a second grace: normally it returns at once.
const stopConfirmMargin = 10 * time.Second

// stopAndConfirm stops a container and does not report success until the
// daemon agrees it is no longer running. A container that is already gone is
// stopped — success, not an error. The confirmation is bounded by confirm and
// by ctx, whichever ends first.
//
// ContainerStop returning is not proof the container is down (#351): the
// Panel's pre-update stop trusted it, and an install pass then ran while a
// game container still held the data dir.
func stopAndConfirm(ctx context.Context, c containerOps, name string, opts container.StopOptions, confirm time.Duration) error {
	if err := c.ContainerStop(ctx, name, opts); err != nil {
		if isNotFound(err) {
			return nil
		}
		return err
	}
	wctx, cancel := context.WithTimeout(ctx, confirm)
	defer cancel()
	statusCh, errCh := c.ContainerWait(wctx, name, container.WaitConditionNotRunning)
	select {
	case <-statusCh:
		return nil
	case err := <-errCh:
		switch {
		case err == nil, isNotFound(err):
			return nil
		case ctx.Err() == nil && wctx.Err() != nil:
			return fmt.Errorf("docker: %s was still running %s after the stop returned", name, confirm)
		}
		return fmt.Errorf("docker: confirm %s stopped: %w", name, err)
	}
}

// PowerRPCBudget is the longest the Agent can spend inside one power RPC for
// action, built from its own timing constants. The Panel sizes its power
// deadlines to cover it, and its tests hold it to that: a deadline shorter
// than this cancels an action the Agent would have finished.
//
//   - START: one wait for a removed container's name to come free, plus the
//     image refresh budget (see refreshImageForStart).
//   - STOP: the stop grace, plus the post-stop confirmation.
//   - RESTART: a STOP, then a START.
//   - KILL: nothing the Agent waits on.
func PowerRPCBudget(action agentpb.PowerAction) time.Duration {
	start := containerNameFreeWait + startPullBudget
	stop := stopGrace + stopConfirmMargin
	switch action {
	case agentpb.PowerAction_POWER_ACTION_START:
		return start
	case agentpb.PowerAction_POWER_ACTION_STOP:
		return stop
	case agentpb.PowerAction_POWER_ACTION_RESTART:
		return stop + start
	}
	return 0
}
