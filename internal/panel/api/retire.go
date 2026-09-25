package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/briggleman/kraken/internal/panel/cluster"
	"github.com/briggleman/kraken/internal/panel/scheduler"
	"github.com/briggleman/kraken/internal/panel/store"
	"github.com/briggleman/kraken/internal/shared/agentpb"
	"github.com/briggleman/kraken/internal/shared/spec"
)

// Retire, revive and permanent delete (#360).
//
// Servers are far more often brought back than gone for good, so the delete
// button retires: the server keeps its id, its config and its backups, and
// loses only what costs space and capacity — its containers, its world, its
// memory and ports, its DNS records and forwards. Its schedules are switched
// off, not deleted. The row stays, in state `retired`, on no node.
//
//   - Retire: stop → final backup (on by default; bounded; skipped and said so
//     when the node is unreachable, or failed and said so) → remove on the node
//     with delete_data → release, clean up, switch schedules off → `retired`.
//     A node that cannot be reached does not stop it: the removal is queued as
//     a pending removal (#354) exactly as a delete's was, holding the
//     allocation until it lands.
//   - Revive: place the server again (its old node by default, its old ports
//     where they are free), run the install pass, optionally restore a backup,
//     switch back on the schedules the retire switched off, optionally start.
//   - Permanent delete: only for a retired server. The node deletes the world
//     that is left and the server's archives where they are its own, and the
//     row goes with its schedules.
//
// The retire runs as a background job: a stop and a backup can take minutes.
// It holds the server in the operation lock (restorejobs.go) for its whole run,
// and its phase is written to the row as it moves, so a Panel that restarts
// mid-retire leaves a row the reconciler can settle (settleOrphanedRetire).

// The machine-readable codes the retire model answers with.
const (
	codeServerRetired    = "server_retired"     // 409: the server is retired; revive it first
	codeServerNotRetired = "server_not_retired" // 409: only a retired server can be revived or deleted permanently
	codeRemovalPending   = "removal_pending"    // 409: a removal is still owed for this server on a node
)

const (
	// retireDeadline bounds a whole retire: a stop, a final backup and a
	// removal, the backup being by far the longest.
	retireDeadline = 45 * time.Minute
	// finalBackupName is what the final backup is called in the backup list.
	finalBackupName = "final-before-retire"
)

// stopRefusalHint ends the reason a retire is abandoned with when the node
// answered but refused the stop the final backup needs. Without a final backup
// the stop is not needed — the removal takes the container down regardless —
// so that is the way through, and the operator is told so.
const stopRefusalHint = " — to retire it without a final backup, retire again with the final backup unchecked (final_backup: false)"

// finalBackupPollInterval is how often the retire asks the node whether the
// final backup has finished, and finalBackupTimeout bounds it, as an install
// pass is bounded. Variables so a test can shorten them.
var (
	finalBackupPollInterval = 2 * time.Second
	finalBackupTimeout      = 30 * time.Minute
)

type retireRequest struct {
	// FinalBackup takes a backup after the stop and before the removal. Absent
	// means true: the checkbox is on by default, and a client that says nothing
	// has not asked to lose the world.
	FinalBackup *bool `json:"final_backup,omitempty"`
}

// handleRetireServer starts a retire and answers at once with the server view:
// 202, state `retiring`, and a `retire` block whose phase the job moves.
//
// POST /servers/{id}/retire. server.delete.
func (s *Server) handleRetireServer(w http.ResponseWriter, r *http.Request) {
	var req retireRequest
	if err := decodeJSON(r, &req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	finalBackup := req.FinalBackup == nil || *req.FinalBackup
	backupState := store.FinalBackupOff
	if finalBackup {
		backupState = store.FinalBackupRequested
	}
	ctx := r.Context()
	sv, err := s.store.GetServer(ctx, chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "server not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not get server")
		return
	}
	if !s.authorizeServer(w, ctx, sv) {
		return
	}
	if refusal := retireRefusalFor(s, sv); refusal != nil {
		refusal.write(w)
		return
	}
	switch held := s.restores.holdOp(sv.ID, opRetire); held {
	case "":
	case codeServerRestoring:
		writeCoded(w, http.StatusConflict, codeServerRestoring,
			"a backup restore is in progress for this server; retire it once the restore finishes")
		return
	case codeServerBusy:
		writeCoded(w, http.StatusConflict, codeServerBusy,
			"a start, restart or reinstall is in progress for this server; retire it once that finishes")
		return
	default:
		writeCoded(w, http.StatusConflict, codeServerBusy, "this server is already being "+held+"d")
		return
	}
	// Re-read under the hold: the row loaded above may be stale, and whatever
	// finished in between must be seen before the retire writes over it.
	fresh, err := s.store.GetServer(ctx, sv.ID)
	if err != nil {
		s.restores.releaseOp(sv.ID)
		writeError(w, http.StatusInternalServerError, "could not get server")
		return
	}
	if refusal := retireRefusalFor(s, fresh); refusal != nil {
		s.restores.releaseOp(sv.ID)
		refusal.write(w)
		return
	}
	fresh.Retire = &store.ServerRetire{
		Phase: store.RetirePhaseStopping, PrevState: fresh.State,
		FinalBackup: backupState, StartedAt: time.Now().UTC(),
	}
	fresh.State = store.StateRetiring
	fresh.RetireNote = ""
	if err := s.store.UpdateServer(ctx, fresh); err != nil {
		s.restores.releaseOp(sv.ID)
		writeError(w, http.StatusInternalServerError, "could not update server state")
		return
	}
	s.logger.Info("server retire started", "server", fresh.ID, "name", fresh.Name, "final_backup", finalBackup)
	go s.runRetire(fresh.ID)
	writeJSON(w, http.StatusAccepted, s.serverResponse(fresh))
}

