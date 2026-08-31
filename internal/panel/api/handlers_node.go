package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"

	"github.com/briggleman/kraken/internal/panel/cloudflare"
	"github.com/briggleman/kraken/internal/panel/cluster"
	"github.com/briggleman/kraken/internal/panel/store"
	"github.com/briggleman/kraken/internal/shared/agentpb"
	"github.com/briggleman/kraken/internal/shared/mtls"
	"github.com/briggleman/kraken/internal/shared/version"
)

// agentIdentityFromPeer extracts the Panel-minted agent identity from the TLS
// serving cert observed on a gRPC call. Empty for plaintext (dev) and
// tunnel-routed connections (whose identity lives on the tunnel session).
func agentIdentityFromPeer(p *peer.Peer) string {
	if p == nil || p.AuthInfo == nil {
		return ""
	}
	ti, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok || len(ti.State.PeerCertificates) == 0 {
		return ""
	}
	return mtls.AgentIdentityFromCert(ti.State.PeerCertificates[0])
}

// defaultPortRange is the game-port pool a node gets when registration doesn't
// specify one. A node must never be created with an empty pool — the scheduler
// can't allocate ports from it, which makes the node permanently unschedulable.
// Operators running several nodes on one IP should still give each its own
// range (the panel allocates per node and doesn't know they share an IP).
var defaultPortRange = cluster.PortRange{Start: 28000, End: 28999}

type registerNodeRequest struct {
	Name        string `json:"name"` // optional; taken from the agent (KRAKEN_NODE_ID) when blank
	OS          string `json:"os"`   // "linux" | "windows"; optional, agent-reported when blank
	WineEnabled bool   `json:"wine_enabled"`
	Address     string `json:"address"`     // Agent gRPC host:port (direct mode)
	PublicHost  string `json:"public_host"` // optional; players' connect host (else auto-detected)
	TotalMemMB  int    `json:"total_memory_mb"`
	PortStart   int    `json:"port_start"`
	PortEnd     int    `json:"port_end"`

	// ConnectionMode selects the transport: "direct" (default; the Panel dials
	// Address) or "tunnel" (the Agent dials out; no Address needed).
	ConnectionMode string `json:"connection_mode,omitempty"`
	// TunnelID binds a tunnel-mode node to the agent identity minted at
	// enrollment (surfaced by the enroll-status endpoint). Required for tunnel
	// mode; the tunnel listener only accepts a session whose client cert
	// carries this identity.
	TunnelID string `json:"tunnel_id,omitempty"`
}

