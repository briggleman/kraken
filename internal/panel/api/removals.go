package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"google.golang.org/grpc/status"

	"github.com/briggleman/kraken/internal/panel/cluster"
	"github.com/briggleman/kraken/internal/panel/rbac"
	"github.com/briggleman/kraken/internal/panel/store"
	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// Removing a server from its node is one RPC, and before #354 nothing stood
// behind it: the Panel discarded the result and deleted its row regardless, so
// a delete that never reached the Agent left a container nothing owned — which
// the Agent's watchdog then re-adopted on every restart and kept alive forever.
//
// The model now:
//
//   - The Panel remembers. A removal the Agent could not be told about (the node
//     was not live, or RemoveServer failed) is recorded on the node record as a
//     PendingRemoval carrying the operator's delete_data intent and the memory
//     and ports the server held, which stay allocated until the removal lands.
//     The delete itself still goes through — an operator with a dead node can
//     still tidy their server list — but only once it is remembered: a delete
//     the Panel could not record does not happen.
//   - The reconciler finishes. Every node pass hands each node that answered to
//     a replay that runs beside the pass, never inside it, and retries the
//     removals that are due, backing off on failure; a confirmed one is dropped
//     and its allocation released.
//   - The Agent never decides. It adopts what it finds running, as before; a
//     pending removal is the operator's own command executed late, and nothing
//     here asks an Agent to destroy data on a guess.
//
// A container orphaned before any of this existed is cleared by hand from the
// node band: handleRetireNodeContainer, which never touches data. A removal
// that will never land (the node is gone for good, the operator cleaned up by
// hand) is dismissed with handleDismissPendingRemoval.

// removeServerTimeout bounds one RemoveServer call. The Agent waits for both
// container names to come free (~8s each at worst) and then deletes the data
// dir, which on a large tree can outlast this — the deletion carries on
// Agent-side, and the retry that follows finds it done.
const removeServerTimeout = 30 * time.Second

// errNodeNotLive marks a removal that was never attempted because the node's
// Agent could not be reached.
var errNodeNotLive = errors.New("node unreachable")

// removeOnNode asks the node's Agent to remove a server, reporting why it could
// not. A node the Panel cannot reach is not dialled for the full RPC budget:
// ensureNodeLive answers that in one bounded probe. deleteBackups is the
// permanent delete of a retired server (#360); the response says what the
// Agent did with the archives.
func (s *Server) removeOnNode(ctx context.Context, n *cluster.Node, serverID string, deleteData, deleteBackups bool) (*agentpb.RemoveServerResponse, error) {
	if err := s.ensureNodeLive(ctx, n); err != nil {
		return nil, fmt.Errorf("%w: %v", errNodeNotLive, err)
	}
	client, err := s.nodes.Client(n.DialTarget())
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errNodeNotLive, err)
	}
	rctx, cancel := context.WithTimeout(ctx, removeServerTimeout)
	defer cancel()
	return client.RemoveServer(rctx, &agentpb.RemoveServerRequest{
		ServerId: serverID, DeleteData: deleteData, DeleteBackups: deleteBackups,
	})
}

// backupsKeptNote is the sentence an operator reads about a server's archives
// after a removal that asked for them to be deleted, or "" when they all went.
func backupsKeptNote(resp *agentpb.RemoveServerResponse) string {
	switch {
	case resp == nil:
		return ""
	case !resp.GetBackupsHandled():
		return "the node's agent is too old to delete backup archives, so this server's archives were kept on the node"
	case resp.GetBackupsKept() != "":
		return "archives on a shared backup target were kept (" + resp.GetBackupsKept() +
			"): they sit beside other servers' archives and cannot be told apart, so delete them there by hand"
	}
	return ""
}

// removalErrorText is how a failed removal is recorded for an operator: the
// Agent's own message without the gRPC framing around it.
func removalErrorText(err error) string {
	if errors.Is(err, errNodeNotLive) {
		return err.Error()
	}
	return status.Convert(err).Message()
}

