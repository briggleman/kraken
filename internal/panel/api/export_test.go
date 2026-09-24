package api

import (
	"context"
	"time"
)

// ReconcileOnceForTest runs a single pass of the server reconciler, which the
// Panel otherwise only runs on a ticker started by cmd/panel. It lets a test in
// the black-box package assert what one pass does to the stored rows without
// racing a background loop.
func (s *Server) ReconcileOnceForTest(ctx context.Context) { s.reconcileOnce(ctx) }

// ReconcileNodesOnceForTest runs a single pass of the node reconciler — node
// health, then the pending removals owed to every node that answered — and,
// unlike the real loop, waits for those replays to settle before returning.
func (s *Server) ReconcileNodesOnceForTest(ctx context.Context) {
	s.reconcileNodesOnce(ctx)
	s.replays.wg.Wait()
}

// ReconcileNodesPassForTest is one pass exactly as the loop runs it — without
// waiting for the replays it starts — and WaitRemovalReplaysForTest waits for
// them afterwards.
func (s *Server) ReconcileNodesPassForTest(ctx context.Context) { s.reconcileNodesOnce(ctx) }
func (s *Server) WaitRemovalReplaysForTest()                    { s.replays.wg.Wait() }

// DownloadRedeemBurstForTest and LoginBurstForTest are the limiters' bursts, so
// a test in the black-box package can walk up to the edge of one without
// restating the number.
const (
	DownloadRedeemBurstForTest = downloadRedeemBurst
	LoginBurstForTest          = loginBurst
	LoginUserBurstForTest      = loginUserBurst
)

// AuditPruneBatchForTest is how many rows one retention pass deletes per
// statement, so a test can stage exactly one batch too many.
const AuditPruneBatchForTest = auditPruneBatch

// PruneAuditOnceForTest runs a single retention pass and reports how many audit
// entries it removed — the loop the daily job drives, without the ticker.
func (s *Server) PruneAuditOnceForTest(ctx context.Context) int64 { return s.pruneAuditOnce(ctx) }

// ExpireDownloadTokensForTest backdates every outstanding file-download token
// so a test can exercise the expiry branch without waiting out the 60-second
// TTL. It lives in a _test.go file, so it is compiled only into the test
// binary and is not part of the Panel.
func (s *Server) ExpireDownloadTokensForTest() {
	s.downloads.mu.Lock()
	defer s.downloads.mu.Unlock()
	for k, g := range s.downloads.grants {
		g.expires = time.Now().Add(-time.Second)
		s.downloads.grants[k] = g
	}
}

// RunDueSchedulesForTest runs a single pass of the scheduler — every enabled
// task whose next run is due — without the ticker cmd/panel starts it on.
func (s *Server) RunDueSchedulesForTest(ctx context.Context) { s.runDueSchedules(ctx) }