// retireRefusalFor says why sv cannot be retired now, or nil. The operation
// hold is checked by the caller (holdOp); this reads the row and the restore
// registry.
func retireRefusalFor(s *Server, sv *store.Server) *opRefusal {
	switch {
	case sv.State == store.StateRetired:
		return &opRefusal{http.StatusConflict, codeServerRetired, "this server is already retired"}
	case sv.Retire != nil || sv.State == store.StateRetiring:
		// The row's record of a retire; the registry's hold is holdOp's to
		// report, and is this handler's own on the re-check.
		return &opRefusal{http.StatusConflict, codeServerBusy, "this server is already being retired"}
	case s.restoreInProgress(sv):
		return &opRefusal{http.StatusConflict, codeServerRestoring,
			"a backup restore is in progress for this server; retire it once the restore finishes"}
	case sv.State == store.StateInstalling:
		// The install pass owns the row until it settles, and would write its
		// outcome over `retired` — and a pass writing the tree is the wrong
		// thing to take a final backup of.
		return &opRefusal{http.StatusConflict, codeServerBusy,
			"this server is installing; retire it once the install finishes"}
	}
	return nil
}

// finalOutcome is where a final-backup attempt got to: ready, failed or
// skipped (store.FinalBackup*), why, and the archive once there is one.
type finalOutcome struct {
	status, note, id string
}

// retireStop stops the server for the final backup. reachable is false when
// the node did not answer at all (Unavailable, or the Panel's own deadline).
func retireStop(ctx context.Context, client agentpb.NodeServiceClient, serverID string) (stopped, reachable bool, why string) {
	pctx, cancel := context.WithTimeout(ctx, powerTimeout(agentpb.PowerAction_POWER_ACTION_STOP))
	defer cancel()
	_, err := client.PowerAction(pctx, &agentpb.PowerActionRequest{ServerId: serverID, Action: agentpb.PowerAction_POWER_ACTION_STOP})
	switch code := status.Code(err); {
	case err == nil:
		return true, true, ""
	case code == codes.Unavailable || code == codes.DeadlineExceeded:
		return false, false, status.Convert(err).Message()
	default:
		return false, true, status.Convert(err).Message()
	}
}

// runRetire is the retire job. It owns the row until it writes `retired` (or
// abandons the retire and puts the row back), and releases the hold after.
//
// The rule it keeps, above all: **while the node answers, nothing is removed
// unless the final backup that was asked for is READY.** A backup that fails,
// or does not finish in time (its archiver may still be writing), abandons the
// retire. A backup that could not even be tried — the node did not answer the
// stop or the backup — is tried again once the removal is due, if the node
// answers then; only a node that is unreachable at that moment gets its
// removal queued without a final backup, and the note says so.
func (s *Server) runRetire(serverID string) {
	ctx, cancel := context.WithTimeout(context.Background(), retireDeadline)
	defer cancel()
	defer s.restores.releaseOp(serverID)

	sv, err := s.store.GetServer(ctx, serverID)
	if err != nil || sv.Retire == nil {
		s.logger.Error("retire: could not load the server", "server", serverID, "err", err)
		return // the reconciler settles the row it left
	}
	prev := sv.Retire.PrevState
	wantBackup := sv.Retire.FinalBackup == store.FinalBackupRequested
	var notes []string
	node, err := s.store.GetNode(ctx, sv.NodeID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		node = nil
		notes = append(notes, "its node no longer exists, so nothing was removed on a node")
	case err != nil:
		s.abandonRetire(ctx, serverID, "could not load the server's node ("+err.Error()+"); nothing was changed", false)
		return
	}
	client := s.retireClient(ctx, node)
	live := client != nil

	// Stop. Only a server that may be running needs it; the removal would stop
	// it anyway, but the final backup must not archive a world mid-save.
	stopped := !mayBeRunning(prev)
	stopWhy := ""
	if live && !stopped {
		stopped, live, stopWhy = retireStop(ctx, client, sv.ID)
	}

	outcome := finalOutcome{status: store.FinalBackupOff}
	if wantBackup {
		if err := s.setRetirePhase(ctx, serverID, store.RetirePhaseBackingUp); err != nil {
			s.abandonRetire(ctx, serverID, "could not record its progress ("+err.Error()+"); nothing was removed", stopped)
			return
		}
		outcome = s.attemptFinalBackup(ctx, client, live, stopped, stopWhy, sv, node)
		if outcome.status == store.FinalBackupSkipped && node != nil {
			fresh, answers := s.nodeAnswers(ctx, node.ID)
			if fresh != nil {
				node = fresh // the copy read before the stop is minutes old now
			}
			if answers {
				// It could not be tried, but the node answers now: it is tried
				// again, and nothing is removed from a node that answers
				// without it. Anything short of READY this time abandons the
				// retire.
				retry := s.retireClient(ctx, node)
				if retry == nil {
					s.abandonRetire(ctx, serverID, "the final backup could not be taken: "+outcome.note+"; nothing was removed", stopped)
					return
				}
				if !stopped {
					var reachable bool
					stopped, reachable, stopWhy = retireStop(ctx, retry, sv.ID)
					if !stopped {
						// A stop the node refused (rather than one it never
						// got) would refuse the next retire the same way, so
						// the reason says how to go ahead without it.
						reason := "the server could not be stopped for its final backup: " + stopWhy + "; nothing was removed" + stopRefusalHint
						if !reachable {
							reason = "the server could not be stopped for its final backup: the node stopped answering (" + stopWhy + "); nothing was removed"
						}
						s.abandonRetire(ctx, serverID, reason, false)
						return
					}
				}
				outcome = s.attemptFinalBackup(ctx, retry, true, true, "", sv, node)
				if outcome.status != store.FinalBackupReady {
					s.abandonRetire(ctx, serverID, "the final backup could not be taken: "+outcome.note+"; nothing was removed", stopped)
					return
				}
			}
		}
		if err := s.recordFinalBackup(ctx, serverID, outcome); err != nil {
			s.abandonRetire(ctx, serverID, "could not record the final backup ("+err.Error()+"); nothing was removed", stopped)
			return
		}
		if outcome.status == store.FinalBackupFailed {
			s.abandonRetire(ctx, serverID, "the final backup failed: "+outcome.note+"; nothing was removed", stopped)
			return
		}
	}

	if err := s.setRetirePhase(ctx, serverID, store.RetirePhaseRemoving); err != nil {
		s.abandonRetire(ctx, serverID, "could not record its progress ("+err.Error()+"); nothing was removed", stopped)
		return
	}
	if node != nil {
		// The removal may probe the node too (removeOnNode), and a probe
		// writes back the copy it works on: a fresh one, so a cordon or any
		// other edit made during the stop and the backup is not undone.
		if fresh, err := s.store.GetNode(ctx, node.ID); err == nil {
			node = fresh
		}
		var removeErr error
		if outcome.status == store.FinalBackupSkipped {
			// The node did not answer the probe above: it is not told to delete
			// anything now. The removal is queued, and lands when it answers.
			removeErr = fmt.Errorf("%w: %s", errNodeNotLive, outcome.note)
		} else {
			_, removeErr = s.removeOnNode(ctx, node, sv.ID, true, false)
		}
		if err := s.settleNodeAfterRemoval(ctx, removalOf(sv), node.ID, removeErr); err != nil {
			s.logger.Error("retire: could not record the removal on the node", "server", sv.ID, "node", node.ID,
				"removal_err", removeErr, "err", err)
			msg := "could not record the removal on the server's node, so the server was not retired; retire it again"
			if removeErr == nil {
				// The node did its part: the containers and the world are gone.
				msg = "the server's containers and world were removed on its node, but the retire could not be recorded; retire it again"
			}
			s.abandonRetire(ctx, serverID, msg, true)
			return
		}
		if removeErr != nil {
			notes = append(notes, "removing its containers and world is queued until node "+nodeLabel(node)+
				" answers ("+removalErrorText(removeErr)+")")
		}
	}
	s.completeRetire(ctx, sv, append(finalBackupNotes(outcome), notes...))
}

