package api

import (
	"context"
	"time"
)

// auditPruneBatch is how many rows one DELETE removes. Small enough that the
// statement is short even on a log nobody has ever pruned, large enough that
// catching up on years of entries is a handful of passes rather than thousands.
const auditPruneBatch = 5000

// auditPruneDelay is how long after startup the first pass runs. Not zero:
// boot is already busy (migrations, CA, catalog seed, the first reconcile), and
// a retention sweep is the least urgent thing the Panel does.
const auditPruneDelay = time.Minute

// StartAuditPruner launches the background loop that enforces the audit log's
// retention window: one pass shortly after startup, then one every interval.
// It returns immediately, and is a no-op when retention is 0 (keep forever).
//
// The log used to be append-only in the literal sense — nothing ever deleted a
// row — while the console told operators it was "retained 90 days". This is the
// job that makes the promise true; KRAKEN_AUDIT_RETENTION_DAYS sets the window
// and GET /audit reports it, so the two cannot drift apart again.
func (s *Server) StartAuditPruner(ctx context.Context, interval time.Duration) {
	if s.cfg.AuditRetentionDays <= 0 {
		s.logger.Info("audit retention: keeping every entry (KRAKEN_AUDIT_RETENTION_DAYS=0)")
		return
	}
	s.logger.Info("audit retention", "days", s.cfg.AuditRetentionDays)
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(auditPruneDelay):
		}
		s.pruneAuditOnce(ctx)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.pruneAuditOnce(ctx)
			}
		}
	}()
}

// pruneAuditOnce deletes everything older than the retention window, in
// batches, and returns how many entries went. One Info line per pass that
// removed something; a pass that found nothing to do — every pass after the
// first, on a Panel that has been running a while — says nothing at all.
func (s *Server) pruneAuditOnce(ctx context.Context) int64 {
	days := s.cfg.AuditRetentionDays
	if days <= 0 {
		return 0
	}
	before := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	var total int64
	for {
		n, err := s.store.PruneAudit(ctx, before, auditPruneBatch)
		total += n
		if err != nil {
			// Whatever was already deleted stays deleted, so report it: the
			// count is the only account of how far the pass got.
			s.logger.Warn("audit prune failed", "err", err, "removed", total)
			return total
		}
		if n < auditPruneBatch {
			break
		}
		select {
		case <-ctx.Done():
			return total
		default:
		}
	}
	if total > 0 {
		s.logger.Info("audit log pruned", "removed", total, "retention_days", days, "older_than", before.UTC().Format(time.RFC3339))
	} else {
		s.logger.Debug("audit log pruned", "removed", 0, "retention_days", days)
	}
	return total
}
