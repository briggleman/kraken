package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/briggleman/kraken/internal/panel/cluster"
	"github.com/briggleman/kraken/internal/panel/store"
	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// validBackupTargets is the set of accepted backup_target values ("" → local).
// "share" is a mounted network share (SMB/NFS) the Agent writes to via native
// file ops — the cross-OS backup destination for NAS devices without SFTP.
var validBackupTargets = map[string]bool{"": true, "local": true, "share": true, "sftp": true}

// allowedBackupTokens are the dynamic-naming substitutions the Agent expands in
// a node's backup path (backup_dir / sftp_base_path). {{SLUG}} is the server's
// game-spec slug, so e.g. /media/games/{{SLUG}}/backup gives each game its own
// directory while servers stay isolated by their per-server subdir.
var allowedBackupTokens = map[string]bool{"{{SLUG}}": true}

var backupTokenRE = regexp.MustCompile(`{{[^}]*}}`)

// validateBackupPathTokens rejects any {{...}} token the Agent won't expand, so
// a typo (e.g. {{SLG}}) fails loudly at save time instead of silently creating a
// directory named after the literal token.
func validateBackupPathTokens(p string) error {
	for _, tok := range backupTokenRE.FindAllString(p, -1) {
		if !allowedBackupTokens[tok] {
			return fmt.Errorf("unknown path token %q (supported: {{SLUG}})", tok)
		}
	}
	return nil
}

// nodeConfigView is the per-node config returned to the UI. Secret fields are
// never echoed back — only a "*_configured" flag indicating whether one is set.
type nodeConfigView struct {
	BackupTarget string `json:"backup_target"`
	BackupDir    string `json:"backup_dir,omitempty"`

	SftpHost               string `json:"sftp_host,omitempty"`
	SftpUser               string `json:"sftp_user,omitempty"`
	SftpPasswordConfigured bool   `json:"sftp_password_configured"`
	SftpKeyConfigured      bool   `json:"sftp_key_configured"`
	SftpBasePath           string `json:"sftp_base_path,omitempty"`
	SftpKnownHostKey       string `json:"sftp_known_host_key,omitempty"`

	ReplicateToSftp bool `json:"replicate_to_sftp"`

	SteamUsername   string `json:"steam_username,omitempty"`
	SteamConfigured bool   `json:"steam_configured"` // a Steam password is stored
}

func toNodeConfigView(c *store.NodeConfig) nodeConfigView {
	target := c.BackupTarget
	if target == "" {
		target = "local"
	}
	return nodeConfigView{
		BackupTarget:           target,
		BackupDir:              c.BackupDir,
		SftpHost:               c.SftpHost,
		SftpUser:               c.SftpUser,
		SftpPasswordConfigured: c.SftpPassword != "",
		SftpKeyConfigured:      c.SftpPrivateKey != "",
		SftpBasePath:           c.SftpBasePath,
		SftpKnownHostKey:       c.SftpKnownHostKey,
		ReplicateToSftp:        c.ReplicateToSftp,
		SteamUsername:          c.SteamUsername,
		SteamConfigured:        c.SteamPassword != "",
	}
}

// nodeConfigToProto maps the stored config to the gRPC NodeConfig delivered to
// the Agent.
func nodeConfigToProto(c *store.NodeConfig) *agentpb.NodeConfig {
	return &agentpb.NodeConfig{
		BackupTarget:     c.BackupTarget,
		BackupDir:        c.BackupDir,
		SftpHost:         c.SftpHost,
		SftpUser:         c.SftpUser,
		SftpPassword:     c.SftpPassword,
		SftpPrivateKey:   c.SftpPrivateKey,
		SftpBasePath:     c.SftpBasePath,
		SftpKnownHostKey: c.SftpKnownHostKey,
		ReplicateToSftp:  c.ReplicateToSftp,
	}
}

// nodeConfigOrEmpty loads a node's config, returning an empty (unconfigured)
// value when none is stored.
func (s *Server) nodeConfigOrEmpty(ctx context.Context, nodeID string) (*store.NodeConfig, error) {
	c, err := s.store.GetNodeConfig(ctx, nodeID)
	if errors.Is(err, store.ErrNotFound) {
		return &store.NodeConfig{}, nil
	}
	if err != nil {
		return nil, err
	}
	return c, nil
}

func (s *Server) handleGetNodeConfig(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := s.store.GetNode(r.Context(), id); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "node not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "could not get node")
		return
	}
	c, err := s.nodeConfigOrEmpty(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load node config")
		return
	}
	writeJSON(w, http.StatusOK, toNodeConfigView(c))
}

type updateNodeConfigRequest struct {
	// Pointers so an omitted field leaves the value unchanged; "" clears it.
	BackupTarget *string `json:"backup_target"`
	BackupDir    *string `json:"backup_dir"`

	SftpHost         *string `json:"sftp_host"`
	SftpUser         *string `json:"sftp_user"`
	SftpPassword     *string `json:"sftp_password"`
	SftpPrivateKey   *string `json:"sftp_private_key"`
	SftpBasePath     *string `json:"sftp_base_path"`
	SftpKnownHostKey *string `json:"sftp_known_host_key"`

	ReplicateToSftp *bool `json:"replicate_to_sftp"`

	SteamUsername *string `json:"steam_username"`
	SteamPassword *string `json:"steam_password"`
}