// finalBackupNotes is what retire_note says about the final backup: nothing
// when it was taken or not asked for.
func finalBackupNotes(o finalOutcome) []string {
	if o.status == store.FinalBackupSkipped {
		return []string{"final backup skipped: " + o.note}
	}
	return nil
}

// retireClient is the node's client when the Panel believes the node is up,
// or nil.
func (s *Server) retireClient(ctx context.Context, node *cluster.Node) agentpb.NodeServiceClient {
	if node == nil || s.ensureNodeLive(ctx, node) != nil {
		return nil
	}
	c, err := s.nodes.Client(node.DialTarget())
	if err != nil {
		return nil
	}
	return c
}

// nodeAnswers probes the node for real — not the stored status, which a node
// that just went away still reads as online — and records what it finds.
//
// The probe runs on a copy of the node read now, not on the retire's own: the
// probe writes back the copy it works on, and the retire's is as old as the
// retire, so a cordon or any other edit made during a long stop would be
// undone (#377). fresh is that copy, for the steps after the probe, or nil
// when it could not be read — and then nothing is probed or written.
func (s *Server) nodeAnswers(ctx context.Context, nodeID string) (fresh *cluster.Node, answers bool) {
	fresh, err := s.store.GetNode(ctx, nodeID)
	if err != nil {
		return nil, false
	}
	_, err = s.reconcileNode(ctx, fresh)
	return fresh, err == nil
}

// attemptFinalBackup tries the final backup, or says why it could not be
// tried. A skip is only ever for reachability or a stop that did not land;
// everything else the node says is a failure.
func (s *Server) attemptFinalBackup(ctx context.Context, client agentpb.NodeServiceClient, live, stopped bool, stopWhy string, sv *store.Server, node *cluster.Node) finalOutcome {
	switch {
	case node == nil:
		return finalOutcome{status: store.FinalBackupSkipped, note: "its node no longer exists"}
	case !live:
		return finalOutcome{status: store.FinalBackupSkipped, note: "node unreachable"}
	case !stopped:
		return finalOutcome{status: store.FinalBackupSkipped, note: "the server could not be stopped (" + stopWhy + ")"}
	}
	id, err := s.takeFinalBackup(ctx, client, sv)
	switch {
	case errors.Is(err, errNodeNotLive):
		return finalOutcome{status: store.FinalBackupSkipped, note: "node unreachable", id: id}
	case err != nil:
		return finalOutcome{status: store.FinalBackupFailed, note: err.Error(), id: id}
	}
	return finalOutcome{status: store.FinalBackupReady, id: id}
}

// mayBeRunning reports whether a server in st may have a container running.
func mayBeRunning(st store.ServerState) bool {
	switch st {
	case store.StateOffline, store.StateInstallFailed, store.StateRetired:
		return false
	}
	return true
}

// errFinalBackupTimeout is a final backup still PENDING at finalBackupTimeout.
// Its archiver may still be writing, so the world under it must not go.
var errFinalBackupTimeout = errors.New("the backup did not finish in time")

// takeFinalBackup takes the retire's final backup and waits for it to be
// READY, bounded by finalBackupTimeout. The error says why it is not; the id
// is the archive's, once the node has one.
func (s *Server) takeFinalBackup(ctx context.Context, client agentpb.NodeServiceClient, sv *store.Server) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, finalBackupTimeout)
	defer cancel()
	cctx, ccancel := context.WithTimeout(ctx, 30*time.Second)
	b, err := client.CreateBackup(cctx, s.backupRequestFor(cctx, sv, finalBackupName))
	ccancel()
	if err != nil {
		if code := status.Code(err); code == codes.Unavailable || code == codes.DeadlineExceeded {
			return "", errNodeNotLive // believed live, but it did not answer
		}
		return "", errors.New(status.Convert(err).Message())
	}
	slug := s.serverSlug(ctx, sv)
	for {
		switch b.GetState() {
		case agentpb.BackupState_BACKUP_STATE_FAILED:
			if b.GetError() != "" {
				return b.GetId(), errors.New(b.GetError())
			}
			return b.GetId(), errors.New("the node reported the backup failed without a reason")
		case agentpb.BackupState_BACKUP_STATE_PENDING:
		default:
			return b.GetId(), nil // ready (an archive with no tracked job reads as ready too)
		}
		select {
		case <-ctx.Done():
			return b.GetId(), fmt.Errorf("%w: it was still being written after %s", errFinalBackupTimeout, finalBackupTimeout)
		case <-time.After(finalBackupPollInterval):
		}
		lctx, lcancel := context.WithTimeout(ctx, 20*time.Second)
		list, lerr := client.ListBackups(lctx, &agentpb.ListBackupsRequest{ServerId: sv.ID, Slug: slug})
		lcancel()
		if lerr != nil {
			continue // a node that blinked mid-archive; the deadline decides
		}
		for _, cur := range list.GetBackups() {
			if cur.GetId() == b.GetId() {
				b = cur
			}
		}
	}
}

