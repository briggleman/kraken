package api

import (
	"context"
	"errors"
	"fmt"
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

// handleNodeAgentUpdate starts a push of this Panel's embedded agent binary to a
// node. The Panel can only push its own version — release builds embed the agent
// builds compiled alongside the Panel — so this is "bring the node to the
// Panel's version", never an arbitrary install.
//
// It answers 202 and streams in the background. The push moves ~17MB and writes
// nothing downstream while it runs, so holding the request open put it at the
// mercy of every proxy read timeout in the path (Cloudflare ~100s, nginx 60s):
// the operator collected a 502 for a push that had often succeeded, and the
// retry then collided with the update already in flight. Preflight stays
// synchronous — a 409 or 503 is worth an immediate answer, not a poll — and only
// the stream is deferred.
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

	// Name the job already in flight rather than letting the operator start a
	// second push and read the Agent's refusal as a new problem.
	if running, ok := s.agentJobs.running(n.ID); ok {
		s.recordAudit(r, http.StatusConflict, "node-agent-update:"+n.ID)
		writeError(w, http.StatusConflict,
			"an update is already pushing to this node (job "+running.ID+")")
		return
	}

	plan, err := s.prepareAgentUpdate(r.Context(), n)
	if err != nil {
		status := http.StatusBadGateway
		var ae *agentUpdateError
		if errors.As(err, &ae) {
			status = ae.status
		}
		// The audit entry records the status the caller actually received —
		// it used to say 502 even when the response was a 409 or 503.
		s.recordAudit(r, status, "node-agent-update:"+n.ID)
		writeError(w, status, err.Error())
		return
	}

	job := s.agentJobs.start(n.ID, n.Name, plan.fromVersion, version.Version, int64(len(plan.data)))
	go s.runAgentUpdate(job.ID, n.ID, n.Name, plan)

	s.recordAudit(r, http.StatusAccepted, "node-agent-update:"+n.ID)
	writeJSON(w, http.StatusAccepted, job.body())
}

// handleNodeAgentUpdateStatus reports the node's most recent push from THIS
// Panel process. A 404 is a real answer, not a failure: it means no job is
// known here — because none was started, because it aged out, or because the
// Panel restarted — and the node record is then the thing to read. The UI
// treats it as "refresh the fleet and trust the version you see".
func (s *Server) handleNodeAgentUpdateStatus(w http.ResponseWriter, r *http.Request) {
	job, ok := s.agentJobs.latest(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, http.StatusNotFound,
			"no agent update job for this node in this panel process — read the node's agent_version")
		return
	}
	writeJSON(w, http.StatusOK, job.body())
}

// runAgentUpdate streams the prepared binary and records the outcome on the job.
//
// It takes no request context on purpose. The HTTP request is already answered
// by the time this runs, so r.Context() is cancelled and would kill the stream
// the moment the 202 went out — the exact bug this whole change exists to avoid.
// Its own deadline is generous: 17MB over a slow WAN is minutes, and the old
// 2-minute bound was itself part of the problem.
func (s *Server) runAgentUpdate(jobID, nodeID, nodeName string, plan *agentUpdatePlan) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	stream, err := plan.client.UpdateAgent(ctx)
	if err != nil {
		s.logger.Error("agent update failed", "node", nodeID, "name", nodeName,
			"from", plan.fromVersion, "to", version.Version, "err", err)
		s.agentJobs.finish(jobID, agentUpdateFailed, "open update stream: "+err.Error())
		return
	}

	resp, err := pushUpdate(stream, plan.meta(), plan.data, func(sent int64) {
		s.agentJobs.progress(jobID, sent)
	})
	if err != nil {
		// The Agent's refusal is the operator's whole diagnosis and it survives
		// nowhere else: writeError does not log, and the audit entry carries
		// only the status. It reaches the UI through the job's error field.
		s.logger.Error("agent update failed", "node", nodeID, "name", nodeName,
			"from", plan.fromVersion, "to", version.Version, "err", err)
		s.agentJobs.finish(jobID, agentUpdateFailed, err.Error())
		return
	}

	s.logger.Info("agent update pushed", "node", nodeID, "name", nodeName,
		"from", resp.FromVersion, "to", resp.ToVersion, "bytes", len(plan.data), "sha256", plan.sha)
	s.agentJobs.finish(jobID, agentUpdateRestarting, "")
}

// updateStream is the subset of the UpdateAgent client stream that pushUpdate
// needs. It exists so the send/recv error handling below — the part that is easy
// to get wrong and impossible to read off the happy path — is testable without a
// gRPC server or an embedded agent binary.
type updateStream interface {
	Send(*agentpb.UpdateAgentChunk) error
	CloseAndRecv() (*agentpb.UpdateAgentResponse, error)
}

