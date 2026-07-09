package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/briggleman/kraken/internal/panel/cloudflare"
	"github.com/briggleman/kraken/internal/panel/cluster"
	"github.com/briggleman/kraken/internal/panel/store"
	"github.com/briggleman/kraken/internal/shared/agentpb"
)

type registerNodeRequest struct {
	Name        string `json:"name"` // optional; taken from the agent (KRAKEN_NODE_ID) when blank
	OS          string `json:"os"`   // "linux" | "windows"; optional, agent-reported when blank
	WineEnabled bool   `json:"wine_enabled"`
	Address     string `json:"address"`     // Agent gRPC host:port
	PublicHost  string `json:"public_host"` // optional; players' connect host (else auto-detected)
	TotalMemMB  int    `json:"total_memory_mb"`
	PortStart   int    `json:"port_start"`
	PortEnd     int    `json:"port_end"`
}

func (s *Server) handleRegisterNode(w http.ResponseWriter, r *http.Request) {
	var req registerNodeRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid node body")
		return
	}
	if req.Address == "" {
		writeError(w, http.StatusBadRequest, "address is required")
		return
	}
	// Identity comes from the agent itself when omitted: dial it and adopt its
	// self-reported node id / OS / Wine capability. Keeps registration to a
	// single field (address) and makes the agent's KRAKEN_NODE_ID authoritative.
	if req.Name == "" || req.OS == "" {
		client, err := s.nodes.Client(req.Address)
		if err != nil {
			writeError(w, http.StatusBadGateway, "could not connect to agent: "+err.Error())
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		info, err := client.GetNodeInfo(ctx, &agentpb.GetNodeInfoRequest{})
		cancel()
		if err != nil {
			writeError(w, http.StatusBadGateway,
				"agent unreachable at "+req.Address+" — it must be running and reachable to auto-detect the node identity "+
					"(check the agent is started and the host firewall allows inbound TCP on the agent port), "+
					"or supply name and os explicitly: "+err.Error())
			return
		}
		if req.Name == "" {
			req.Name = info.NodeId
		}
		if req.OS == "" {
			req.OS = info.Os
			req.WineEnabled = info.WineEnabled
		}
	}
	os := cluster.NodeOS(req.OS)
	if os != cluster.OSLinux && os != cluster.OSWindows {
		writeError(w, http.StatusBadRequest, "os must be 'linux' or 'windows'")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required (agent did not report one)")
		return
	}
	n := &cluster.Node{
		ID:            uuid.NewString(),
		Name:          req.Name,
		OS:            os,
		WineEnabled:   req.WineEnabled,
		Status:        cluster.NodeOffline, // until first successful contact
		Address:       req.Address,
		PublicHost:    req.PublicHost,
		TotalMemoryMB: req.TotalMemMB,
	}
	if req.PortStart > 0 && req.PortEnd >= req.PortStart {
		n.Ports = cluster.NewPortPool(cluster.PortRange{Start: req.PortStart, End: req.PortEnd})
	} else {
		n.Ports = cluster.NewPortPool()
	}
	if err := s.store.CreateNode(r.Context(), n); err != nil {
		writeError(w, http.StatusInternalServerError, "could not register node")
		return
	}
	s.logger.Info("node registered", "id", n.ID, "name", n.Name, "addr", n.Address)
	writeJSON(w, http.StatusCreated, n)
}

func (s *Server) handleListNodes(w http.ResponseWriter, r *http.Request) {
	nodes, err := s.store.ListNodes(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list nodes")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"nodes": nodes})
}

func (s *Server) handleGetNode(w http.ResponseWriter, r *http.Request) {
	n, err := s.store.GetNode(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "node not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not get node")
		return
	}
	writeJSON(w, http.StatusOK, n)
}

