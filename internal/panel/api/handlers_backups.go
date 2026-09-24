package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/briggleman/kraken/internal/panel/store"
	"github.com/briggleman/kraken/internal/shared/agentpb"
)

type backupView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	Created     int64  `json:"created_ms"`
	State       string `json:"state"`           // "pending" | "ready" | "failed"
	Replication string `json:"replication"`     // "" (none) | "pending" | "done" | "failed"
	Error       string `json:"error,omitempty"` // why it failed (or a degraded-capture note)
}

func toBackupView(b *agentpb.BackupInfo) backupView {
	return backupView{
		ID: b.Id, Name: b.Name, Size: b.Size, Created: b.CreatedUnixMs,
		State: backupStateString(b.State), Replication: replicationStateString(b.Replication),
		Error: b.Error,
	}
}

// backupStateString maps the archive state to a short token; UNSPECIFIED (an
// on-disk archive with no tracked job, e.g. after an agent restart) reads as ready.
func backupStateString(s agentpb.BackupState) string {
	switch s {
	case agentpb.BackupState_BACKUP_STATE_PENDING:
		return "pending"
	case agentpb.BackupState_BACKUP_STATE_FAILED:
		return "failed"
	default:
		return "ready"
	}
}

func replicationStateString(s agentpb.ReplicationState) string {
	switch s {
	case agentpb.ReplicationState_REPLICATION_STATE_PENDING:
		return "pending"
	case agentpb.ReplicationState_REPLICATION_STATE_DONE:
		return "done"
	case agentpb.ReplicationState_REPLICATION_STATE_FAILED:
		return "failed"
	default:
		return ""
	}
}