// pushUpdate sends the metadata then the binary, and returns the agent's ack.
//
// The subtlety it exists for: gRPC returns io.EOF from Send once the SERVER has
// ended the RPC, and the status saying WHY is only retrievable from
// CloseAndRecv. The agent refuses before it reads a single chunk for every
// reason an operator needs to know — an immutable container binary, a platform
// mismatch, an install directory it cannot write — so returning the Send error
// reports a bare "EOF" and throws the diagnosis away. That is exactly what
// hid an unwritable /usr/local/bin behind "stream binary: EOF".
//
// So: stop sending, then let CloseAndRecv speak. The Send error is only worth
// reporting when the agent had nothing to say for itself.
// onSent, when non-nil, is called with the running total after each accepted
// chunk. It is the only progress signal available: gRPC gives no send-side
// acknowledgement, so "handed to the stream" is the honest measure and the UI
// should label it as such.
func pushUpdate(stream updateStream, meta *agentpb.UpdateAgentChunk_Meta, data []byte, onSent func(sent int64)) (*agentpb.UpdateAgentResponse, error) {
	sendErr := stream.Send(&agentpb.UpdateAgentChunk{
		Payload: &agentpb.UpdateAgentChunk_Meta_{Meta: meta},
	})
	for off := 0; sendErr == nil && off < len(data); off += updateChunkSize {
		end := min(off+updateChunkSize, len(data))
		sendErr = stream.Send(&agentpb.UpdateAgentChunk{
			Payload: &agentpb.UpdateAgentChunk_Data{Data: data[off:end]},
		})
		if sendErr == nil && onSent != nil {
			onSent(int64(end))
		}
	}

	resp, err := stream.CloseAndRecv()
	if err != nil {
		return nil, fmt.Errorf("agent rejected update: %w", err)
	}
	if sendErr != nil {
		// The agent acked a binary it cannot have received in full. Never report
		// that as success — the checksum should have caught it, and if it didn't,
		// something is wrong that a green result would bury.
		return nil, fmt.Errorf("stream binary: %w", sendErr)
	}
	return resp, nil
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

// agentUpdatePlan is everything preflight resolved, ready for a stream that may
// outlive the request that asked for it. It deliberately holds no context: the
// pushing goroutine makes its own (see runAgentUpdate).
type agentUpdatePlan struct {
	client      agentpb.NodeServiceClient
	data        []byte
	sha         string
	os          string
	arch        string
	fromVersion string
}

func (p *agentUpdatePlan) meta() *agentpb.UpdateAgentChunk_Meta {
	return &agentpb.UpdateAgentChunk_Meta{
		Version:   version.Version,
		Sha256:    p.sha,
		TotalSize: int64(len(p.data)),
		Os:        p.os,
		Arch:      p.arch,
	}
}

// prepareAgentUpdate does everything that must answer the operator immediately:
// confirm the agent is reachable, refuse a pointless or impossible push, select
// and load the binary, and get a client. All of it is fast, and each failure has
// a precise status the caller should not have to poll for.
//
// It stops short of opening the stream. That belongs to the pushing goroutine,
// because the stream's lifetime has to be the push's lifetime and not the
// request's.
func (s *Server) prepareAgentUpdate(ctx context.Context, n *cluster.Node) (*agentUpdatePlan, error) {
	// Fresh NodeInfo first: confirms the agent is reachable and gives the
	// authoritative os/arch/version to select the binary (and to refuse a
	// pointless push).
	info, err := s.reconcileNode(ctx, n)
	if err != nil {
		// 503, not 502: Cloudflare (and some other edges) replace an origin
		// 502/504 body with their own HTML error page, which destroys the
		// diagnosis this message carries — observed live, the UI received an
		// unparseable page and an empty reason. A 503 passes through intact,
		// and "the service cannot do this right now" is the truer meaning.
		return nil, &agentUpdateError{http.StatusServiceUnavailable, "agent unreachable: " + err.Error()}
	}
	if info.AgentVersion == version.Version {
		return nil, &agentUpdateError{http.StatusConflict, "agent is already at the panel's version (" + version.Version + ")"}
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
		return nil, &agentUpdateError{http.StatusServiceUnavailable,
			"this panel build has no embedded agent binary for " + info.Os + "/" + arch + " — release builds embed them; dev builds do not"}
	}
	if err != nil {
		return nil, &agentUpdateError{http.StatusInternalServerError, "load embedded agent: " + err.Error()}
	}

	client, err := s.nodes.Client(n.DialTarget())
	if err != nil {
		// 503 for the same reason as the unreachable branch above.
		return nil, &agentUpdateError{http.StatusServiceUnavailable, "agent connection: " + err.Error()}
	}

	return &agentUpdatePlan{
		client:      client,
		data:        data,
		sha:         sha,
		os:          info.Os,
		arch:        arch,
		fromVersion: info.AgentVersion,
	}, nil
}