// removalReplays is the node reconciler's bookkeeping for pending-removal
// replays: which nodes have one running, and a group a test can wait on.
type removalReplays struct {
	mu       sync.Mutex
	inFlight map[string]bool
	wg       sync.WaitGroup
	// lookupErr is the last server-lookup failure reported per "node/server",
	// so a store that keeps failing is logged loudly once, not every pass.
	lookupErr map[string]string
}

// noteLookupFailure records a failed server lookup for a replay and reports
// whether it is worth a warning: the first failure, or a different reason.
func (r *removalReplays) noteLookupFailure(nodeID, serverID, reason string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := nodeID + "/" + serverID
	if prev, ok := r.lookupErr[key]; ok && prev == reason {
		return false
	}
	if r.lookupErr == nil {
		r.lookupErr = map[string]string{}
	}
	r.lookupErr[key] = reason
	return true
}

// clearLookupFailure forgets a lookup failure once the lookup answers again.
func (r *removalReplays) clearLookupFailure(nodeID, serverID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.lookupErr, nodeID+"/"+serverID)
}

// startPendingRemovals hands each of nodes — the ones the pass just reached —
// to a replay of its own and returns at once. The health pass must never wait
// on it: four hung removals at 30s each would otherwise hold a dead node
// elsewhere at "online" for two minutes. A node whose previous replay is still
// running is skipped, so passes never overlap on one node.
func (s *Server) startPendingRemovals(ctx context.Context, nodes []*cluster.Node) {
	for _, n := range nodes {
		s.replays.mu.Lock()
		if s.replays.inFlight[n.ID] {
			s.replays.mu.Unlock()
			continue
		}
		if s.replays.inFlight == nil {
			s.replays.inFlight = map[string]bool{}
		}
		s.replays.inFlight[n.ID] = true
		s.replays.wg.Add(1)
		s.replays.mu.Unlock()
		go func(id string) {
			defer func() {
				s.replays.mu.Lock()
				delete(s.replays.inFlight, id)
				s.replays.mu.Unlock()
				s.replays.wg.Done()
			}()
			s.finishNodeRemovals(ctx, id)
		}(n.ID)
	}
}

