package api

import (
	"context"
	"fmt"
	"time"

	"github.com/briggleman/kraken/internal/panel/cron"
	"github.com/briggleman/kraken/internal/panel/store"
	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// StartScheduler launches the background loop that runs due scheduled tasks
// (restart / backup / command). It ticks on `interval` (which should be well
// under the 1-minute cron granularity to avoid drift) and runs until ctx is
// cancelled.
func (s *Server) StartScheduler(ctx context.Context, interval time.Duration) {
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.runDueSchedules(ctx)
			}
		}
	}()
}

func (s *Server) runDueSchedules(ctx context.Context) {
	tasks, err := s.store.ListSchedules(ctx)
	if err != nil {
		return
	}
	now := time.Now()
	for _, task := range tasks {
		if !task.Enabled {
			continue
		}
		sched, err := cron.Parse(task.Cron)
		if err != nil {
			continue // a malformed expression shouldn't have been stored; skip it
		}
		// First time we've seen this task (or after enabling): arm its next run.
		if task.NextRunAt == nil {
			next := sched.Next(now)
			task.NextRunAt = &next
			_ = s.store.UpdateSchedule(ctx, task)
			continue
		}
		if now.Before(*task.NextRunAt) {
			continue
		}
		slot := *task.NextRunAt

		runErr := s.runScheduleAction(ctx, task)
		task.LastRunAt = &now
		task.LastError = ""
		if runErr != nil {
			task.LastError = runErr.Error()
			s.logger.Warn("scheduler: task failed", "schedule", task.ID, "server", task.ServerID, "action", task.Action, "err", runErr)
		} else {
			s.logger.Info("scheduler: ran task", "schedule", task.ID, "server", task.ServerID, "action", task.Action)
		}
		// Advance from the slot we just ran, then fast-forward past any slots that
		// already elapsed (e.g. the action outlasted the interval, or the Panel was
		// down) so we schedule the next *future* slot rather than firing back-to-back.
		next := sched.Next(slot)
		for !next.IsZero() && !next.After(time.Now()) {
			next = sched.Next(next)
		}
		if next.IsZero() {
			task.NextRunAt = nil
		} else {
			task.NextRunAt = &next
		}
		_ = s.store.UpdateSchedule(ctx, task)
	}
}