// setRetirePhase writes the retire's phase to the row (a fresh read, so
// nothing else on the row is lost). A write that fails is returned: the job
// stops there rather than remove anything the row does not record.
func (s *Server) setRetirePhase(ctx context.Context, serverID, phase string) error {
	return s.updateRetireBlock(ctx, serverID, func(r *store.ServerRetire) { r.Phase = phase })
}

// recordFinalBackup writes the final backup's outcome to the row's retire
// block, so a retire a Panel restart interrupts can still say it.
func (s *Server) recordFinalBackup(ctx context.Context, serverID string, o finalOutcome) error {
	return s.updateRetireBlock(ctx, serverID, func(r *store.ServerRetire) {
		r.FinalBackup, r.FinalBackupNote, r.FinalBackupID = o.status, o.note, o.id
	})
}

func (s *Server) updateRetireBlock(ctx context.Context, serverID string, edit func(*store.ServerRetire)) error {
	sv, err := s.store.GetServer(ctx, serverID)
	if err != nil {
		return err
	}
	if sv.Retire == nil {
		return errors.New("the retire record is gone from the server")
	}
	r := *sv.Retire
	edit(&r)
	sv.Retire = &r
	return s.store.UpdateServer(ctx, sv)
}

// abandonRetire ends a retire that could not go on without removing anything:
// the row loses its retire block and goes back to the state it came from —
// offline when the retire had already stopped it — with the reason in
// last_error, where the drill-in shows it, and in retire_note.
func (s *Server) abandonRetire(ctx context.Context, serverID, reason string, stopped bool) {
	s.logger.Warn("server retire abandoned", "server", serverID, "reason", reason)
	sv, err := s.store.GetServer(ctx, serverID)
	if err != nil {
		s.logger.Error("retire: could not load the server to abandon it", "server", serverID, "err", err)
		return
	}
	back := store.StateOffline
	if sv.Retire != nil && sv.Retire.PrevState != "" {
		back = sv.Retire.PrevState
	}
	if stopped && mayBeRunning(back) {
		back = store.StateOffline
	}
	if sv.State == store.StateRetiring {
		sv.State = back
	}
	sv.Retire = nil
	sv.RetireNote = "retire abandoned: " + reason
	sv.LastError = sv.RetireNote
	if err := s.store.UpdateServer(ctx, sv); err != nil {
		s.logger.Error("retire: could not record the abandoned retire", "server", serverID, "err", err)
	}
}

// completeRetire is the retire's last step, once the node's part is done or
// owed: DNS records and forwards go, schedules are switched off, and the row
// becomes retired — on no node, remembering the node and ports it had. sv is
// the row as the retire found it: nothing can have moved it since, because the
// retire held it.
func (s *Server) completeRetire(ctx context.Context, sv *store.Server, notes []string) {
	s.cleanupServerExternal(ctx, sv)
	s.disableSchedulesForRetire(ctx, sv.ID)
	fresh, err := s.store.GetServer(ctx, sv.ID)
	if err != nil {
		s.logger.Error("retire: could not load the server to mark it retired", "server", sv.ID, "err", err)
		return
	}
	now := time.Now().UTC()
	fresh.State = store.StateRetired
	fresh.RetiredAt = &now
	fresh.RetiredFromNodeID = sv.NodeID
	fresh.RetiredPorts = maps.Clone(sv.Ports)
	fresh.RetireNote = strings.Join(notes, "; ")
	fresh.Retire = nil
	fresh.NodeID = ""
	fresh.Ports = map[string]int{}
	// Everything below described a server on a node, with a tree.
	fresh.DNS, fresh.Forwards = nil, nil
	fresh.Players, fresh.MaxPlayers, fresh.PlayersKnown = 0, 0, false
	fresh.LastExitCode, fresh.LastExitCodeKnown = 0, false
	fresh.LastError = ""
	fresh.ProvisionedAt = nil
	if err := s.store.UpdateServer(ctx, fresh); err != nil {
		s.logger.Error("retire: could not mark the server retired", "server", sv.ID, "err", err)
		return
	}
	s.installs.Drop(sv.ID)
	s.logger.Info("server retired", "server", sv.ID, "name", sv.Name, "from_node", sv.NodeID, "note", fresh.RetireNote)
}

// disableSchedulesForRetire switches off every enabled schedule of the server
// and flags it, so a revive switches back on exactly these.
func (s *Server) disableSchedulesForRetire(ctx context.Context, serverID string) {
	tasks, err := s.store.ListSchedulesByServer(ctx, serverID)
	if err != nil {
		s.logger.Warn("retire: could not list the server's schedules; they stay as they are", "server", serverID, "err", err)
		return
	}
	for _, t := range tasks {
		if !t.Enabled {
			continue
		}
		t.Enabled, t.DisabledByRetire, t.NextRunAt = false, true, nil
		if err := s.store.UpdateSchedule(ctx, t); err != nil {
			s.logger.Warn("retire: could not switch a schedule off", "server", serverID, "schedule", t.ID, "err", err)
		}
	}
}

// enableRetireDisabledSchedules switches back on the schedules a retire
// switched off, and only those. provision calls it on every install that
// lands, so a revive whose install failed gets them back on the reinstall
// that succeeds (#377); a server that was never retired has none flagged, and
// a second call finds none left, so it is safe to call on any successful
// install. The scheduler arms each one's next run on its next pass.
func (s *Server) enableRetireDisabledSchedules(ctx context.Context, serverID string) {
	tasks, err := s.store.ListSchedulesByServer(ctx, serverID)
	if err != nil {
		s.logger.Warn("could not list the server's schedules to switch back on the ones its retire switched off; switch them on by hand",
			"server", serverID, "err", err)
		return
	}
	for _, t := range tasks {
		if !t.DisabledByRetire {
			continue
		}
		t.Enabled, t.DisabledByRetire, t.NextRunAt = true, false, nil
		if err := s.store.UpdateSchedule(ctx, t); err != nil {
			s.logger.Warn("could not switch back on a schedule the retire switched off", "server", serverID, "schedule", t.ID, "err", err)
			continue
		}
		s.logger.Info("switched back on a schedule the retire switched off", "server", serverID, "schedule", t.ID)
	}
}

