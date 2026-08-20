package api

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/crypto/ssh"

	"github.com/briggleman/kraken/internal/panel/cluster"
	"github.com/briggleman/kraken/internal/panel/store"
	"github.com/briggleman/kraken/internal/shared/spec"
)

// sftpStatusView is the SFTP panel state for a server. It never includes the
// password (only whether one is set); the plaintext is returned once, on reset.
type sftpStatusView struct {
	Enabled     bool     `json:"enabled"`
	Username    string   `json:"username"`
	Host        string   `json:"host,omitempty"`
	Port        int      `json:"port,omitempty"`
	HasPassword bool     `json:"has_password"`
	Keys        []string `json:"keys"`
	// Tunneled: the hosting node is reached over a reverse tunnel. When the
	// Panel-side SFTP proxy is fronting the node, Host/Port already point at
	// the proxy endpoint and Proxied is true; otherwise the node's own SFTP
	// listener is LAN-local and the UI says so.
	Tunneled bool `json:"tunneled,omitempty"`
	// Proxied: Host/Port address the Panel-side SFTP proxy, reachable wherever
	// the Panel is, rather than the node's LAN-local listener.
	Proxied bool `json:"proxied,omitempty"`
}

// sftpStatusView is assembled by (*Server).sftpStatus so it can consult the
// live SFTP proxy for a tunnel node's Panel-side port + advertised host.
func (s *Server) sftpStatus(r *http.Request, sv *store.Server, node *cluster.Node) sftpStatusView {
	v := sftpStatusView{Username: sv.ID, Keys: []string{}}
	if sv.SFTP != nil {
		v.Enabled = sv.SFTP.Enabled
		v.HasPassword = sv.SFTP.PasswordHash != ""
		if len(sv.SFTP.Keys) > 0 {
			v.Keys = sv.SFTP.Keys
		}
	}
	if node != nil {
		v.Host = node.PublicHost
		v.Port = node.SFTPPort
		v.Tunneled = node.Tunneled()
		// A tunnel node fronted by the proxy advertises the proxy endpoint,
		// which is reachable wherever the Panel is — the whole point of #90.
		if node.Tunneled() && s.sftpProxy != nil {
			if port := s.sftpProxy.port(node.ID); port != 0 {
				v.Host = s.sftpProxyHost(r)
				v.Port = port
				v.Proxied = true
			}
		}
	}
	return v
}

// sftpProxyHost is the hostname operators should point their SFTP client at for
// proxied nodes: the configured advertised host, else the host from the request
// (the Panel they're already talking to).
func (s *Server) sftpProxyHost(r *http.Request) string {
	if h := s.sftpProxy.host; h != "" {
		return h
	}
	if host, _, err := net.SplitHostPort(r.Host); err == nil && host != "" {
		return host
	}
	return r.Host
}

func (s *Server) handleGetServerSFTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sv, err := s.store.GetServer(ctx, chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "server not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load server")
		return
	}
	if !s.authorizeServer(w, ctx, sv) {
		return
	}
	node, _ := s.store.GetNode(ctx, sv.NodeID)
	writeJSON(w, http.StatusOK, s.sftpStatus(r, sv, node))
}

// sftpTarget loads the server (+ spec, node, agent client) for a mutating SFTP
// request, enforcing ownership. ok=false means an error response was written.
func (s *Server) sftpTarget(w http.ResponseWriter, r *http.Request) (*store.Server, *spec.Spec, *cluster.Node, bool) {
	sv, sp, ok := s.loadServerAndSpec(r.Context(), w, chi.URLParam(r, "id"))
	if !ok {
		return nil, nil, nil, false
	}
	node, err := s.store.GetNode(r.Context(), sv.NodeID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load hosting node")
		return nil, nil, nil, false
	}
	return sv, sp, node, true
}

// pushSFTP saves the server and re-pushes its spec so the Agent picks up the new
// SFTP credentials immediately (best-effort — persisted regardless).
func (s *Server) pushSFTP(ctx context.Context, sv *store.Server, sp *spec.Spec, node *cluster.Node) error {
	if err := s.store.UpdateServer(ctx, sv); err != nil {
		return err
	}
	if client, err := s.nodes.Client(node.DialTarget()); err == nil {
		s.rePushServerSpec(ctx, client, sv, sp)
	}
	return nil
}

func (s *Server) handleResetServerSFTPPassword(w http.ResponseWriter, r *http.Request) {
	sv, sp, node, ok := s.sftpTarget(w, r)
	if !ok {
		return
	}
	pw, err := genSFTPPassword()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not generate password")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not hash password")
		return
	}
	if sv.SFTP == nil {
		sv.SFTP = &store.ServerSFTP{}
	}
	sv.SFTP.PasswordHash = string(hash)
	sv.SFTP.Enabled = true
	if err := s.pushSFTP(r.Context(), sv, sp, node); err != nil {
		writeError(w, http.StatusInternalServerError, "could not save SFTP credentials")
		return
	}
	s.recordAudit(r, http.StatusOK, "sftp-password:"+sv.ID)
	// The plaintext password is returned exactly once — it is never stored.
	writeJSON(w, http.StatusOK, map[string]any{"password": pw, "status": s.sftpStatus(r, sv, node)})
}

type sftpKeysRequest struct {
	Keys []string `json:"keys"`
}

func (s *Server) handleSetServerSFTPKeys(w http.ResponseWriter, r *http.Request) {
	var req sftpKeysRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	// Validate + normalize each authorized key; reject anything unparseable.
	keys := make([]string, 0, len(req.Keys))
	for _, k := range req.Keys {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(k))
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid public key: "+k)
			return
		}
		keys = append(keys, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub))))
	}
	sv, sp, node, ok := s.sftpTarget(w, r)
	if !ok {
		return
	}
	if sv.SFTP == nil {
		sv.SFTP = &store.ServerSFTP{}
	}
	sv.SFTP.Keys = keys
	if len(keys) > 0 {
		sv.SFTP.Enabled = true
	}
	if err := s.pushSFTP(r.Context(), sv, sp, node); err != nil {
		writeError(w, http.StatusInternalServerError, "could not save SFTP keys")
		return
	}
	s.recordAudit(r, http.StatusOK, "sftp-keys:"+sv.ID)
	writeJSON(w, http.StatusOK, s.sftpStatus(r, sv, node))
}

func (s *Server) handleDisableServerSFTP(w http.ResponseWriter, r *http.Request) {
	sv, sp, node, ok := s.sftpTarget(w, r)
	if !ok {
		return
	}
	if sv.SFTP != nil {
		sv.SFTP.Enabled = false
	}
	if err := s.pushSFTP(r.Context(), sv, sp, node); err != nil {
		writeError(w, http.StatusInternalServerError, "could not update SFTP")
		return
	}
	s.recordAudit(r, http.StatusOK, "sftp-disable:"+sv.ID)
	writeJSON(w, http.StatusOK, s.sftpStatus(r, sv, node))
}

// genSFTPPassword returns a strong URL-safe random password (~24 chars).
func genSFTPPassword() (string, error) {
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
