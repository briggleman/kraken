package api

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

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
		writeError(w, http.StatusBadGateway, "agent error: "+err.Error())
		return
	}
	views := make([]backupView, 0, len(resp.Backups))
	for _, b := range resp.Backups {
		views = append(views, toBackupView(b))
	}
	writeJSON(w, http.StatusOK, map[string]any{"backups": views})
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
	// CreateBackup is asynchronous on the Agent — it returns a PENDING record
	// immediately and archives in the background, so a short deadline is fine.
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	b, err := client.CreateBackup(ctx, s.backupRequestFor(ctx, sv, req.Name))
	if err != nil {
		writeError(w, http.StatusBadGateway, "agent error: "+err.Error())
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

func (s *Server) handleRestoreBackup(w http.ResponseWriter, r *http.Request) {
	client, sv, ok := s.agentForServer(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	if !restorableStates[sv.State] {
		writeError(w, http.StatusConflict, "stop the server before restoring a backup (current state: "+string(sv.State)+")")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	if _, err := client.RestoreBackup(ctx, &agentpb.RestoreBackupRequest{ServerId: sv.ID, Id: chi.URLParam(r, "backupId"), Slug: s.serverSlug(ctx, sv)}); err != nil {
		writeError(w, http.StatusBadGateway, "agent error: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "restored"})
}

func (s *Server) handleDeleteBackup(w http.ResponseWriter, r *http.Request) {
	client, sv, ok := s.agentForServer(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if _, err := client.DeleteBackup(ctx, &agentpb.DeleteBackupRequest{ServerId: sv.ID, Id: chi.URLParam(r, "backupId"), Slug: s.serverSlug(ctx, sv)}); err != nil {
		writeError(w, http.StatusBadGateway, "agent error: "+err.Error())
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}