// errOrphanedRetireRemoval is the removal error a Panel restart mid-removal is
// queued with: whether the Agent was told is not known, so it is told again.
var errOrphanedRetireRemoval = errors.New("the panel restarted while the retire was removing the server")

// settleOrphanedRetire finishes, or abandons, a retire this process has no job
// for — the only way to get one is a Panel that stopped mid-retire.
//
// Before the removal nothing is lost: the retire is abandoned, the server goes
// back to the state it came from, and the note says to retire it again. During
// the removal the Agent may or may not have been told, so it is told again — a
// pending removal, which is idempotent — and the retire completes, with the
// final backup's recorded outcome in its note. A removal phase with a final
// backup that is not settled cannot happen (the outcome is written first, and
// a failed write stops the job), and is abandoned rather than guessed at. The
// allocation travels with the queued removal only when the node provably still
// holds it for this server: queued already, or its ports still allocated with
// no other row on the node claiming them. Otherwise it was released before the
// restart, and holding it again would free it a second time when the removal
// lands.
func (s *Server) settleOrphanedRetire(ctx context.Context, id string) {
	sv, err := s.store.GetServer(ctx, id)
	if err != nil || s.restores.opHolding(id) != "" {
		return
	}
	if sv.Retire == nil {
		if sv.State == store.StateRetiring {
			s.abandonRetire(ctx, id, "the retire lost its record; nothing was removed — retire the server again", false)
		}
		return
	}
	backup := sv.Retire.FinalBackup
	settled := backup == store.FinalBackupOff || backup == store.FinalBackupReady || backup == store.FinalBackupSkipped
	if sv.Retire.Phase != store.RetirePhaseRemoving || !settled {
		s.abandonRetire(ctx, id, "the panel restarted while this retire was "+strings.ReplaceAll(sv.Retire.Phase, "_", " ")+
			"; nothing was removed — retire the server again", false)
		return
	}
	notes := append(finalBackupNotes(finalOutcome{status: backup, note: sv.Retire.FinalBackupNote}),
		"the panel restarted while this retire was removing the server")
	node, err := s.store.GetNode(ctx, sv.NodeID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		node = nil
	case err != nil:
		return // the next pass tries again
	}
	if node != nil {
		if _, owed := node.PendingRemovalFor(id); !owed {
			rm := removalOf(sv)
			if !s.nodeStillHolds(ctx, node, sv) {
				rm.memoryMB, rm.ports = 0, nil
			}
			if err := s.settleNodeAfterRemoval(ctx, rm, node.ID, errOrphanedRetireRemoval); err != nil {
				s.logger.Warn("reconcile: could not queue an orphaned retire's removal", "server", id, "err", err)
				return
			}
		}
		notes = append(notes, "removing its containers and world is queued for node "+nodeLabel(node))
	}
	s.completeRetire(ctx, sv, notes)
	s.logger.Warn("reconcile: finished a retire this panel has no job for (it restarted mid-removal)", "server", id)
}

// nodeStillHolds reports whether node's allocation provably still includes
// sv's ports: every one allocated, and none claimed by another row on the node.
func (s *Server) nodeStillHolds(ctx context.Context, node *cluster.Node, sv *store.Server) bool {
	if node.Ports == nil || len(sv.Ports) == 0 {
		return false
	}
	for _, p := range sv.Ports {
		if node.Ports.IsFree(p) {
			return false
		}
	}
	servers, err := s.store.ListServers(ctx)
	if err != nil {
		return false
	}
	for _, other := range servers {
		if other.ID == sv.ID || other.NodeID != node.ID {
			continue
		}
		for _, p := range other.Ports {
			for _, mine := range sv.Ports {
				if p == mine {
					return false
				}
			}
		}
	}
	return true
}

// holdRetiredRow takes the operation hold on a retired server for a revive or
// a permanent delete, or says why not. A retire still holding a row that
// already reads retired is only a moment from letting go — its last write is
// done — and the answer says to retry rather than call the server busy with
// something it is not.
func holdRetiredRow(s *Server, sv *store.Server, op string) *opRefusal {
	switch held := s.restores.holdOp(sv.ID, op); held {
	case "":
		return nil
	case opRetire:
		if sv.State == store.StateRetired {
			return &opRefusal{http.StatusConflict, codeServerBusy, "the retire is finishing — retry in a moment"}
		}
		return &opRefusal{http.StatusConflict, codeServerBusy, "this server is being retired"}
	default:
		return &opRefusal{http.StatusConflict, codeServerBusy, "this server is already being " + held + "d"}
	}
}

// ---- revive ----

type reviveRequest struct {
	// NodeID places the server on this node. Empty: the node it was retired
	// from, or — when that node no longer exists — wherever the scheduler
	// finds room.
	NodeID string `json:"node_id,omitempty"`
	// MemoryMB sizes the server; empty keeps what it had. It must clear the
	// spec's minimum, like a create's.
	MemoryMB int `json:"memory_mb,omitempty"`
	// RestoreBackupID restores this backup after the install, before any
	// start. It must be on the node the server is placed on.
	RestoreBackupID string `json:"restore_backup_id,omitempty"`
	// Start starts the server once the install (and the restore) succeeded.
	Start bool `json:"start,omitempty"`
	// SteamGuardCode is the one-time 2FA code for an authenticated install.
	SteamGuardCode string `json:"steam_guard_code,omitempty"`
}