// finishNodeRemovals replays one node's due removals and records how each
// went: a confirmed one is finished (dropped, its allocation released), a
// failed one keeps its place with the attempt counted and the next one pushed
// back.
func (s *Server) finishNodeRemovals(ctx context.Context, nodeID string) {
	// Work from the stored record, not the pass's snapshot: a delete may have
	// queued a removal since the node list was read. Nothing owed is the common
	// case, and costs this one read.
	fresh, err := s.store.GetNode(ctx, nodeID)
	if err != nil || len(fresh.PendingRemovals) == 0 {
		return
	}
	now := time.Now()
	var due []cluster.PendingRemoval
	for _, p := range fresh.PendingRemovals {
		if p.Due(now) {
			due = append(due, p)
		}
	}
	if len(due) == 0 {
		return
	}
	client, err := s.nodes.Client(fresh.DialTarget())
	if err != nil {
		return
	}
	// done maps a delivered removal to whether it carried delete_backups, so a
	// purge folded into the record while the RPC was out is not lost with it.
	done := map[string]bool{}
	failed := map[string]string{}
	for _, p := range due {
		claimed, cerr := s.serverClaimsID(ctx, nodeID, p.ServerID)
		if cerr != nil {
			// Not knowing is not "gone": only a store that says not-found may
			// let a removal that can delete data go ahead. Loud the first time
			// and whenever the reason changes, quiet on the passes between.
			level := slog.LevelDebug
			if s.replays.noteLookupFailure(nodeID, p.ServerID, cerr.Error()) {
				level = slog.LevelWarn
			}
			s.logger.Log(ctx, level, "pending server removal skipped this pass: could not check for a server with its id",
				"node", nodeID, "server", p.ServerID, "err", cerr)
			continue
		}
		s.replays.clearLookupFailure(nodeID, p.ServerID)
		if claimed {
			// A live server row on this node answers to the id — a retire that
			// queued this removal and has not written `retired` yet, or a row
			// someone placed here since. The removal waits rather than destroy
			// what may be a live server.
			continue
		}
		rctx, cancel := context.WithTimeout(ctx, removeServerTimeout)
		resp, rerr := client.RemoveServer(rctx, &agentpb.RemoveServerRequest{
			ServerId: p.ServerID, DeleteData: p.DeleteData, DeleteBackups: p.DeleteBackups,
		})
		cancel()
		if rerr != nil {
			failed[p.ServerID] = removalErrorText(rerr)
			continue
		}
		if p.DeleteBackups {
			// The row is gone, so the log is the only place left to say it.
			if note := backupsKeptNote(resp); note != "" {
				s.logger.Warn("pending permanent delete landed, but not every archive went", "node", nodeID, "server", p.ServerID, "note", note)
			}
		}
		done[p.ServerID] = p.DeleteBackups
	}
	if len(done) == 0 && len(failed) == 0 {
		return
	}
	// Re-read once more before writing: the RPCs took time, and the record is
	// shared with deletes and the reconcile that ran just before this.
	latest, err := s.store.GetNode(ctx, nodeID)
	if err != nil {
		return
	}
	for id, sentBackups := range done {
		if p, ok := latest.PendingRemovalFor(id); ok {
			if p.DeleteBackups && !sentBackups {
				// A permanent delete was folded into this record after the
				// removal went out without delete_backups: what landed is not
				// what is owed now. Kept, and due again at once.
				latest.RetryPendingRemovalNow(id)
				s.logger.Info("pending server removal landed, but a permanent delete was queued onto it meanwhile; sending again",
					"node", nodeID, "server", id)
				continue
			}
			latest.FinishPendingRemoval(id)
			s.logger.Info("pending server removal finished on its node",
				"node", nodeID, "server", id, "delete_data", p.DeleteData, "requested_at", p.RequestedAt,
				"released_memory_mb", p.MemoryMB, "released_ports", p.Ports)
		}
	}
	for id, reason := range failed {
		changed := latest.RecordRemovalFailure(id, reason, now)
		p, _ := latest.PendingRemovalFor(id)
		// Loud when something new is said, quiet otherwise: a node that keeps
		// refusing for the same reason is already on the band and in the log.
		level := slog.LevelDebug
		if changed {
			level = slog.LevelWarn
		}
		s.logger.Log(ctx, level, "pending server removal failed again; will retry",
			"node", nodeID, "server", id, "attempts", p.Attempts, "next_attempt", p.NextAttempt, "err", reason)
	}
	if err := s.store.UpdateNode(ctx, latest); err != nil {
		s.logger.Error("could not record the outcome of pending server removals", "node", nodeID, "err", err)
	}
}

// serverClaimsID reports whether a Panel server row placed on nodeID carries
// serverID — the test for "this container is the Panel's, not an orphan". An
// error other than not-found is returned as-is: the caller cannot tell "no
// row" from "could not look", and must not guess.
//
// A retired row does not claim the node it was retired from (#360): the
// removal owed there is its own retire's (or its permanent delete's) — the
// operator's command, which a replay must be allowed to finish. A removal for
// the id owed on any other node is none of the retire's doing and waits, as it
// does for a row in any other state on the node it is placed on, rather than
// destroy what may be a live server. A revive, which would place the id
// again, is refused while any removal for it is still owed.
func (s *Server) serverClaimsID(ctx context.Context, nodeID, serverID string) (bool, error) {
	sv, err := s.store.GetServer(ctx, serverID)
	switch {
	case err == nil:
		if sv.State == store.StateRetired {
			return nodeID != sv.RetiredFromNodeID, nil
		}
		return sv.NodeID == nodeID, nil
	case errors.Is(err, store.ErrNotFound):
		return false, nil
	default:
		return false, err
	}
}

// nodeRemoval is what a removal owes a node and carries while it is owed: the
// server, the operator's intent, and the allocation the node still holds for
// it (none, for a retired server — its retire released it, or queued a
// removal of its own that holds it).
type nodeRemoval struct {
	serverID, name string
	deleteBackups  bool
	memoryMB       int
	ports          []int
}

