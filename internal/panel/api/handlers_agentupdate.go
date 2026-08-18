package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/briggleman/kraken/internal/panel/agentbin"
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

	// Fresh NodeInfo first: confirms the agent is reachable and gives the
	// authoritative os/arch/version to select the binary (and to refuse a
	// pointless push).
	info, err := s.reconcileNode(r.Context(), n)
	if err != nil {
		writeError(w, http.StatusBadGateway, "agent unreachable: "+err.Error())
		return
	}
	if info.AgentVersion == version.Version {
		writeError(w, http.StatusConflict, "agent is already at the panel's version ("+version.Version+")")
		return
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
		writeError(w, http.StatusServiceUnavailable,
			"this panel build has no embedded agent binary for "+info.Os+"/"+arch+" — release builds embed them; dev builds do not")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "load embedded agent: "+err.Error())
		return
	}

	client, err := s.nodes.Client(n.Address)
	if err != nil {
		writeError(w, http.StatusBadGateway, "agent connection: "+err.Error())
		return
	}
	// Generous bound: the binary is ~17MB and may cross a WAN.
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	stream, err := client.UpdateAgent(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, "open update stream: "+err.Error())
		return
	}
	if err := stream.Send(&agentpb.UpdateAgentChunk{Payload: &agentpb.UpdateAgentChunk_Meta_{Meta: &agentpb.UpdateAgentChunk_Meta{
		Version:   version.Version,
		Sha256:    sha,
		TotalSize: int64(len(data)),
		Os:        info.Os,
		Arch:      arch,
	}}}); err != nil {
		writeError(w, http.StatusBadGateway, "send update metadata: "+err.Error())
		return
	}
	for off := 0; off < len(data); off += updateChunkSize {
		end := min(off+updateChunkSize, len(data))
		if err := stream.Send(&agentpb.UpdateAgentChunk{Payload: &agentpb.UpdateAgentChunk_Data{Data: data[off:end]}}); err != nil {
			writeError(w, http.StatusBadGateway, "stream binary: "+err.Error())
			return
		}
	}
	resp, err := stream.CloseAndRecv()
	if err != nil {
		// The agent's refusal reasons (container, platform mismatch, checksum,
		// already-updating) arrive as gRPC status messages — surface verbatim,
		// they are the operator's whole diagnosis.
		s.recordAudit(r, http.StatusBadGateway, "node-agent-update:"+n.ID)
		writeError(w, http.StatusBadGateway, "agent rejected update: "+err.Error())
		return
	}

	s.logger.Info("agent update pushed", "node", n.ID, "name", n.Name,
		"from", resp.FromVersion, "to", resp.ToVersion, "bytes", len(data), "sha256", sha)
	s.recordAudit(r, http.StatusOK, "node-agent-update:"+n.ID)
	writeJSON(w, http.StatusOK, map[string]any{
		"from_version": resp.FromVersion,
		"to_version":   resp.ToVersion,
		"bytes":        len(data),
		// The agent restarts right after responding; the node reconciler
		// observes the new version (or the rollback) on its next pass.
		"restarting": true,
	})
}