func (s *Server) handleRegisterNode(w http.ResponseWriter, r *http.Request) {
	var req registerNodeRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid node body")
		return
	}
	mode := cluster.ConnectionMode(req.ConnectionMode)
	if mode == "" {
		mode = cluster.ConnDirect
	}
	switch mode {
	case cluster.ConnDirect:
		if req.Address == "" {
			writeError(w, http.StatusBadRequest, "address is required")
			return
		}
	case cluster.ConnTunnel:
		if req.TunnelID == "" {
			writeError(w, http.StatusBadRequest, "tunnel_id is required for a tunnel-mode node (it comes from the agent's enrollment)")
			return
		}
		if s.tunnel == nil {
			writeError(w, http.StatusBadRequest, "the reverse-tunnel listener is disabled on this Panel (set KRAKEN_TUNNEL_ADDR)")
			return
		}
		// One node per identity: the binding is the whole security story.
		if nodes, err := s.store.ListNodes(r.Context()); err == nil {
			for _, ex := range nodes {
				if ex.TunnelID == req.TunnelID {
					writeError(w, http.StatusConflict, "another node ("+ex.Name+") is already bound to this tunnel identity")
					return
				}
			}
		}
	default:
		writeError(w, http.StatusBadRequest, "connection_mode must be 'direct' or 'tunnel'")
		return
	}
	// Identity comes from the agent itself when omitted, adopted on first
	// reconcile rather than by dialing inside this request (#106). Registration
	// must never 5xx because an agent was slow to answer — the record is the
	// durable thing, contact is eventually consistent (the same philosophy
	// tunnel registration always had). A blank name registers as a placeholder
	// derived from the address and identityPending=true; the first successful
	// reconcile adopts the agent's KRAKEN_NODE_ID and OS.
	identityPending := false
	if req.Name == "" {
		req.Name = placeholderNodeName(req.Address, mode)
		identityPending = true
	}
	if req.OS == "" {
		// Corrected from the agent's report on first contact. Tunnel nodes have
		// no address to probe; the Add Node dialog sends the install tab's OS
		// (#112), so a blank here is a raw-API caller either way.
		req.OS = string(cluster.OSLinux)
	}
	os := cluster.NodeOS(req.OS)
	if os != cluster.OSLinux && os != cluster.OSWindows {
		writeError(w, http.StatusBadRequest, "os must be 'linux' or 'windows'")
		return
	}
	// Wine is a property of the game image (the wine runtime ships in the
	// container), not the host — every Linux node can run linux-wine specs.
	req.WineEnabled = os == cluster.OSLinux
	n := &cluster.Node{
		ID:              uuid.NewString(),
		Name:            req.Name,
		OS:              os,
		WineEnabled:     req.WineEnabled,
		Status:          cluster.NodeOffline, // until first successful contact
		Address:         req.Address,
		PublicHost:      req.PublicHost,
		TotalMemoryMB:   req.TotalMemMB,
		ConnectionMode:  mode,
		TunnelID:        req.TunnelID,
		IdentityPending: identityPending,
	}
	if req.PortStart > 0 && req.PortEnd >= req.PortStart {
		n.Ports = cluster.NewPortPool(cluster.PortRange{Start: req.PortStart, End: req.PortEnd})
	} else {
		n.Ports = cluster.NewPortPool(defaultPortRange)
	}
	if err := s.store.CreateNode(r.Context(), n); err != nil {
		writeError(w, http.StatusInternalServerError, "could not register node")
		return
	}
	s.logger.Info("node registered", "id", n.ID, "name", n.Name, "addr", n.Address, "mode", mode, "identity_pending", identityPending)
	// No first-contact reconcile is kicked here: it would be a fire-and-forget
	// writer racing every operator action that can land in the seconds after
	// registration (cordon, mode flip, delete) — it reads the node, dials the
	// agent (slow), then persists a status that clobbers whatever was set in
	// between. The periodic node reconciler brings a fresh node online on its
	// next pass (and the connect-node UI polls node-info right after
	// registering, which reconciles on demand), so prompt online doesn't need a
	// racing writer here. #106 only requires that registration not block or
	// 5xx on a slow agent, which it no longer does.
	writeJSON(w, http.StatusCreated, n)
}

// placeholderNodeName derives a temporary node name for a record registered
// before its agent could report KRAKEN_NODE_ID (#106). The first reconcile
// replaces it. Kept human-readable so a never-connecting node is identifiable.
func placeholderNodeName(address string, mode cluster.ConnectionMode) string {
	if mode == cluster.ConnTunnel || address == "" {
		return "pending-node"
	}
	host := address
	if h, _, err := net.SplitHostPort(address); err == nil && h != "" {
		host = h
	}
	return "node-" + host
}

// maxNodeNameLen bounds a node name so it stays renderable in the node band
// and the logs it appears in.
const maxNodeNameLen = 64

// updateNodeRequest carries the operator-editable capacity fields. Pointer
// fields distinguish "leave unchanged" (absent) from an explicit value.
type updateNodeRequest struct {
	// Name renames the node. The name is display-only — servers reference a
	// node by its UUID — so a rename is safe at any time and does not disturb
	// what is running.
	Name *string `json:"name,omitempty"`

	TotalMemMB *int `json:"total_memory_mb,omitempty"`
	PortStart  *int `json:"port_start,omitempty"`
	PortEnd    *int `json:"port_end,omitempty"`

	// ConnectionMode flips an existing node between "direct" and "tunnel"
	// without re-registering (the design's promised non-destructive flip).
	// Tunnel needs the node's TunnelID binding, which the reconciler captures
	// from the agent's serving cert on contact — so the natural sequence is:
	// re-enroll the agent for an identity-bearing cert (if it predates them),
	// let one reconcile pass land, then flip.
	ConnectionMode *string `json:"connection_mode,omitempty"`
	// Address optionally accompanies a flip to direct for nodes registered in
	// tunnel mode, which have no stored address to dial.
	Address *string `json:"address,omitempty"`
}