func (s *Server) handleDeleteNode(w http.ResponseWriter, r *http.Request) {
	err := s.store.DeleteNode(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "node not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not delete node")
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

// reconcileNode dials the node's Agent over gRPC, marks it online, and adopts
// the Agent-authoritative facts the Panel may not know yet: detected public host
// (when unset), OS, and total memory (when unset). On failure it marks the node
// offline (persisted) and returns the error. The live NodeInfo is returned on
// success. Shared by the node-info handler and the quickstart auto-register path.
func (s *Server) reconcileNode(ctx context.Context, n *cluster.Node) (*agentpb.NodeInfo, error) {
	client, err := s.nodes.Client(n.Address)
	if err != nil {
		return nil, err
	}
	// Capture the DNS target host before we adopt any Agent-reported changes, so we
	// can detect when published records have gone stale (PublicHost/ExternalIP move).
	oldDNSHost := serverExternalHost(n)
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	info, err := client.GetNodeInfo(cctx, &agentpb.GetNodeInfoRequest{})
	if err != nil {
		if n.Status != cluster.NodeOffline {
			n.Status = cluster.NodeOffline
			_ = s.store.UpdateNode(ctx, n)
		}
		return nil, err
	}
	changed := false
	if n.Status != cluster.NodeOnline {
		n.Status = cluster.NodeOnline
		changed = true
	}
	if n.PublicHost == "" && info.Host != "" {
		n.PublicHost = info.Host
		changed = true
	}
	// The Agent is authoritative about its own OS; correct a stale/guessed value.
	if os := cluster.NodeOS(info.Os); (os == cluster.OSLinux || os == cluster.OSWindows) && os != n.OS {
		n.OS = os
		changed = true
	}
	// Backfill capacity the operator didn't supply (e.g. the quickstart local node).
	if n.TotalMemoryMB == 0 && info.TotalMemoryMb > 0 {
		n.TotalMemoryMB = int(info.TotalMemoryMb)
		changed = true
	}
	// Adopt the outward-facing IP (used for DNS + connect address): the Agent's
	// egress echo by default, overridden by the UniFi gateway's WAN IP when configured.
	ext := info.ExternalIp
	if wan := s.unifiWANIP(ctx); wan != "" {
		ext = wan
	}
	if ext != "" && n.ExternalIP != ext {
		n.ExternalIP = ext
		changed = true
	}
	// Adopt the Agent's SFTP port so the Files tab can show connection details.
	if int(info.SftpPort) != n.SFTPPort {
		n.SFTPPort = int(info.SftpPort)
		changed = true
	}
	if changed {
		_ = s.store.UpdateNode(ctx, n)
	}
	// If the node's player-facing host moved (e.g. a new WAN IP), the A/CNAME
	// records we published for its servers now point at the old address — re-point
	// them. Best-effort; only fires on an actual change.
	if newHost := serverExternalHost(n); newHost != "" && newHost != oldDNSHost {
		s.reconcileNodeDNS(ctx, n.ID, newHost)
	}
	// Deliver the node's Panel-managed config (backup target + replication). The
	// Agent keeps this only in memory, so re-pushing on each reconcile restores it
	// after an Agent restart. Best-effort: failures don't fail reconcile.
	if _, _, perr := s.pushNodeConfig(ctx, n); perr != nil {
		s.logger.Warn("could not push node config", "node", n.ID, "err", perr)
	}
	return info, nil
}

// reconcileNodeDNS re-points the host (A/CNAME) record of every server on the
// node to newHost, fixing records that went stale when the node's public host
// changed. SRV records reference the name (not the host), so they're unaffected.
// Best-effort: a Cloudflare failure on one server is logged and skipped.
func (s *Server) reconcileNodeDNS(ctx context.Context, nodeID, newHost string) {
	token := s.cloudflareToken(ctx)
	if token == "" {
		return
	}
	servers, err := s.store.ListServers(ctx)
	if err != nil {
		return
	}
	cf := cloudflare.New(token)
	for _, sv := range servers {
		if sv.NodeID != nodeID || sv.DNS == nil || len(sv.DNS.RecordIDs) == 0 {
			continue
		}
		// RecordIDs[0] is the host (A/CNAME) record created first in handleSetServerDNS.
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		if uerr := cf.UpdateHostRecord(cctx, sv.DNS.ZoneID, sv.DNS.RecordIDs[0], sv.DNS.Name, newHost); uerr != nil {
			s.logger.Warn("could not re-point stale DNS record", "server", sv.ID, "name", sv.DNS.Name, "host", newHost, "err", uerr)
		} else {
			s.logger.Info("re-pointed server DNS to node's new host", "server", sv.ID, "name", sv.DNS.Name, "host", newHost)
		}
		cancel()
	}
}

// handleNodeInfo dials the Agent over gRPC and returns its live NodeInfo. It
// also reconciles the stored node's status (online on success). This is the
// end-to-end proof that the Panel can reach and command an Agent.
func (s *Server) handleNodeInfo(w http.ResponseWriter, r *http.Request) {
	n, err := s.store.GetNode(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "node not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not get node")
		return
	}

	info, err := s.reconcileNode(r.Context(), n)
	if err != nil {
		writeError(w, http.StatusBadGateway, "agent unreachable: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"node_id":         info.NodeId,
		"os":              info.Os,
		"wine_enabled":    info.WineEnabled,
		"agent_version":   info.AgentVersion,
		"total_memory_mb": info.TotalMemoryMb,
		"running_servers": info.RunningServers,
		"host":            info.Host,
		"public_host":     n.PublicHost,
	})
}

type serverPowerRequest struct {
	Action string `json:"action"` // start | stop | restart | kill
}

var powerActions = map[string]agentpb.PowerAction{
	"start":   agentpb.PowerAction_POWER_ACTION_START,
	"stop":    agentpb.PowerAction_POWER_ACTION_STOP,
	"restart": agentpb.PowerAction_POWER_ACTION_RESTART,
	"kill":    agentpb.PowerAction_POWER_ACTION_KILL,
}

// handleServerPower forwards a power action to the Agent that hosts the server.
func (s *Server) handleServerPower(w http.ResponseWriter, r *http.Request) {
	var req serverPowerRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	action, ok := powerActions[req.Action]
	if !ok {
		writeError(w, http.StatusBadRequest, "action must be one of start|stop|restart|kill")
		return
	}
	n, err := s.store.GetNode(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "node not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not get node")
		return
	}
	// Object-level authz: only the owner (or a server.any role) may power a server.
	sv, err := s.store.GetServer(r.Context(), chi.URLParam(r, "serverID"))
	if err != nil {
		writeError(w, http.StatusNotFound, "server not found")
		return
	}
	if !s.authorizeServer(w, r.Context(), sv) {
		return
	}
	client, err := s.nodes.Client(n.Address)
	if err != nil {
		writeError(w, http.StatusBadGateway, "could not connect to agent: "+err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	resp, err := client.PowerAction(ctx, &agentpb.PowerActionRequest{
		ServerId: chi.URLParam(r, "serverID"),
		Action:   action,
	})
	if err != nil {
		writeError(w, http.StatusBadGateway, "agent error: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"state": resp.State.String()})
}