// removalOf is the removal a placed server owes its node, holding what it holds.
func removalOf(sv *store.Server) nodeRemoval {
	ports := make([]int, 0, len(sv.Ports))
	for _, p := range sv.Ports {
		ports = append(ports, p)
	}
	return nodeRemoval{serverID: sv.ID, name: sv.Name, memoryMB: sv.MemoryMB, ports: ports}
}

// settleNodeAfterRemoval records the outcome of a removal on the node: a
// removal that landed releases the server's allocation now; one that did not
// is remembered as a pending removal that carries the allocation until it
// lands. One read-modify-write on a fresh copy taken after the RPC, so nothing
// is lost to a write made while the RPC was in flight.
//
// An error means the outcome could not be recorded, and the caller must not go
// on: a retire or delete the Panel cannot remember does not happen.
func (s *Server) settleNodeAfterRemoval(ctx context.Context, rm nodeRemoval, nodeID string, removeErr error) error {
	n, err := s.store.GetNode(ctx, nodeID)
	if err != nil {
		return fmt.Errorf("reload node: %w", err)
	}
	ports := rm.ports
	if removeErr == nil {
		// A retried delete can find a pending removal an earlier attempt
		// recorded (its removal failed, then the row delete failed and the row
		// stayed). That record holds this allocation; finishing it is the
		// release, and releasing here as well would free the ports a second
		// time — possibly out from under a server placed on them since.
		if !n.FinishPendingRemoval(rm.serverID) {
			n.Release(rm.memoryMB, ports)
		}
	} else {
		now := time.Now().UTC()
		n.AddPendingRemoval(cluster.PendingRemoval{
			ServerID:      rm.serverID,
			DeleteData:    true,
			DeleteBackups: rm.deleteBackups,
			RequestedAt:   now,
			Attempts:      1,
			LastError:     removalErrorText(removeErr),
			NextAttempt:   now.Add(cluster.RemovalBackoff(1)),
			MemoryMB:      rm.memoryMB,
			Ports:         ports,
		})
	}
	if err := s.store.UpdateNode(ctx, n); err != nil {
		return fmt.Errorf("save node: %w", err)
	}
	if removeErr != nil {
		s.logger.Warn("server's removal did not reach its node; queued for the node reconciler",
			"server", rm.serverID, "name", rm.name, "node", n.ID, "delete_backups", rm.deleteBackups,
			"held_memory_mb", rm.memoryMB, "held_ports", ports, "err", removeErr)
	}
	return nil
}

// retireServerIDPattern is what a server id handed to the node-scoped
// endpoints may look like. The id travels to the Agent, which uses it to name
// a container and a spec file on disk, so it is held to the characters a
// Panel-minted id uses rather than trusted as a path segment.
var retireServerIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

// nodeScopedServer is the shared front half of the two node-scoped endpoints
// that act on a server id the Panel may not own: node.manage on top of the
// route's server.delete (there is no server owner to authorize against), a
// plain id, and the node. It writes the refusal and returns ok=false.
func (s *Server) nodeScopedServer(w http.ResponseWriter, r *http.Request) (*cluster.Node, string, bool) {
	if role := roleFrom(r.Context()); role == nil || !role.Has(rbac.PermNodeManage) {
		writeCoded(w, http.StatusForbidden, "forbidden", "missing permission: "+string(rbac.PermNodeManage))
		return nil, "", false
	}
	serverID := chi.URLParam(r, "serverID")
	if !retireServerIDPattern.MatchString(serverID) {
		writeCoded(w, http.StatusBadRequest, "invalid_server_id", "invalid server id")
		return nil, "", false
	}
	n, err := s.store.GetNode(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		writeCoded(w, http.StatusNotFound, "node_not_found", "node not found")
		return nil, "", false
	}
	if err != nil {
		writeCoded(w, http.StatusInternalServerError, "internal", "could not get node")
		return nil, "", false
	}
	return n, serverID, true
}