// handleUpdateNode edits a node's name and schedulable capacity — total memory
// and the game-port range — and flips its connection mode. Port-range changes
// preserve existing allocations, so running servers keep their ports; only
// future placements draw from the new range.
func (s *Server) handleUpdateNode(w http.ResponseWriter, r *http.Request) {
	var req updateNodeRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid node body")
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
	if n.Ports == nil {
		n.Ports = cluster.NewPortPool()
	}

	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			writeError(w, http.StatusBadRequest, "name cannot be empty")
			return
		}
		if len(name) > maxNodeNameLen {
			writeError(w, http.StatusBadRequest,
				fmt.Sprintf("name cannot exceed %d characters", maxNodeNameLen))
			return
		}
		// A rename also settles the identity question: the record is no longer
		// waiting to adopt the agent's self-reported id, or the operator's
		// chosen name would be silently overwritten on the next reconcile.
		n.Name = name
		n.IdentityPending = false
	}

	if req.TotalMemMB != nil {
		if *req.TotalMemMB <= 0 {
			writeError(w, http.StatusBadRequest, "total_memory_mb must be positive")
			return
		}
		if *req.TotalMemMB < n.AllocatedMemoryMB {
			writeError(w, http.StatusBadRequest,
				fmt.Sprintf("total_memory_mb cannot be below the %dMB already reserved by servers on this node", n.AllocatedMemoryMB))
			return
		}
		n.TotalMemoryMB = *req.TotalMemMB
	}

	if (req.PortStart == nil) != (req.PortEnd == nil) {
		writeError(w, http.StatusBadRequest, "port_start and port_end must be set together")
		return
	}
	if req.PortStart != nil {
		start, end := *req.PortStart, *req.PortEnd
		if start < 1 || end > 65535 || end < start {
			writeError(w, http.StatusBadRequest, "port range must satisfy 1 <= start <= end <= 65535")
			return
		}
		n.Ports.SetRanges(cluster.PortRange{Start: start, End: end})
	}

	if req.Address != nil && *req.Address != "" {
		n.Address = *req.Address
	}
	if req.ConnectionMode != nil {
		switch cluster.ConnectionMode(*req.ConnectionMode) {
		case cluster.ConnTunnel:
			if s.tunnel == nil {
				writeError(w, http.StatusBadRequest, "the reverse-tunnel listener is disabled on this Panel (set KRAKEN_TUNNEL_ADDR)")
				return
			}
			// The binding must already exist: either set at registration, or
			// captured by the reconciler from the agent's identity-bearing
			// cert. Without it any enrolled agent could claim this node.
			if n.TunnelID == "" {
				writeError(w, http.StatusBadRequest,
					"node has no tunnel identity yet — re-enroll the agent (its cert predates per-node identities), let it reconnect once, then flip")
				return
			}
			n.ConnectionMode = cluster.ConnTunnel
		case cluster.ConnDirect:
			if n.Address == "" {
				writeError(w, http.StatusBadRequest, "flipping to direct needs an address (host:port) — include it in this request")
				return
			}
			n.ConnectionMode = cluster.ConnDirect
		default:
			writeError(w, http.StatusBadRequest, "connection_mode must be 'direct' or 'tunnel'")
			return
		}
	}

	if err := s.store.UpdateNode(r.Context(), n); err != nil {
		writeError(w, http.StatusInternalServerError, "could not update node")
		return
	}
	s.logger.Info("node updated", "id", n.ID, "name", n.Name,
		"total_memory_mb", n.TotalMemoryMB, "ports", n.Ports.Ranges, "mode", n.ConnectionMode)
	writeJSON(w, http.StatusOK, n)
}

