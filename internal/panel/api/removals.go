package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"google.golang.org/grpc/codes"
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
//     PendingRemoval carrying the operator's delete_data intent. The delete
//     itself still goes through — an operator with a dead node can still tidy
//     their server list.
//   - The reconciler finishes. Every node pass replays the pending removals of
//     each node that answered, and drops a record once the Agent confirms it.
//   - The Agent never decides. It adopts what it finds running, as before; a
//     pending removal is the operator's own command executed late, and nothing
//     here asks an Agent to destroy data on a guess.
//
// A container orphaned before any of this existed is cleared by hand from the
// node band: handleRetireNodeContainer, which never touches data.

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

// agentUnreachable reports whether a failed RPC means the Agent was not there
// to answer, as opposed to an Agent that answered with a failure.
func agentUnreachable(err error) bool {
	if errors.Is(err, errNodeNotLive) {
		return true
	}
	switch status.Code(err) {
	case codes.Unavailable, codes.DeadlineExceeded:
		return true
	}
	return false
}

// finishPendingRemovals replays the removals owed to each of nodes — the ones
// the pass just reached — one goroutine per node so a slow removal on one does
// not hold up the rest, and returns once all of them have settled, which is
// also what keeps two passes from replaying the same removal at once.
func (s *Server) finishPendingRemovals(ctx context.Context, nodes []*cluster.Node) {
	var wg sync.WaitGroup
	for _, n := range nodes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.finishNodeRemovals(ctx, n)
		}()
	}
	wg.Wait()
}

// finishNodeRemovals replays one node's pending removals and records how each
// went: a confirmed one is dropped, a failed one keeps its place with the
// attempt counted and the reason updated.
func (s *Server) finishNodeRemovals(ctx context.Context, n *cluster.Node) {
	// Work from the stored record, not the pass's snapshot: a delete may have
	// queued a removal since the node list was read. Nothing owed is the common
	// case, and costs this one read.
	fresh, err := s.store.GetNode(ctx, n.ID)
	if err != nil || len(fresh.PendingRemovals) == 0 {
		return
	}
	client, err := s.nodes.Client(fresh.DialTarget())
	if err != nil {
		return
	}
	done := map[string]bool{}
	failed := map[string]string{}
	for _, p := range fresh.PendingRemovals {
		if s.serverClaimsID(ctx, fresh.ID, p.ServerID) {
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
			s.logger.Warn("pending server removal failed again; will retry on the next node pass",
				"node", fresh.ID, "server", p.ServerID, "delete_data", p.DeleteData, "attempts", p.Attempts+1, "err", rerr)
			continue
		}
		done[p.ServerID] = true
		s.logger.Info("pending server removal finished on its node",
			"node", fresh.ID, "server", p.ServerID, "delete_data", p.DeleteData, "requested_at", p.RequestedAt)
	}
	if len(done) == 0 && len(failed) == 0 {
		return
	}
	// Re-read once more before writing: the RPCs took time, and the record is
	// shared with deletes and the reconcile that ran just before this.
	latest, err := s.store.GetNode(ctx, n.ID)
	if err != nil {
		return
	}
	for id := range done {
		latest.DropPendingRemoval(id)
	}
	for i := range latest.PendingRemovals {
		if reason, ok := failed[latest.PendingRemovals[i].ServerID]; ok {
			latest.PendingRemovals[i].Attempts++
			latest.PendingRemovals[i].LastError = reason
		}
	}
	if err := s.store.UpdateNode(ctx, latest); err != nil {
		s.logger.Error("could not record the outcome of pending server removals", "node", n.ID, "err", err)
	}
}

// serverClaimsID reports whether a Panel server row placed on nodeID carries
// serverID — the test for "this container is the Panel's, not an orphan".
func (s *Server) serverClaimsID(ctx context.Context, nodeID, serverID string) bool {
	sv, err := s.store.GetServer(ctx, serverID)
	return err == nil && sv.NodeID == nodeID
}