func (s *Server) handleListBackups(w http.ResponseWriter, r *http.Request) {
	client, sv, ok := s.agentForServer(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	resp, err := client.ListBackups(ctx, &agentpb.ListBackupsRequest{ServerId: sv.ID, Slug: s.serverSlug(ctx, sv)})
	if err != nil {
		writeAgentError(w, err)
		return
	}
	views := make([]backupView, 0, len(resp.Backups))
	for _, b := range resp.Backups {
		views = append(views, toBackupView(b))
	}
	// The off-node mirror destination, for the drill-in's detail and ledger. It is
	// node config, not per-archive: the same target every current backup mirrors
	// to (see The Spoken Mirror Rule in DESIGN.md). Empty when replication is off,
	// which the UI reads as "no mirror line". A config the panel can't load is not
	// fatal to listing backups — the mirror simply goes unnamed.
	mirror := ""
	if cfg, cerr := s.nodeConfigOrEmpty(ctx, sv.NodeID); cerr == nil {
		mirror = mirrorTarget(cfg)
	}
	writeJSON(w, http.StatusOK, map[string]any{"backups": views, "mirror": mirror})
}

// mirrorTarget formats a node's off-node replication destination for display, or
// "" when replication is off. It names the kind and host only — never a
// credential — because it is shown in the UI. The host being absent while the
// toggle is on means a half-configured mirror; naming it "sftp" without a host
// would imply a working destination there is not, so that also reads as off.
func mirrorTarget(c *store.NodeConfig) string {
	switch {
	case c.ReplicateToSftp && c.SftpHost != "":
		return "sftp " + c.SftpHost
	case c.ReplicateToSmb && c.SmbHost != "":
		t := "smb " + c.SmbHost
		if c.SmbShare != "" {
			t += "/" + c.SmbShare
		}
		return t
	default:
		return ""
	}
}

type createBackupRequest struct {
	Name string `json:"name"`
}

func (s *Server) handleCreateBackup(w http.ResponseWriter, r *http.Request) {
	var req createBackupRequest
	_ = decodeJSON(r, &req) // name optional
	client, sv, ok := s.agentForServer(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	if s.refuseWhileRestoring(w, sv) {
		return
	}
	// CreateBackup is asynchronous on the Agent — it returns a PENDING record
	// immediately and archives in the background, so a short deadline is fine.
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	b, err := client.CreateBackup(ctx, s.backupRequestFor(ctx, sv, req.Name))
	if err != nil {
		writeAgentError(w, err)
		return
	}
	// 202 Accepted: archiving has started; the client polls the list for READY.
	writeJSON(w, http.StatusAccepted, toBackupView(b))
}

// restorableStates are the states a restore is allowed from. A save-set restore
// replaces exactly the files a running game holds open: on Windows the swap hits
// a sharing violation partway, and on Linux it succeeds only to be overwritten
// by the running process's next save. installing is excluded too — the install
// is still writing the tree the restore would swap out from under it.
var restorableStates = map[store.ServerState]bool{
	store.StateOffline:       true,
	store.StateCrashed:       true,
	store.StateInstallFailed: true,
}

// handleRestoreBackup starts a restore and answers at once (#361).
//
// It used to hold the request open on one unary RPC for up to ten minutes with
// the server sitting in `offline` — so nothing stopped a start racing the
// extraction, the reconciler could adopt a container over it, and the only
// sign of life was the button's own label. Now the server enters `restoring`
// before the answer goes out (202, with the server view carrying the job), and
// a background job streams the Agent's progress into GET /servers/{id} until
// the restore lands or fails and the row goes back to the state it came from.
func (s *Server) handleRestoreBackup(w http.ResponseWriter, r *http.Request) {
	client, sv, ok := s.agentForServer(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	backupID := chi.URLParam(r, "backupId")
	// Checked before the state: a server mid-restore is not stopped either, and
	// "stop the server" would send the operator after the wrong thing.
	if s.restoreInProgress(sv) {
		writeJSON(w, http.StatusConflict, errorCodeBody{
			Error: "a restore is already in progress for this server; wait for it to finish",
			Code:  "restore_in_progress",
		})
		return
	}
	if !restorableStates[sv.State] {
		writeError(w, http.StatusConflict, "stop the server before restoring a backup (current state: "+string(sv.State)+")")
		return
	}
	ctx := r.Context()
	node, err := s.store.GetNode(ctx, sv.NodeID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load hosting node")
		return
	}
	// Refuse up front when the node is unreachable, as a power action does
	// (#328): otherwise the server would flip to restoring only for the job to
	// fail on its first dial and write that back as a restore failure.
	if lerr := s.ensureNodeLive(ctx, node); lerr != nil {
		writeError(w, http.StatusServiceUnavailable,
			"node "+nodeLabel(node)+" is offline — the panel has no live connection to its agent ("+lerr.Error()+"); nothing was changed")
		return
	}
	job, started := s.restores.start(sv.ID, backupID)
	if !started {
		writeJSON(w, http.StatusConflict, errorCodeBody{
			Error: "a restore is already in progress for this server; wait for it to finish",
			Code:  "restore_in_progress",
		})
		return
	}
	// Re-read now the job holds the registry: the row loaded above may be
	// stale, and a start or reinstall that wrote a new state in between must
	// win — its own re-check (claimForStart) will see this job and stop.
	fresh, err := s.store.GetServer(ctx, sv.ID)
	if err != nil || !restorableStates[fresh.State] {
		s.restores.finish(sv.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not load server")
			return
		}
		writeError(w, http.StatusConflict, "stop the server before restoring a backup (current state: "+string(fresh.State)+")")
		return
	}
	sv = fresh
	// Where the row goes back to, on the row itself: a Panel that restarts
	// mid-restore loses the job, and the orphan settle still has to put an
	// install_failed server back behind its reinstall gate.
	sv.Restore = &store.ServerRestore{
		BackupID: backupID, PrevState: sv.State, StartedAt: job.StartedAt, Phase: job.Phase,
	}
	sv.State = store.StateRestoring
	if err := s.store.UpdateServer(ctx, sv); err != nil {
		s.restores.finish(sv.ID)
		writeError(w, http.StatusInternalServerError, "could not update server state")
		return
	}
	req := &agentpb.RestoreBackupRequest{ServerId: sv.ID, Id: backupID, Slug: s.serverSlug(ctx, sv)}
	s.logger.Info("backup restore started", "server", sv.ID, "name", sv.Name, "backup", backupID)
	go s.runRestore(client, req)
	writeJSON(w, http.StatusAccepted, s.serverResponse(sv))
}

// errorCodeBody is the error shape that also names a machine-readable code, so
// a client can branch on the refusal instead of on its wording.
type errorCodeBody struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

// runRestore is the restore job: it drives the Agent with a background context
// (the restore outlives the request that asked for it), then settles the row.
func (s *Server) runRestore(client agentpb.NodeServiceClient, req *agentpb.RestoreBackupRequest) {
	ctx, cancel := context.WithTimeout(context.Background(), restoreDeadline)
	defer cancel()
	err := s.restoreOnAgent(ctx, client, req)
	s.finishRestore(req.ServerId, req.Id, err)
}

// restoreOnAgent runs the streamed restore, falling back to the unary call
// against an Agent that predates the stream. The fallback is decided on the
// FIRST response only: an Unimplemented after progress has flowed is not an
// old Agent, and replaying the restore through the unary call would run it a
// second time over a tree the first one may have half-swapped. No other code
// ever falls back.
func (s *Server) restoreOnAgent(ctx context.Context, client agentpb.NodeServiceClient, req *agentpb.RestoreBackupRequest) error {
	stream, err := client.RestoreBackupStream(ctx, req)
	if err != nil {
		if status.Code(err) == codes.Unimplemented {
			return s.restoreUnary(ctx, client, req)
		}
		return fmt.Errorf("the restore did not start: %w", err)
	}
	heard := false
	for {
		ev, rerr := stream.Recv()
		if errors.Is(rerr, io.EOF) {
			return nil // a clean end of the stream is a restore that landed
		}
		if rerr != nil {
			if !heard {
				if status.Code(rerr) == codes.Unimplemented {
					return s.restoreUnary(ctx, client, req)
				}
				// Nothing was ever reported, so nothing ran.
				return fmt.Errorf("the restore did not start: %v", rerr)
			}
			// The stream died mid-restore. Which of two things happened is not
			// visible from here: a cut connection cancels the Agent's context and
			// it unwinds, but an Agent that crashed or restarted mid-swap does
			// not, and the displaced originals are then left beside the tree.
			return fmt.Errorf("the restore stream was interrupted (%v); if the agent kept running it rolled the files back, "+
				"but if it crashed or restarted mid-swap the originals are left beside the tree as *%s* directories — "+
				"check the server's files before starting it", rerr, agentAsideMarker)
		}
		heard = true
		if ev.GetFailed() != "" || ev.GetPhase() == "failed" {
			reason := ev.GetFailed()
			if reason == "" {
				reason = "the agent reported a failure without a reason"
			}
			return errors.New(reason)
		}
		s.restores.progress(req.ServerId, ev.GetPhase(), ev.GetBytesDone(), ev.GetBytesTotal())
	}
}

// agentAsideMarker is the Agent's name for a displaced original during a swap
// (restore.go, asideMarker), repeated here only for the operator's message.
const agentAsideMarker = ".kraken-aside-"

// restoreUnary is the old Agent's restore: one blocking call, no progress. The
// job says so by its phase and leaves bytes_total at 0, which the UI draws as
// an indeterminate meter rather than a number it would have to invent.
func (s *Server) restoreUnary(ctx context.Context, client agentpb.NodeServiceClient, req *agentpb.RestoreBackupRequest) error {
	s.restores.progress(req.ServerId, restorePhaseUnary, 0, 0)
	if _, err := client.RestoreBackup(ctx, req); err != nil {
		return err
	}
	return nil
}

// finishRestore writes the job's outcome to the row, and only then drops the
// job — in that order, so the reconciler can never see a `restoring` row with
// no job behind it and mistake a restore that just ended for one the Panel lost.
//
// The row goes back to the state it came from (Restore.PrevState): a restore
// puts save files back, it does not repair an install, so returning an
// install_failed server to offline would offer START over a tree that was
// never provisioned. last_error is never touched — an install's reason stays —
// and the outcome goes to restore_result, which is what the UI reads.
//
// If the row is no longer `restoring`, someone else wrote it while the job ran
// (an operator's stop landing on a row the reconciler settled, a direct store
// edit); that write wins, and only the result is recorded.
func (s *Server) finishRestore(serverID, backupID string, restoreErr error) {
	defer s.restores.finish(serverID)
	sv, err := s.store.GetServer(context.Background(), serverID)
	if err != nil {
		s.logger.Error("could not load server to settle its restore", "server", serverID, "err", err)
		return
	}
	result := &store.RestoreResult{BackupID: backupID, OK: restoreErr == nil, FinishedAt: time.Now().UTC()}
	if restoreErr != nil {
		result.Error = restoreErr.Error()
		s.logger.Warn("backup restore failed", "server", serverID, "backup", backupID, "err", restoreErr)
	} else {
		s.logger.Info("backup restore finished", "server", serverID, "backup", backupID)
	}
	if sv.State == store.StateRestoring {
		sv.State = restoreReturnState(sv.Restore)
		// A crash's exit code described a run the restore has just replaced.
		sv.LastExitCode, sv.LastExitCodeKnown = 0, false
	} else {
		s.logger.Warn("restore settled on a row someone else moved; leaving its state", "server", serverID, "state", sv.State)
	}
	sv.Restore = nil
	sv.RestoreResult = result
	if err := s.store.UpdateServer(context.Background(), sv); err != nil {
		s.logger.Error("could not settle the server after its restore", "server", serverID, "err", err)
	}
}

// restoreReturnState is where a settled restore leaves its row: the state it
// came from, or offline for a row that never recorded one (written before
// Restore.PrevState existed, or damaged). Only a stopped state is honoured —
// a restore never returns a row to running.
func restoreReturnState(r *store.ServerRestore) store.ServerState {
	if r != nil && restorableStates[r.PrevState] {
		return r.PrevState
	}
	return store.StateOffline
}

func (s *Server) handleDeleteBackup(w http.ResponseWriter, r *http.Request) {
	client, sv, ok := s.agentForServer(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	if s.refuseWhileRestoring(w, sv) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if _, err := client.DeleteBackup(ctx, &agentpb.DeleteBackupRequest{ServerId: sv.ID, Id: chi.URLParam(r, "backupId"), Slug: s.serverSlug(ctx, sv)}); err != nil {
		writeAgentError(w, err)
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}