// handleRetireNodeContainer stops and removes a container the node reports for
// a server id the Panel has no row for on that node — the orphan the untracked
// badge names — and has the Agent forget its spec so the watchdog never adopts
// it again. The server's data is left exactly where it is: this is cleanup of a
// runtime, not a delete, and no one is asked to type a name to confirm it.
//
// DELETE /nodes/{id}/containers/{serverID}. Needs server.delete (the route) and
// node.manage (checked here).
func (s *Server) handleRetireNodeContainer(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	n, serverID, ok := s.nodeScopedServer(w, r)
	if !ok {
		return
	}
	// A removal already owed for this id may carry delete_data: retiring the
	// container now would promise the data stays, and the next replay would
	// delete it. The pending removal is the operator's standing order; it wins.
	if _, owed := n.PendingRemovalFor(serverID); owed {
		writeCoded(w, http.StatusConflict, codeRemovalPending,
			"a removal is already pending for this server on this node — it finishes when the node accepts it, or dismiss it first")
		return
	}
	switch claimed, err := s.serverClaimsID(ctx, n.ID, serverID); {
	case err != nil:
		writeCoded(w, http.StatusInternalServerError, "internal", "could not check for a server with that id")
		return
	case claimed:
		writeCoded(w, http.StatusConflict, "server_tracked",
			"a server with this id is managed by the Panel on this node — delete the server instead")
		return
	}
	if _, err := s.removeOnNode(ctx, n, serverID, false, false); err != nil {
		// Never dialled: the liveness probe (or the client) already failed.
		if errors.Is(err, errNodeNotLive) {
			writeCoded(w, http.StatusServiceUnavailable, codeNodeUnreachable,
				fmt.Sprintf("node %s is unreachable: %s", nodeLabel(n), removalErrorText(err)))
			return
		}
		// The RPC itself failed: the one Agent-failure mapping (writeAgentError's)
		// decides the status — 503 for an Agent that did not answer, 500
		// node_error for one that answered with a failure — and a failure the
		// node reported says what it was refusing.
		st, code, msg := agentFailure(err)
		if st != http.StatusServiceUnavailable {
			msg = "the node could not retire the container: " + msg
		}
		writeCoded(w, st, code, msg)
		return
	}
	s.logger.Info("retired an untracked container; its data is untouched", "node", n.ID, "server", serverID)
	// Take it off the node's roll call now rather than a reconcile from now, so
	// the badge the operator acted on clears when the UI refetches.
	if fresh, ferr := s.store.GetNode(ctx, n.ID); ferr == nil {
		kept := fresh.ManagedContainers[:0:0]
		wasRunning := 0
		for _, c := range fresh.ManagedContainers {
			if c.ServerID != serverID {
				kept = append(kept, c)
			} else if c.Running() {
				// The list carries stopped containers too (#385); only the running
				// ones were ever in the Agent's count.
				wasRunning++
			}
		}
		if len(kept) < len(fresh.ManagedContainers) {
			fresh.ManagedContainers = kept
			if len(kept) == 0 {
				fresh.ManagedContainers = nil
			}
			fresh.RunningServers = max(0, fresh.RunningServers-wasRunning)
			_ = s.store.UpdateNode(ctx, fresh)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDismissPendingRemoval forgets a removal owed to a node without
// delivering it — for a node that is gone for good, or a container the
// operator cleared by hand. The allocation the record was holding is released
// with it, so if the container is in fact still running its ports are free to
// be handed out again: retire it, or remove it on the host.
//
// DELETE /nodes/{id}/removals/{serverID}. server.delete + node.manage.
func (s *Server) handleDismissPendingRemoval(w http.ResponseWriter, r *http.Request) {
	n, serverID, ok := s.nodeScopedServer(w, r)
	if !ok {
		return
	}
	p, owed := n.PendingRemovalFor(serverID)
	if !owed {
		writeCoded(w, http.StatusNotFound, "removal_not_found", "no removal is pending for this server on this node")
		return
	}
	n.FinishPendingRemoval(serverID)
	if err := s.store.UpdateNode(r.Context(), n); err != nil {
		writeCoded(w, http.StatusInternalServerError, "internal", "could not update node")
		return
	}
	s.logger.Warn("pending server removal dismissed without reaching the node",
		"node", n.ID, "server", serverID, "delete_data", p.DeleteData, "attempts", p.Attempts,
		"released_memory_mb", p.MemoryMB, "released_ports", p.Ports)
	w.WriteHeader(http.StatusNoContent)
}
