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
	node, err := s.store.GetNode(ctx, sv.NodeID)
	if err != nil {
		return fmt.Errorf("load node: %w", err)
	}
	client, err := s.nodes.Client(node.Address)
	if err != nil {
		return fmt.Errorf("connect agent: %w", err)
	}

	switch task.Action {
	case store.ScheduleRestart:
		// Re-push the spec first so the Agent can recreate the container even if it
		// lost its in-memory spec after a restart (mirrors the manual power path).
		if sp, serr := s.store.GetSpec(ctx, sv.SpecID); serr == nil {
			s.rePushServerSpec(ctx, client, sv, sp)
		}
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		resp, err := client.PowerAction(cctx, &agentpb.PowerActionRequest{ServerId: sv.ID, Action: agentpb.PowerAction_POWER_ACTION_RESTART})
		if err != nil {
			return fmt.Errorf("restart: %w", err)
		}
		sv.State = storeStateFromAgent(resp.State)
		_ = s.store.UpdateServer(ctx, sv)
		return nil

	case store.ScheduleBackup:
		cctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		defer cancel()
		name := "scheduled-" + time.Now().UTC().Format("2006-01-02-150405")
		if _, err := client.CreateBackup(cctx, &agentpb.CreateBackupRequest{ServerId: sv.ID, Name: name, Slug: s.serverSlug(ctx, sv)}); err != nil {
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