// nodeConfigUpdateResponse returns the saved view plus the result of pushing the
// config to the (online) Agent, so the UI can surface reachability immediately.
type nodeConfigUpdateResponse struct {
	nodeConfigView
	Applied     bool   `json:"applied"`      // config was delivered to the Agent
	ApplyOK     bool   `json:"apply_ok"`     // configured target(s) reachable
	ApplyDetail string `json:"apply_detail"` // status / error detail from the Agent
}

func (s *Server) handleUpdateNodeConfig(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	node, err := s.store.GetNode(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "node not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "could not get node")
		return
	}

	var req updateNodeConfigRequest
	if derr := decodeJSON(r, &req); derr != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.BackupTarget != nil {
		t := strings.TrimSpace(*req.BackupTarget)
		if !validBackupTargets[t] {
			writeError(w, http.StatusBadRequest, "backup_target must be one of local|share|sftp")
			return
		}
	}

	ctx := r.Context()
	c, err := s.nodeConfigOrEmpty(ctx, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load node config")
		return
	}

	// Trim non-secret fields; leave the SFTP password / PEM verbatim.
	setTrim(&c.BackupTarget, req.BackupTarget)
	setTrim(&c.BackupDir, req.BackupDir)
	setTrim(&c.SftpHost, req.SftpHost)
	setTrim(&c.SftpUser, req.SftpUser)
	setRaw(&c.SftpPassword, req.SftpPassword)
	setRaw(&c.SftpPrivateKey, req.SftpPrivateKey)
	setTrim(&c.SftpBasePath, req.SftpBasePath)
	setTrim(&c.SftpKnownHostKey, req.SftpKnownHostKey)
	if req.ReplicateToSftp != nil {
		c.ReplicateToSftp = *req.ReplicateToSftp
	}
	setTrim(&c.SteamUsername, req.SteamUsername)
	setRaw(&c.SteamPassword, req.SteamPassword)

	// Reject unknown dynamic-naming tokens before persisting (the Agent only
	// expands {{SLUG}}); a bad token would otherwise become a literal directory.
	if err := validateBackupPathTokens(c.BackupDir); err != nil {
		writeError(w, http.StatusBadRequest, "backup_dir: "+err.Error())
		return
	}
	if err := validateBackupPathTokens(c.SftpBasePath); err != nil {
		writeError(w, http.StatusBadRequest, "sftp_base_path: "+err.Error())
		return
	}

	if err := s.store.SaveNodeConfig(ctx, id, c); err != nil {
		writeError(w, http.StatusInternalServerError, "could not save node config")
		return
	}
	s.recordAudit(r, http.StatusOK, "node-config:"+id)

	// Push to the Agent so the change takes effect now (and acts as a connectivity
	// test). A push failure isn't fatal — the config is saved and re-pushed on the
	// next reconcile.
	resp := nodeConfigUpdateResponse{nodeConfigView: toNodeConfigView(c)}
	if node.Status == cluster.NodeOnline {
		ok, detail, perr := s.pushNodeConfig(ctx, node)
		resp.Applied = perr == nil
		resp.ApplyOK = ok
		if perr != nil {
			resp.ApplyDetail = perr.Error()
		} else {
			resp.ApplyDetail = detail
		}
	} else {
		resp.ApplyDetail = "node offline; config will apply on next contact"
	}
	writeJSON(w, http.StatusOK, resp)
}

// pushNodeConfig delivers a node's stored config to its Agent via ApplyNodeConfig.
// It returns (true, "", nil) when there's nothing to push (no stored config).
func (s *Server) pushNodeConfig(ctx context.Context, n *cluster.Node) (ok bool, detail string, err error) {
	cfg, gerr := s.store.GetNodeConfig(ctx, n.ID)
	if errors.Is(gerr, store.ErrNotFound) {
		return true, "", nil
	}
	if gerr != nil {
		return false, "", gerr
	}
	client, cerr := s.nodes.Client(n.Address)
	if cerr != nil {
		return false, "", cerr
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	resp, aerr := client.ApplyNodeConfig(cctx, &agentpb.ApplyNodeConfigRequest{Config: nodeConfigToProto(cfg)})
	if aerr != nil {
		return false, "", aerr
	}
	return resp.Ok, resp.Detail, nil
}

// setTrim assigns *dst from a non-nil request pointer, trimming whitespace.
func setTrim(dst *string, src *string) {
	if src != nil {
		*dst = strings.TrimSpace(*src)
	}
}

// setRaw assigns *dst from a non-nil request pointer verbatim (for secrets/PEM
// where surrounding whitespace may be significant).
func setRaw(dst *string, src *string) {
	if src != nil {
		*dst = *src
	}
}