// handleReviveServer places a retired server on a node again and starts its
// install, answering 202 with the server `installing`. The rest — the restore
// and the start — follow in the background (runRevive).
//
// POST /servers/{id}/revive. server.create.
func (s *Server) handleReviveServer(w http.ResponseWriter, r *http.Request) {
	var req reviveRequest
	if err := decodeJSON(r, &req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	ctx := r.Context()
	sv, err := s.store.GetServer(ctx, chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "server not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not get server")
		return
	}
	if !s.authorizeServer(w, ctx, sv) {
		return
	}
	if sv.State != store.StateRetired {
		writeCoded(w, http.StatusConflict, codeServerNotRetired,
			"only a retired server can be revived (current state: "+string(sv.State)+")")
		return
	}
	// Held until the row is placed and `installing`: two revives racing would
	// otherwise each reserve the server a place, and a permanent delete must
	// not take the row out from under one.
	if refusal := holdRetiredRow(s, sv, opRevive); refusal != nil {
		refusal.write(w)
		return
	}
	defer s.restores.releaseOp(sv.ID)
	sv, err = s.store.GetServer(ctx, sv.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not get server")
		return
	}
	if sv.State != store.StateRetired {
		writeCoded(w, http.StatusConflict, codeServerNotRetired,
			"only a retired server can be revived (current state: "+string(sv.State)+")")
		return
	}
	sp, err := s.store.GetSpec(ctx, sv.SpecID)
	if errors.Is(err, store.ErrNotFound) {
		writeCoded(w, http.StatusConflict, "spec_missing",
			"the game spec this server was built from no longer exists, so it cannot be revived")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load spec")
		return
	}
	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list nodes")
		return
	}
	// A removal still owed for this id — the retire's own, queued for a node
	// that did not answer — would delete the revived server's world the moment
	// its node came back. Wherever it is owed, it has to land (or be
	// dismissed) first.
	for _, n := range nodes {
		if _, owed := n.PendingRemovalFor(sv.ID); owed {
			writeCoded(w, http.StatusConflict, codeRemovalPending,
				"removing this server's old containers and world is still queued on node "+nodeLabel(n)+
					"; revive it once that node has confirmed the removal, or dismiss the removal first")
			return
		}
	}

	candidates, pinnedName, refusal := reviveCandidates(nodes, req.NodeID, sv.RetiredFromNodeID)
	if refusal != nil {
		refusal.write(w)
		return
	}
	// The same platform it ran on: its vars (APP_ID), its image and its
	// backups all belong to that one.
	kindSpec := *sp
	kindSpec.Platforms = slices.DeleteFunc(slices.Clone(sp.Platforms), func(p spec.Platform) bool { return p.Kind != sv.Kind })
	if len(kindSpec.Platforms) == 0 {
		writeCoded(w, http.StatusConflict, "platform_missing",
			"the game spec no longer offers the "+string(sv.Kind)+" platform this server ran on, so it cannot be revived")
		return
	}
	mem := sv.MemoryMB
	if req.MemoryMB != 0 {
		if req.MemoryMB < sp.Resources.MinMemoryMB {
			writeError(w, http.StatusBadRequest, fmt.Sprintf(
				"memory_mb must be at least the spec's minimum of %dMB", sp.Resources.MinMemoryMB))
			return
		}
		mem = req.MemoryMB
	} else if mem < sp.Resources.MinMemoryMB || mem == 0 {
		mem = sp.Resources.AllocMemoryMB()
	}
	placement, err := scheduler.PlaceWithMemory(&kindSpec, candidates, mem, sv.RetiredPorts)
	if err != nil {
		if pinnedName != "" {
			writeError(w, http.StatusConflict, "node "+pinnedName+" can't host this server: "+err.Error())
			return
		}
		writeError(w, http.StatusConflict, "no node can host this server: "+err.Error())
		return
	}
	chosen := findNode(candidates, placement.NodeID)
	if chosen == nil {
		writeError(w, http.StatusInternalServerError, "scheduler returned unknown node")
		return
	}
	// A backup to restore has to be on the node the server lands on, and has
	// to be whole — found out now, not after a ten-minute install.
	if req.RestoreBackupID != "" {
		if refusal := s.checkRevivalBackup(ctx, chosen, sv, req.RestoreBackupID); refusal != nil {
			refusal.write(w)
			return
		}
		// That check can take twenty seconds, and the node record is shared:
		// the reservation is made again on a copy read now, so nothing written
		// to the node meanwhile is lost with a stale one.
		fresh, err := s.store.GetNode(ctx, chosen.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not reload the node")
			return
		}
		placement, err = scheduler.PlaceWithMemory(&kindSpec, []*cluster.Node{fresh}, mem, sv.RetiredPorts)
		if err != nil {
			writeError(w, http.StatusConflict, "node "+nodeLabel(fresh)+" can't host this server: "+err.Error())
			return
		}
		chosen = fresh
	}
	if err := s.store.UpdateNode(ctx, chosen); err != nil {
		writeError(w, http.StatusInternalServerError, "could not persist node allocation")
		return
	}

	sv.NodeID = chosen.ID
	sv.Ports = placement.Ports
	sv.MemoryMB = placement.MemoryMB
	sv.Vars = maps.Clone(sv.Vars)
	if sv.Vars == nil {
		sv.Vars = map[string]string{}
	}
	for name, port := range placement.Ports {
		sv.Vars["PORT_"+strings.ToUpper(name)] = strconv.Itoa(port)
	}
	sv.State = store.StateInstalling
	sv.RetiredAt, sv.RetireNote, sv.RetiredFromNodeID, sv.RetiredPorts = nil, "", "", nil
	sv.LastError = ""
	sv.ProvisionedAt = nil
	if err := s.store.UpdateServer(ctx, sv); err != nil {
		// Give the place back: the server is still retired.
		if n, nerr := s.store.GetNode(ctx, chosen.ID); nerr == nil {
			ports := make([]int, 0, len(placement.Ports))
			for _, p := range placement.Ports {
				ports = append(ports, p)
			}
			n.Release(placement.MemoryMB, ports)
			_ = s.store.UpdateNode(ctx, n)
		}
		writeError(w, http.StatusInternalServerError, "could not update server state")
		return
	}
	s.logger.Info("server revive started", "server", sv.ID, "name", sv.Name, "node", chosen.Name,
		"ports", placement.Ports, "restore", req.RestoreBackupID, "start", req.Start)
	go s.runRevive(sv, sp, chosen, req.SteamGuardCode, req.RestoreBackupID, req.Start)
	writeJSON(w, http.StatusAccepted, s.serverResponse(sv))
}

