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

// SetReconcileWriteHookForTest runs fn inside a reconcile pass, after the
// Agent answered and before the row is re-read to be written. The returned
// func clears it.
func SetReconcileWriteHookForTest(fn func(serverID string)) func() {
	reconcileWriteHook = fn
	return func() { reconcileWriteHook = nil }
}

// OperationHeldForTest reports the retire, revive or delete holding a server,
// or "" — what a test waits on instead of sleeping after a retire lands.
func (s *Server) OperationHeldForTest(serverID string) string { return s.restores.opHolding(serverID) }

// SetFinalBackupTimingForTest shortens the final backup's poll interval and
// deadline, so the wait loop and its timeout run in milliseconds. The returned
// func restores them.
func SetFinalBackupTimingForTest(poll, timeout time.Duration) func() {
	oldPoll, oldTimeout := finalBackupPollInterval, finalBackupTimeout
	finalBackupPollInterval, finalBackupTimeout = poll, timeout
	return func() { finalBackupPollInterval, finalBackupTimeout = oldPoll, oldTimeout }
}

// SetRestoreClaimHookForTest runs fn just after a start, restart or reinstall
// claims the server (see claimStart), so a test can attempt a restore exactly
// there. The returned func clears it.
func SetRestoreClaimHookForTest(fn func(serverID string)) func() {
	restoreClaimHook = fn
	return func() { restoreClaimHook = nil }
}

// SetProvisionHookForTest runs fn first thing in provision, before the install
// has written anything — a test blocks there to read the install log in the gap
// between a handler's 202 and the install's first line (#387). The returned
// func clears it. Unlike the plain-var hooks above, this one is an atomic
// pointer: install goroutines from earlier tests may still be reading it.
func SetProvisionHookForTest(fn func(serverID string)) func() {
	provisionHook.Store(&fn)
	return func() { provisionHook.Store(nil) }
}