func (s *Server) handleListNodes(w http.ResponseWriter, r *http.Request) {
	nodes, err := s.store.ListNodes(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list nodes")
		return
	}
	// panel_version rides along so the UI can flag agents whose build doesn't match
	// the Panel's. Agents track the Panel (Panel↔Agent is a versioned gRPC
	// contract, and both are built from the same tag), so "matches the Panel" is
	// the definition of up to date. panel_agent_sha carries the embedded agent
	// build's hash per platform so the UI can flag on artifact identity (#93) —
	// a panel-only release leaves it unchanged and the fleet stays unflagged.
	resp := map[string]any{"nodes": nodes, "panel_version": version.Version}
	if sha := s.embeddedAgentSHA(cluster.OSLinux, "amd64"); sha != "" {
		resp["panel_agent_sha"] = map[string]string{
			"linux/amd64":   sha,
			"linux/arm64":   s.embeddedAgentSHA(cluster.OSLinux, "arm64"),
			"windows/amd64": s.embeddedAgentSHA(cluster.OSWindows, "amd64"),
		}
	}
	writeJSON(w, http.StatusOK, resp)
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

// handleDeleteNode removes a node record. It refuses while servers are still
// placed here, because nothing else would: servers.node_id carries no foreign
// key, so the delete would succeed and leave rows pointing at a node that no
// longer exists — and those rows are unrecoverable, since a re-enrolled node is
// minted a fresh id rather than reclaiming its old one. The orphans also stop
// being reconciled (reconcileOnce needs GetNode to succeed), so they sit
// claiming whatever state they were last in, forever.
//
// The check lives here rather than in a constraint on purpose: the in-memory
// store would orphan identically, and a foreign key would cover only Postgres.
func (s *Server) handleDeleteNode(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if servers, lerr := s.store.ListServers(r.Context()); lerr == nil {
		placed := 0
		for _, sv := range servers {
			if sv.NodeID == id {
				placed++
			}
		}
		if placed > 0 {
			noun := "servers"
			if placed == 1 {
				noun = "server"
			}
			writeError(w, http.StatusConflict, fmt.Sprintf(
				"%d %s still placed on this node — delete or move them first", placed, noun))
			return
		}
	}
	err := s.store.DeleteNode(r.Context(), id)
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

// handleCordonNode holds a node back from new placements without touching what
// already runs on it. Cordon is durable operator intent (persisted, honored by
// reconcile), distinct from the health states.
func (s *Server) handleCordonNode(w http.ResponseWriter, r *http.Request) {
	s.setNodeCordon(w, r, true)
}

// handleUncordonNode releases the hold; the node's status reverts to whatever
// the next reconcile observes (online/partial/offline).
func (s *Server) handleUncordonNode(w http.ResponseWriter, r *http.Request) {
	s.setNodeCordon(w, r, false)
}

func (s *Server) setNodeCordon(w http.ResponseWriter, r *http.Request, cordon bool) {
	n, err := s.store.GetNode(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "node not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not get node")
		return
	}
	n.Cordoned = cordon
	// Reflect the intent immediately so the UI doesn't wait for the next
	// reconcile: a healthy node flips online<->cordoned now; a partial or
	// offline node keeps its real (worse) health, which cordon never masks.
	if cordon {
		if n.Status == cluster.NodeOnline {
			n.Status = cluster.NodeCordoned
		}
	} else if n.Status == cluster.NodeCordoned {
		n.Status = cluster.NodeOnline
	}
	if err := s.store.UpdateNode(r.Context(), n); err != nil {
		writeError(w, http.StatusInternalServerError, "could not update node")
		return
	}
	s.logger.Info("node cordon changed", "node", n.ID, "name", n.Name, "cordoned", cordon)
	s.recordAudit(r, http.StatusOK, "node-cordon:"+n.ID)
	writeJSON(w, http.StatusOK, n)
}

// reconcileNode dials the node's Agent over gRPC, marks it online, and adopts
// the Agent-authoritative facts the Panel may not know yet: detected public host
// (when unset), OS, and total memory (when unset). On failure it marks the node
// offline (persisted) and returns the error. The live NodeInfo is returned on
// success. Shared by the node-info handler and the quickstart auto-register path.
func (s *Server) reconcileNode(ctx context.Context, n *cluster.Node) (*agentpb.NodeInfo, error) {
	client, err := s.nodes.Client(n.DialTarget())
	if err != nil {
		return nil, err
	}
	// Capture the DNS target host before we adopt any Agent-reported changes, so we
	// can detect when published records have gone stale (PublicHost/ExternalIP move).
	oldDNSHost := serverExternalHost(n)
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var peerInfo peer.Peer
	info, err := client.GetNodeInfo(cctx, &agentpb.GetNodeInfoRequest{}, grpc.Peer(&peerInfo))
	if err != nil {
		// The Agent itself is unreachable, so whatever it last said about its
		// container runtime is no longer knowable — drop it rather than leave a
		// stale reason attached to an offline node.
		if n.Status != cluster.NodeOffline || n.RuntimeError != "" {
			s.logger.Info("node unreachable", "node", n.ID, "name", n.Name, "addr", n.Address, "err", err)
			n.Status = cluster.NodeOffline
			n.RuntimeError = ""
			_ = s.store.UpdateNode(ctx, n)
		}
		return nil, err
	}
	changed := false
	// The Agent answered, so it is up — but an Agent that can't reach Docker can't
	// run anything. Distinguish the two: online means "ready for work", partial
	// means "reachable, runtime down" (unschedulable, with the reason attached).
	// RUNTIME_STATUS_UNSPECIFIED comes from an Agent built before the field existed
	// and is read as healthy, so a version-skewed fleet doesn't go all-partial.
	status := cluster.NodeOnline
	runtimeErr := ""
	if info.GetRuntimeStatus() == agentpb.RuntimeStatus_RUNTIME_STATUS_UNAVAILABLE {
		status = cluster.NodePartial
		runtimeErr = info.GetRuntimeError()
	}
	// A cordon is operator intent that outlives a reconcile: a reachable,
	// runtime-healthy cordoned node stays cordoned (not flipped back to online),
	// but a partial or offline one still reports its real health so the operator
	// isn't misled. Uncordon restores normal reconcile-driven status.
	if n.Cordoned && status == cluster.NodeOnline {
		status = cluster.NodeCordoned
	}
	if n.Status != status {
		s.logger.Info("node status changed", "node", n.ID, "name", n.Name, "from", n.Status, "to", status, "runtime_err", runtimeErr)
		n.Status = status
		changed = true
	}
	if n.RuntimeError != runtimeErr {
		n.RuntimeError = runtimeErr
		changed = true
	}
	if info.AgentVersion != "" && n.AgentVersion != info.AgentVersion {
		n.AgentVersion = info.AgentVersion
		changed = true
	}
	if info.BinarySha256 != "" && n.AgentSHA != info.BinarySha256 {
		n.AgentSHA = info.BinarySha256
		changed = true
	}
	if info.Arch != "" && n.Arch != info.Arch {
		n.Arch = info.Arch
		changed = true
	}
	// Adopted rather than derived: this is the Agent's count of its own managed
	// containers, and comparing it to the servers the Panel placed here is what
	// surfaces containers the Panel has lost track of.
	if n.RunningServers != int(info.RunningServers) {
		n.RunningServers = int(info.RunningServers)
		changed = true
	}
	if n.LastUpdateError != info.LastUpdateError {
		n.LastUpdateError = info.LastUpdateError
		changed = true
	}
	// Capture the agent's Panel-minted identity from its serving cert (the
	// URI SAN). This is what lets a direct-mode node flip to tunnel mode later
	// without re-enrolling: the binding already exists. Only ever filled in,
	// never overwritten — the identity in an existing binding is load-bearing.
	if n.TunnelID == "" {
		if id := agentIdentityFromPeer(&peerInfo); id != "" {
			n.TunnelID = id
			changed = true
		}
	}
	// Adopt the agent's self-reported node id for a record registered before
	// the agent could be probed (#106): registration used a placeholder name so
	// it never had to block on a slow agent, and first contact is where the
	// real KRAKEN_NODE_ID lands. Only while pending, and only once.
	if n.IdentityPending {
		if info.NodeId != "" {
			n.Name = info.NodeId
		}
		n.IdentityPending = false
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
	// Wine capability is derived, not configured: the wine runtime ships in the
	// game image, so every Linux node supports linux-wine (and no Windows node does).
	if wine := n.OS == cluster.OSLinux; n.WineEnabled != wine {
		n.WineEnabled = wine
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
	// Rotate the agent's mTLS cert when it nears expiry (best-effort, throttled).
	s.maybeRotateAgentCert(ctx, n, client, info)
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
		"panel_version":   version.Version,
		"total_memory_mb": info.TotalMemoryMb,
		"running_servers": info.RunningServers,
		"host":            info.Host,
		"public_host":     n.PublicHost,
		// The Agent answered; status distinguishes a node ready for work from one
		// whose container runtime is down (see reconcileNode).
		"status":        string(n.Status),
		"runtime_error": n.RuntimeError,
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
	client, err := s.nodes.Client(n.DialTarget())
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