// reviveCandidates narrows placement for a revive: the node asked for, else
// the node the server was retired from, else — that node being gone — every
// node. A node asked for that does not exist is a 404; the old node is only a
// default, so its absence is not.
func reviveCandidates(nodes []*cluster.Node, asked, retiredFrom string) ([]*cluster.Node, string, *opRefusal) {
	if asked != "" {
		n := findNode(nodes, asked)
		if n == nil {
			return nil, "", &opRefusal{http.StatusNotFound, "node_not_found", "node not found"}
		}
		return []*cluster.Node{n}, nodeLabel(n), nil
	}
	if n := findNode(nodes, retiredFrom); n != nil {
		return []*cluster.Node{n}, nodeLabel(n), nil
	}
	return nodes, "", nil
}

// checkRevivalBackup confirms backupID is a ready archive of sv on node.
func (s *Server) checkRevivalBackup(ctx context.Context, node *cluster.Node, sv *store.Server, backupID string) *opRefusal {
	if lerr := s.ensureNodeLive(ctx, node); lerr != nil {
		return &opRefusal{http.StatusServiceUnavailable, codeNodeUnreachable,
			"node " + nodeLabel(node) + " is offline, so the backup to restore could not be checked; nothing was changed"}
	}
	client, err := s.nodes.Client(node.DialTarget())
	if err != nil {
		st, code, msg := agentFailure(err)
		return &opRefusal{st, code, msg}
	}
	lctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	resp, err := client.ListBackups(lctx, &agentpb.ListBackupsRequest{ServerId: sv.ID, Slug: s.serverSlug(ctx, sv)})
	if err != nil {
		st, code, msg := agentFailure(err)
		return &opRefusal{st, code, msg}
	}
	for _, b := range resp.GetBackups() {
		if b.GetId() != backupID {
			continue
		}
		if st := backupStateString(b.GetState()); st != "ready" {
			return &opRefusal{http.StatusConflict, "backup_not_ready",
				"backup " + backupID + " is " + st + " on node " + nodeLabel(node) + ", so it cannot be restored"}
		}
		return nil
	}
	return &opRefusal{http.StatusConflict, "backup_not_found",
		"node " + nodeLabel(node) + " has no backup " + backupID + " for this server; revive it on the node that holds the backup"}
}

// runRevive is everything after the placement, in order, each step only when
// the one before it landed:
//
//   - the install pass (provision): a failure lands install_failed, as a
//     failed create does, and the schedules stay off; once an install lands —
//     this one, or a later reinstall — provision switches back on the
//     schedules the retire switched off;
//   - the restore, when one was asked for: through the same job an operator's
//     restore runs (restoring → offline, the outcome in restore_result);
//   - the start, when asked: the checks and the update decision an operator's
//     start gets (a refusal or a failure lands offline with last_error).
func (s *Server) runRevive(sv *store.Server, sp *spec.Spec, node *cluster.Node, steamGuardCode, backupID string, start bool) {
	s.provision(sv, sp, node, steamGuardCode, "")
	ctx, cancel := context.WithTimeout(context.Background(), restoreDeadline)
	defer cancel()
	after, err := s.store.GetServer(ctx, sv.ID)
	if err != nil || after.State != store.StateOffline {
		return // the install did not land; provision said why on the row
	}
	if backupID != "" {
		if !s.reviveRestore(ctx, sv.ID, backupID, node) {
			return
		}
	}
	if start {
		s.startAfterRevive(ctx, sv.ID)
	}
}

// reviveRestore runs the revive's restore to its end and reports whether it
// landed. A restore that could not even begin is recorded in restore_result
// like one that failed, so the operator reads the reason in one place.
func (s *Server) reviveRestore(ctx context.Context, serverID, backupID string, node *cluster.Node) bool {
	fail := func(reason string) bool {
		s.logger.Warn("revive: the restore did not run", "server", serverID, "backup", backupID, "reason", reason)
		if sv, err := s.store.GetServer(ctx, serverID); err == nil {
			sv.RestoreResult = &store.RestoreResult{BackupID: backupID, OK: false, Error: reason, FinishedAt: time.Now().UTC()}
			_ = s.store.UpdateServer(ctx, sv)
		}
		return false
	}
	client, err := s.nodes.Client(node.DialTarget())
	if err != nil {
		return fail("could not reach the node's agent: " + err.Error())
	}
	sv, refusal := s.beginRestore(ctx, serverID, backupID)
	if refusal != nil {
		return fail(refusal.msg)
	}
	s.runRestore(client, &agentpb.RestoreBackupRequest{ServerId: serverID, Id: backupID, Slug: s.serverSlug(ctx, sv)})
	after, err := s.store.GetServer(ctx, serverID)
	return err == nil && after.RestoreResult != nil && after.RestoreResult.OK
}