// runScheduleAction performs a single scheduled task against its server's Agent.
func (s *Server) runScheduleAction(ctx context.Context, task *store.ScheduledTask) error {
	sv, err := s.store.GetServer(ctx, task.ServerID)
	if err != nil {
		return fmt.Errorf("load server: %w", err)
	}
	// A restart is stop-then-start on the Agent, so it boots the game: it is
	// asked the same questions as an operator's start (checkStartable), before
	// the node is contacted. The refusal's sentence becomes the schedule's
	// last_error, which is where an operator looks when a restart did not
	// happen.
	if task.Action == store.ScheduleRestart {
		if err := s.checkScheduledRestart(ctx, sv); err != nil {
			return err
		}
	}
	// A restore owns the data dir until it settles (#361): a 04:00 backup would
	// archive a half-swapped tree and its retention pass could evict the very
	// archive being read, on the node and the mirror. Every other action skips
	// with the reason in last_error, as a refused restart does.
	if s.restoreInProgress(sv) {
		return fmt.Errorf("a backup restore is in progress, so the scheduled %s was skipped", task.Action)
	}
	node, err := s.store.GetNode(ctx, sv.NodeID)
	if err != nil {
		return fmt.Errorf("load node: %w", err)
	}
	client, err := s.nodes.Client(node.DialTarget())
	if err != nil {
		return fmt.Errorf("connect agent: %w", err)
	}

	switch task.Action {
	case store.ScheduleRestart:
		// Held across the Power call and the write-back below, so a restore
		// cannot begin on a crashed row while the Agent restarts it.
		release, refusal := s.claimStart(sv.ID)
		if refusal != nil {
			return fmt.Errorf("a backup restore is in progress, so the scheduled restart was skipped")
		}
		defer release()
		// Re-push the spec first so the Agent can recreate the container even if it
		// lost its in-memory spec after a restart (mirrors the manual power path).
		if sp, serr := s.store.GetSpec(ctx, sv.SpecID); serr == nil {
			s.rePushServerSpec(ctx, client, sv, sp)
		}
		// The same deadline as an operator's restart: the Agent's stop grace
		// alone is longer than the 20s this used to allow.
		cctx, cancel := context.WithTimeout(ctx, scheduledRestartTimeout)
		defer cancel()
		resp, err := client.PowerAction(cctx, &agentpb.PowerActionRequest{ServerId: sv.ID, Action: agentpb.PowerAction_POWER_ACTION_RESTART})
		if err != nil {
			return fmt.Errorf("restart: %w", err)
		}
		// Written onto a fresh read, and not at all over a restore: one that
		// began while the restart was in flight owns the row now, and writing
		// the Agent's `running` over `restoring` would lift its start gate.
		fresh, ferr := s.store.GetServer(ctx, sv.ID)
		if ferr != nil {
			return nil
		}
		if s.restoreInProgress(fresh) {
			s.logger.Warn("scheduler: a restore began during the restart; leaving the row to it", "server", sv.ID)
			return nil
		}
		fresh.State = storeStateFromAgent(resp.State)
		_ = s.store.UpdateServer(ctx, fresh)
		return nil

	case store.ScheduleBackup:
		cctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		defer cancel()
		name := "scheduled-" + time.Now().UTC().Format("2006-01-02-150405")
		// Same glob resolution as the manual path — the Panel drives every
		// backup, so there is exactly one place the policy is applied.
		if _, err := client.CreateBackup(cctx, s.backupRequestFor(ctx, sv, name)); err != nil {
			return fmt.Errorf("backup: %w", err)
		}
		return nil

	case store.ScheduleCommand:
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		if _, err := client.SendCommand(cctx, &agentpb.SendCommandRequest{ServerId: sv.ID, Command: task.Command}); err != nil {
			return fmt.Errorf("command: %w", err)
		}
		return nil

	case store.ScheduleReplicate:
		cctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
		defer cancel()
		resp, err := client.ReplicateBackups(cctx, &agentpb.ReplicateBackupsRequest{ServerId: sv.ID, Slug: s.serverSlug(ctx, sv)})
		if err != nil {
			return fmt.Errorf("replicate: %w", err)
		}
		s.logger.Info("scheduler: replicated backups", "server", sv.ID, "mirrored", resp.Mirrored, "skipped", resp.Skipped)
		return nil

	default:
		return fmt.Errorf("unknown action %q", task.Action)
	}
}

// checkScheduledRestart reports why a scheduled restart of sv must not run, or
// nil when it may. Beyond checkStartable, the server has to be in a state a
// restart is for:
//
//   - running, the ordinary case;
//   - starting, because a server stuck there (a ready line that never matches)
//     is exactly what a nightly restart should cycle;
//   - crashed, because reviving a server the watchdog gave up on is behaviour
//     operators rely on.
//
// It is refused on an offline server — one someone stopped, which the Agent's
// stop-then-start would quietly start again — and on every other state
// (stopping, installing, install_failed, and any state added later), since an
// allow-list cannot start something by default.
func (s *Server) checkScheduledRestart(ctx context.Context, sv *store.Server) error {
	switch sv.State {
	case store.StateRunning, store.StateStarting, store.StateCrashed:
	case store.StateOffline:
		return fmt.Errorf("server is offline, so the scheduled restart was skipped — a restart would start a server someone had stopped")
	default:
		return fmt.Errorf("server is %s, so the scheduled restart was skipped", sv.State)
	}
	if refusal := s.checkStartable(ctx, sv, agentpb.PowerAction_POWER_ACTION_RESTART); refusal != nil {
		return refusal
	}
	return nil
}
