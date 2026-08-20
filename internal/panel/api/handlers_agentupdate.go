package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/briggleman/kraken/internal/panel/agentbin"
	"github.com/briggleman/kraken/internal/panel/cluster"
	"github.com/briggleman/kraken/internal/panel/store"
	"github.com/briggleman/kraken/internal/shared/agentpb"
	"github.com/briggleman/kraken/internal/shared/version"
)

// updateChunkSize is how much of the agent binary rides in each gRPC message.
// Well under the 4MB default message cap, big enough that a ~17MB binary is
// a few dozen messages.
const updateChunkSize = 512 * 1024

// handleNodeAgentUpdate pushes this Panel's embedded agent binary to a node
// over the UpdateAgent RPC. The Panel can only push its own version — release
// builds embed the agent builds compiled alongside the Panel — so this is
// "bring the node to the Panel's version", never an arbitrary install.
func (s *Server) handleNodeAgentUpdate(w http.ResponseWriter, r *http.Request) {
	n, err := s.store.GetNode(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "node not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not get node")
		return
	}

	from, to, err := s.pushAgentUpdateTo(r.Context(), n)
	if err != nil {
		s.recordAudit(r, http.StatusBadGateway, "node-agent-update:"+n.ID)
		var ae *agentUpdateError
		if errors.As(err, &ae) {
			writeError(w, ae.status, ae.msg)
			return
		}
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	s.recordAudit(r, http.StatusOK, "node-agent-update:"+n.ID)
	writeJSON(w, http.StatusOK, map[string]any{
		"from_version": from,
		"to_version":   to,
		// The agent restarts right after responding; the node reconciler
		// observes the new version (or the rollback) on its next pass.
		"restarting": true,
	})
}

// agentUpdateError carries an HTTP status alongside the message so the
// single-node handler can preserve its precise codes (409 already-current,
// 503 no embedded binary) while pushAgentUpdateTo stays reusable by the wave.
type agentUpdateError struct {
	status int
	msg    string
}

func (e *agentUpdateError) Error() string { return e.msg }

// embeddedAgentSHA returns the SHA-256 of the Panel's embedded agent binary for
// a node's platform, or "" when none is embedded (dev build) or arch is unknown.
// Used for artifact-identity skew detection (#93).
func (s *Server) embeddedAgentSHA(os cluster.NodeOS, arch string) string {
	if arch == "" {
		arch = "amd64"
	}
	// Cached in agentbin — the node list polls this per node on a 15s loop, and
	// the embedded binaries never change at runtime.
	return agentbin.SHA(string(os), arch)
}

// pushAgentUpdateTo streams the Panel's embedded agent build to one node and
// waits for the agent's swap-and-restart ack. Shared by the single-node
// handler and the update-all wave. Returns (fromVersion, toVersion) on success.
func (s *Server) pushAgentUpdateTo(ctx context.Context, n *cluster.Node) (string, string, error) {
	// Fresh NodeInfo first: confirms the agent is reachable and gives the
	// authoritative os/arch/version to select the binary (and to refuse a
	// pointless push).
	info, err := s.reconcileNode(ctx, n)
	if err != nil {
		return "", "", &agentUpdateError{http.StatusBadGateway, "agent unreachable: " + err.Error()}
	}
	if info.AgentVersion == version.Version {
		return "", "", &agentUpdateError{http.StatusConflict, "agent is already at the panel's version (" + version.Version + ")"}
	}
	arch := info.Arch
	if arch == "" {
		// An agent old enough to not report its arch predates the update RPC
		// too; amd64 is the overwhelmingly likely platform, and the push below
		// fails cleanly with Unimplemented if the agent truly can't do this.
		arch = "amd64"
	}

	data, sha, err := agentbin.Get(info.Os, arch)
	if errors.Is(err, agentbin.ErrNotEmbedded) {
		return "", "", &agentUpdateError{http.StatusServiceUnavailable,
			"this panel build has no embedded agent binary for " + info.Os + "/" + arch + " — release builds embed them; dev builds do not"}
	}
	if err != nil {
		return "", "", &agentUpdateError{http.StatusInternalServerError, "load embedded agent: " + err.Error()}
	}

	client, err := s.nodes.Client(n.DialTarget())
	if err != nil {
		return "", "", &agentUpdateError{http.StatusBadGateway, "agent connection: " + err.Error()}
	}
	// Generous bound: the binary is ~17MB and may cross a WAN.
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	stream, err := client.UpdateAgent(cctx)
	if err != nil {
		return "", "", &agentUpdateError{http.StatusBadGateway, "open update stream: " + err.Error()}
	}
	if err := stream.Send(&agentpb.UpdateAgentChunk{Payload: &agentpb.UpdateAgentChunk_Meta_{Meta: &agentpb.UpdateAgentChunk_Meta{
		Version:   version.Version,
		Sha256:    sha,
		TotalSize: int64(len(data)),
		Os:        info.Os,
		Arch:      arch,
	}}}); err != nil {
		return "", "", &agentUpdateError{http.StatusBadGateway, "send update metadata: " + err.Error()}
	}
	for off := 0; off < len(data); off += updateChunkSize {
		end := min(off+updateChunkSize, len(data))
		if err := stream.Send(&agentpb.UpdateAgentChunk{Payload: &agentpb.UpdateAgentChunk_Data{Data: data[off:end]}}); err != nil {
			return "", "", &agentUpdateError{http.StatusBadGateway, "stream binary: " + err.Error()}
		}
	}
	resp, err := stream.CloseAndRecv()
	if err != nil {
		// The agent's refusal reasons (container, platform mismatch, checksum,
		// already-updating) arrive as gRPC status messages — surface verbatim,
		// they are the operator's whole diagnosis.
		return "", "", &agentUpdateError{http.StatusBadGateway, "agent rejected update: " + err.Error()}
	}

	s.logger.Info("agent update pushed", "node", n.ID, "name", n.Name,
		"from", resp.FromVersion, "to", resp.ToVersion, "bytes", len(data), "sha256", sha)
	return resp.FromVersion, resp.ToVersion, nil
}
