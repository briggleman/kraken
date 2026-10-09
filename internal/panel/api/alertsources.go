package api

import (
	"context"
	"time"

	"github.com/briggleman/kraken/internal/panel/alerts"
	"github.com/briggleman/kraken/internal/panel/cluster"
	"github.com/briggleman/kraken/internal/panel/store"
	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// The push-alert sources (#348, docs/design/push-alerts.md). Each hooks the
// place where the Panel already learns the fact — the reconcilers' writes, the
// backup paths' answers — and never adds a poll of its own where one already
// reads the data. The one new poll is the backup watcher, because a backup's
// outcome is otherwise read by nobody.
//
// The in-memory diffs run whether or not a relay is configured; they cost a
// map lookup per poll and keep their baselines warm. The backup watcher, which
// costs a round trip to the node every 15 seconds, runs only when there is a
// relay to tell.

func serverRef(sv *store.Server) alerts.ServerRef {
	return alerts.ServerRef{ID: sv.ID, Name: sv.Name, OwnerID: sv.OwnerID, NodeID: hostNodeID(sv)}
}

// observeServerStatus runs the per-poll diffs for one live server: the
// watchdog's restart count and the roster. It runs on every poll, before the
// reconciler decides whether the row needs writing, because both can change
// while everything the row stores stays the same.
func (s *Server) observeServerStatus(sv *store.Server, newState store.ServerState, status *agentpb.ServerStatus) {
	now := time.Now()
	ref := serverRef(sv)
	v := s.watchdogs.Observe(sv.ID, status.GetWatchdogRestarts(), newState == store.StateCrashed, now)
	switch {
	case v.CrashLoop:
		s.alerts.Dispatch(alerts.CrashLoop(ref, v.InWindow, now))
	case v.Healed:
		s.alerts.Dispatch(alerts.WatchdogRestart(ref, now))
	}
	r := alerts.Roster{Running: newState == store.StateRunning}
	if ls := status.GetLastStats(); ls != nil && ls.GetPlayersKnown() {
		r.Known, r.Count = true, ls.GetPlayers()
		for _, p := range ls.GetOnlinePlayers() {
			if p.GetName() != "" {
				r.Names = append(r.Names, p.GetName())
			}
		}
	}
	if e, ok := s.rosters.Observe(ref, r, now); ok {
		s.alerts.Dispatch(e)
	}
}

// noteServerCrashed sends server_crashed for a server the reconciler has just
// written from a live state into crashed. Called after the write lands, so a
// write that failed — and will be tried again on the next pass — is not
// announced twice. An operator stop or kill never comes through here: it lands
// offline, not crashed.
func (s *Server) noteServerCrashed(sv *store.Server, status *agentpb.ServerStatus) {
	s.alerts.Dispatch(alerts.ServerCrashed(serverRef(sv), status.GetLastExitCode(), status.GetExitCodeKnown(), time.Now()))
}

// finishAlertPass ends a reconcile pass: it sends the player joins that were
// held for their server's one-minute interval, and forgets the servers that no
// longer exist.
func (s *Server) finishAlertPass(listed map[string]bool) {
	for _, e := range s.rosters.Flush(time.Now()) {
		s.alerts.Dispatch(e)
	}
	s.watchdogs.Retain(listed)
	s.rosters.Retain(listed)
}

// observeNodeStatus is told every status a probe finds for a node, and sends
// node_offline or node_partial on a fall from online or cordoned. It is called
// where the status is decided — every probe of a node goes through
// reconcileNode, and a tunnel that drops marks its node offline directly — so
// a fall is caught whichever path noticed it, once.
func (s *Server) observeNodeStatus(n *cluster.Node, next cluster.NodeStatus) {
	if !s.nodeFalls.Observe(n.ID, next) {
		return
	}
	now := time.Now()
	switch next {
	case cluster.NodeOffline:
		s.alerts.Dispatch(alerts.NodeOffline(n.ID, n.Name, now))
	case cluster.NodePartial:
		s.alerts.Dispatch(alerts.NodePartial(n.ID, n.Name, now))
	}
}

// Backup watching. Every backup the Panel starts answers PENDING and archives
// in the background; nothing reads its outcome but an operator who opens the
// list. The watcher follows one backup until it reads ready or failed.
var (
	backupWatchInterval = 15 * time.Second
	// backupWatchTimeout is the retire's window for a final backup: a backup
	// still PENDING after it is not followed further, and is not called failed.
	backupWatchTimeout = func() time.Duration { return finalBackupTimeout }
)

// watchBackup follows a backup the Panel just started and sends backup_failed
// with the node's reason if it lands FAILED. Ready, vanished or still pending
// at the deadline all end the watch quietly. It returns at once; the watch runs
// on its own goroutine.
func (s *Server) watchBackup(sv *store.Server, b *agentpb.BackupInfo, kind alerts.BackupKind) {
	if !s.alerts.Enabled() || b == nil {
		return
	}
	ref := serverRef(sv)
	switch b.GetState() {
	case agentpb.BackupState_BACKUP_STATE_FAILED:
		s.alerts.Dispatch(alerts.BackupFailed(ref, kind, b.GetError(), time.Now()))
		return
	case agentpb.BackupState_BACKUP_STATE_PENDING:
	default:
		return
	}
	snapshot := *sv
	go s.followBackup(&snapshot, ref, b.GetId(), kind)
}

func (s *Server) followBackup(sv *store.Server, ref alerts.ServerRef, backupID string, kind alerts.BackupKind) {
	ctx, cancel := context.WithTimeout(context.Background(), backupWatchTimeout())
	defer cancel()
	slug := s.serverSlug(ctx, sv)
	t := time.NewTicker(backupWatchInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		b, found, err := s.backupState(ctx, sv, slug, backupID)
		if err != nil {
			continue // a node that blinked; the deadline decides
		}
		if !found {
			return // deleted, or an Agent restart lost it: nobody to blame
		}
		switch b.GetState() {
		case agentpb.BackupState_BACKUP_STATE_PENDING:
			continue
		case agentpb.BackupState_BACKUP_STATE_FAILED:
			s.alerts.Dispatch(alerts.BackupFailed(ref, kind, b.GetError(), time.Now()))
		}
		return
	}
}

// backupState reads one backup from the node the server is on now (re-resolved
// every time, so a tunnel that reconnected is used through its new session).
func (s *Server) backupState(ctx context.Context, sv *store.Server, slug, backupID string) (*agentpb.BackupInfo, bool, error) {
	node, err := s.store.GetNode(ctx, hostNodeID(sv))
	if err != nil {
		return nil, false, err
	}
	client, err := s.nodes.Client(node.DialTarget())
	if err != nil {
		return nil, false, err
	}
	lctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	list, err := client.ListBackups(lctx, &agentpb.ListBackupsRequest{ServerId: sv.ID, Slug: slug})
	if err != nil {
		return nil, false, err
	}
	for _, b := range list.GetBackups() {
		if b.GetId() == backupID {
			return b, true, nil
		}
	}
	return nil, false, nil
}
