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
// ensureNodeLive answers that in one bounded probe.
func (s *Server) removeOnNode(ctx context.Context, n *cluster.Node, serverID string, deleteData bool) error {
	if err := s.ensureNodeLive(ctx, n); err != nil {
		return fmt.Errorf("%w: %v", errNodeNotLive, err)
	}
	client, err := s.nodes.Client(n.DialTarget())
	if err != nil {
		return fmt.Errorf("%w: %v", errNodeNotLive, err)
	}
	rctx, cancel := context.WithTimeout(ctx, removeServerTimeout)
	defer cancel()
	_, err = client.RemoveServer(rctx, &agentpb.RemoveServerRequest{ServerId: serverID, DeleteData: deleteData})
	return err
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
			// A server row on this node answers to the id again. Until the
			// retire/revive model (#360) says what that means, the removal
			// waits rather than destroy what may be a live server.
			continue
		}
		rctx, cancel := context.WithTimeout(ctx, removeServerTimeout)
		_, rerr := client.RemoveServer(rctx, &agentpb.RemoveServerRequest{ServerId: p.ServerID, DeleteData: p.DeleteData})
		cancel()
		if rerr != nil {
			failed[p.ServerID] = removalErrorText(rerr)
			continue
		}
		done[p.ServerID] = true
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
	for id := range done {
		if p, ok := latest.PendingRemovalFor(id); ok {
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
func (s *Server) serverClaimsID(ctx context.Context, nodeID, serverID string) (bool, error) {
	sv, err := s.store.GetServer(ctx, serverID)
	switch {
	case err == nil:
		return sv.NodeID == nodeID, nil
	case errors.Is(err, store.ErrNotFound):
		return false, nil
	default:
		return false, err
	}
}

// settleNodeAfterDelete records the outcome of a delete's removal on the
// node: a removal that landed releases the server's allocation now; one that
// did not is remembered as a pending removal that carries the allocation
// until it lands. One read-modify-write on a fresh copy taken after the RPC,
// so nothing is lost to a write made while the RPC was in flight.
//
// An error means the outcome could not be recorded, and the caller must not
// delete the server: a delete the Panel cannot remember does not happen.
func (s *Server) settleNodeAfterDelete(ctx context.Context, sv *store.Server, nodeID string, removeErr error) error {
	n, err := s.store.GetNode(ctx, nodeID)
	if err != nil {
		return fmt.Errorf("reload node: %w", err)
	}
	ports := make([]int, 0, len(sv.Ports))
	for _, p := range sv.Ports {
		ports = append(ports, p)
	}
	if removeErr == nil {
		// A retried delete can find a pending removal an earlier attempt
		// recorded (its removal failed, then the row delete failed and the row
		// stayed). That record holds this allocation; finishing it is the
		// release, and releasing here as well would free the ports a second
		// time — possibly out from under a server placed on them since.
		if !n.FinishPendingRemoval(sv.ID) {
			n.Release(sv.MemoryMB, ports)
		}
	} else {
		now := time.Now().UTC()
		n.AddPendingRemoval(cluster.PendingRemoval{
			ServerID:    sv.ID,
			DeleteData:  true,
			RequestedAt: now,
			Attempts:    1,
			LastError:   removalErrorText(removeErr),
			NextAttempt: now.Add(cluster.RemovalBackoff(1)),
			MemoryMB:    sv.MemoryMB,
			Ports:       ports,
		})
	}
	if err := s.store.UpdateNode(ctx, n); err != nil {
		return fmt.Errorf("save node: %w", err)
	}
	if removeErr != nil {
		s.logger.Warn("server deleted but its removal did not reach the node; queued for the node reconciler",
			"server", sv.ID, "name", sv.Name, "node", n.ID, "held_memory_mb", sv.MemoryMB, "held_ports", ports, "err", removeErr)
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
		writeCoded(w, http.StatusConflict, "removal_pending",
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
	if err := s.removeOnNode(ctx, n, serverID, false); err != nil {
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
		for _, c := range fresh.ManagedContainers {
			if c.ServerID != serverID {
				kept = append(kept, c)
			}
		}
		if removed := len(fresh.ManagedContainers) - len(kept); removed > 0 {
			fresh.ManagedContainers = kept
			if len(kept) == 0 {
				fresh.ManagedContainers = nil
			}
			fresh.RunningServers = max(0, fresh.RunningServers-removed)
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