// settleNodeAfterDelete releases a deleted server's allocation on its node
// and, when its removal did not land, records it as owed. One read-modify-write
// on a fresh copy of the record, taken after the RPC, so neither the release
// nor the pending removal is lost to a write made while the RPC was in flight.
func (s *Server) settleNodeAfterDelete(ctx context.Context, sv *store.Server, stale *cluster.Node, removeErr error) {
	n := stale
	if fresh, err := s.store.GetNode(ctx, stale.ID); err == nil {
		n = fresh
	}
	ports := make([]int, 0, len(sv.Ports))
	for _, p := range sv.Ports {
		ports = append(ports, p)
	}
	n.Release(sv.MemoryMB, ports)
	if removeErr != nil {
		n.AddPendingRemoval(cluster.PendingRemoval{
			ServerID:    sv.ID,
			DeleteData:  true,
			RequestedAt: time.Now().UTC(),
			Attempts:    1,
			LastError:   removalErrorText(removeErr),
		})
		s.logger.Warn("server deleted but its removal did not reach the node; queued for the node reconciler",
			"server", sv.ID, "name", sv.Name, "node", n.ID, "err", removeErr)
	}
	if err := s.store.UpdateNode(ctx, n); err != nil {
		s.logger.Error("could not update node after server delete", "node", n.ID, "server", sv.ID, "err", err)
	}
}

// retireServerIDPattern is what a server id handed to the retire endpoint may
// look like. The id travels to the Agent, which uses it to name a container and
// a spec file on disk, so it is held to the characters a Panel-minted id uses
// rather than trusted as a path segment.
var retireServerIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

// handleRetireNodeContainer stops and removes a container the node reports for
// a server id the Panel has no row for on that node — the orphan the untracked
// badge names — and has the Agent forget its spec so the watchdog never adopts
// it again. The server's data is left exactly where it is: this is cleanup of a
// runtime, not a delete, and no one is asked to type a name to confirm it.
//
// DELETE /nodes/{id}/containers/{serverID}. Needs server.delete (the route) and
// node.manage (checked here): the container belongs to no server, so there is
// no owner to authorize against, and acting on what a node runs outside the
// Panel's books is node administration.
func (s *Server) handleRetireNodeContainer(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if role := roleFrom(ctx); role == nil || !role.Has(rbac.PermNodeManage) {
		writeCodedError(w, http.StatusForbidden, "missing permission: "+string(rbac.PermNodeManage), "forbidden")
		return
	}
	serverID := chi.URLParam(r, "serverID")
	if !retireServerIDPattern.MatchString(serverID) {
		writeCodedError(w, http.StatusBadRequest, "invalid server id", "invalid_server_id")
		return
	}
	n, err := s.store.GetNode(ctx, chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		writeCodedError(w, http.StatusNotFound, "node not found", "node_not_found")
		return
	}
	if err != nil {
		writeCodedError(w, http.StatusInternalServerError, "could not get node", "internal")
		return
	}
	switch sv, gerr := s.store.GetServer(ctx, serverID); {
	case gerr == nil && sv.NodeID == n.ID:
		writeCodedError(w, http.StatusConflict,
			fmt.Sprintf("server %q is managed by the Panel on this node — delete the server instead", sv.Name), "server_tracked")
		return
	case gerr != nil && !errors.Is(gerr, store.ErrNotFound):
		writeCodedError(w, http.StatusInternalServerError, "could not check for a server with that id", "internal")
		return
	}
	if err := s.removeOnNode(ctx, n, serverID, false); err != nil {
		if agentUnreachable(err) {
			writeCodedError(w, http.StatusServiceUnavailable,
				fmt.Sprintf("node %s is unreachable: %s", nodeLabel(n), removalErrorText(err)), "node_unreachable")
			return
		}
		writeCodedError(w, http.StatusBadGateway, "agent could not retire the container: "+removalErrorText(err), "agent_error")
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

// writeCodedError writes the {"error","code"} envelope: the message for a
// person, the code for a client that has to branch on what went wrong.
func writeCodedError(w http.ResponseWriter, status int, msg, code string) {
	writeJSON(w, status, map[string]string{"error": msg, "code": code})
}