// startAfterRevive starts a revived server the way an operator's start does —
// checkStartable, the start claim, the update-on-start decision (which a start
// inside the fresh-install window skips) — and lands the outcome on the row.
func (s *Server) startAfterRevive(ctx context.Context, serverID string) {
	refused := func(reason string) {
		s.logger.Warn("revive: the start did not happen", "server", serverID, "reason", reason)
		s.setServerState(serverID, store.StateOffline, "start after revive: "+reason)
	}
	sv, err := s.store.GetServer(ctx, serverID)
	if err != nil {
		return
	}
	if r := s.checkStartable(ctx, sv, agentpb.PowerAction_POWER_ACTION_START); r != nil {
		refused(r.message)
		return
	}
	release, r := s.claimStart(serverID)
	if r != nil {
		refused(r.message)
		return
	}
	defer release()
	sv, err = s.store.GetServer(ctx, serverID)
	if err != nil {
		return
	}
	if r := s.checkStartable(ctx, sv, agentpb.PowerAction_POWER_ACTION_START); r != nil {
		refused(r.message)
		return
	}
	node, err := s.store.GetNode(ctx, sv.NodeID)
	if err != nil {
		refused("could not load its node")
		return
	}
	if lerr := s.ensureNodeLive(ctx, node); lerr != nil {
		refused("node " + nodeLabel(node) + " is offline (" + lerr.Error() + ")")
		return
	}
	client, err := s.nodes.Client(node.DialTarget())
	if err != nil {
		refused(err.Error())
		return
	}
	sp, err := s.store.GetSpec(ctx, sv.SpecID)
	if err != nil {
		refused("could not load its game spec")
		return
	}
	if s.updatesOnStart(ctx, sv, sp, node) {
		prev := sv.State
		sv.State = store.StateInstalling
		sv.LastError = ""
		if err := s.store.UpdateServer(ctx, sv); err != nil {
			refused("could not update its state")
			return
		}
		release() // `installing` keeps a restore out from here on
		s.updateThenStart(sv, sp, node, prev)
		return
	}
	s.rePushServerSpec(ctx, client, sv, sp)
	if _, aerr := s.applyConfig(ctx, sv, sp); aerr != nil {
		s.logger.Warn("config apply before start failed", "server", sv.ID, "err", aerr)
	}
	pctx, pcancel := context.WithTimeout(ctx, powerTimeout(agentpb.PowerAction_POWER_ACTION_START))
	resp, err := client.PowerAction(pctx, &agentpb.PowerActionRequest{ServerId: sv.ID, Action: agentpb.PowerAction_POWER_ACTION_START})
	pcancel()
	if err != nil {
		_, _, msg := agentFailure(err)
		refused(msg)
		return
	}
	fresh, err := s.store.GetServer(ctx, serverID)
	if err != nil {
		return
	}
	fresh.State = storeStateFromAgent(resp.State)
	fresh.LastError = ""
	fresh.LastExitCode, fresh.LastExitCodeKnown = 0, false
	if err := s.store.UpdateServer(ctx, fresh); err != nil {
		s.logger.Error("revive: could not record the start", "server", serverID, "err", err)
	}
}

// ---- permanent delete ----

// handleDeleteServer is the permanent delete, and only of a retired server:
// a live one is retired first (POST /servers/{id}/retire), which is what the
// delete button now does.
//
// The node the server was retired from is told to delete what is left of it —
// the world, should the retire's removal still be owed, and its archives where
// they are its own (the zero-config layout; archives on a shared target are
// kept and the answer says so). A node that cannot be reached is owed the
// removal, delete_backups and all, and the row goes regardless (#354); a
// removal that cannot be recorded stops the delete.
//
// DELETE /servers/{id}. server.delete. 200 with {note, removal_pending}.
func (s *Server) handleDeleteServer(w http.ResponseWriter, r *http.Request) {
	// Detached from the request: once the removal has been attempted, the
	// record of how it went must be written even if the client has gone.
	ctx := context.WithoutCancel(r.Context())
	sv, err := s.store.GetServer(ctx, chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "server not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not get server")
		return
	}
	if !s.authorizeServer(w, ctx, sv) {
		return
	}
	// A server something holds says so first: "retire it first" would send the
	// operator after the wrong thing while a restore or a retire is running.
	if sv.State != store.StateRetired && s.refuseWhileHeld(w, sv) {
		return
	}
	if sv.State != store.StateRetired {
		writeCoded(w, http.StatusConflict, codeServerNotRetired,
			"retire the server first — only a retired server can be deleted permanently (current state: "+string(sv.State)+")")
		return
	}
	// Held for the whole delete, so a revive cannot place the row while it is
	// being deleted, and re-read under the hold: a revive that landed between
	// the read above and the hold has made it a live server again.
	if refusal := holdRetiredRow(s, sv, opDelete); refusal != nil {
		refusal.write(w)
		return
	}
	defer s.restores.releaseOp(sv.ID)
	sv, err = s.store.GetServer(ctx, sv.ID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "server not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not get server")
		return
	}
	if sv.State != store.StateRetired {
		writeCoded(w, http.StatusConflict, codeServerNotRetired,
			"retire the server first — only a retired server can be deleted permanently (current state: "+string(sv.State)+")")
		return
	}
	// A node that no longer exists has nothing to be told and nothing to hold;
	// any other failure to read it means the removal could be neither
	// delivered nor remembered, and a delete the Panel cannot remember does
	// not happen.
	node, err := s.store.GetNode(ctx, sv.RetiredFromNodeID)
	switch {
	case errors.Is(err, store.ErrNotFound), sv.RetiredFromNodeID == "":
		node = nil
	case err != nil:
		s.logger.Error("server delete refused: could not load its node", "server", sv.ID, "node", sv.RetiredFromNodeID, "err", err)
		writeError(w, http.StatusInternalServerError, "could not load the node this server was retired from; nothing was deleted")
		return
	}
	note, pending := "", false
	if node != nil {
		resp, removeErr := s.removeOnNode(ctx, node, sv.ID, true, true)
		// Nothing of the allocation is this row's any more: the retire released
		// it, or queued a removal of its own that holds it — which this one is
		// folded into, and whose success releases it.
		rm := nodeRemoval{serverID: sv.ID, name: sv.Name, deleteBackups: true}
		if err := s.settleNodeAfterRemoval(ctx, rm, node.ID, removeErr); err != nil {
			s.logger.Error("server delete refused: could not record its removal on the node",
				"server", sv.ID, "node", node.ID, "removal_err", removeErr, "err", err)
			msg := "could not record the removal on the node this server was retired from; the server was not deleted"
			if removeErr == nil {
				// The node did its part. Only the Panel's books are behind, and
				// a retry settles them.
				msg = "the server's data and archives were removed on the node but the delete could not be recorded; retry the delete"
			}
			writeError(w, http.StatusInternalServerError, msg)
			return
		}
		if removeErr != nil {
			pending = true
			note = "node " + nodeLabel(node) + " could not be reached (" + removalErrorText(removeErr) +
				"); its archives of this server are deleted when it answers"
		} else {
			note = backupsKeptNote(resp)
		}
	}
	s.cleanupServerExternal(ctx, sv)
	if err := s.store.DeleteServer(ctx, sv.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "could not delete server")
		return
	}
	s.installs.Drop(sv.ID)
	s.logger.Info("retired server deleted permanently", "server", sv.ID, "name", sv.Name, "node", sv.RetiredFromNodeID,
		"removal_pending", pending, "note", note)
	writeJSON(w, http.StatusOK, map[string]any{"note": note, "removal_pending": pending})
}
