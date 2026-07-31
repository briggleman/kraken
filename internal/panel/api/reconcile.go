package api

import (
	"context"
	"time"

	"github.com/briggleman/kraken/internal/panel/store"
	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// StartReconciler launches a background loop that periodically syncs each
// server's stored lifecycle state with the authoritative state reported by its
// hosting Agent. This is what surfaces watchdog-driven transitions — a crash,
// an auto-restart, or a ready_regex flip from starting→running — in the UI
// without requiring an operator power action. It returns immediately; the loop
// runs until ctx is cancelled.
func (s *Server) StartReconciler(ctx context.Context, interval time.Duration) {
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.reconcileOnce(ctx)
			}
		}
	}()
}

// StartNodeReconciler launches a background loop that polls every registered
// node's Agent and persists its health (online / partial / offline).
//
// Without it a node's status only moved when something asked for it — the Ping
// button, the setup wizard, or the connect-node flow — so a host whose Agent died
// (or whose Docker stopped) went on reading "online" indefinitely, and the
// scheduler kept sending servers to it. The interval is deliberately slower than
// the server reconciler: each pass is a gRPC round trip per node that also
// re-pushes node config and checks cert expiry.
func (s *Server) StartNodeReconciler(ctx context.Context, interval time.Duration) {
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.reconcileNodesOnce(ctx)
			}
		}
	}()
}

func (s *Server) reconcileNodesOnce(ctx context.Context) {
	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		return
	}
	for _, n := range nodes {
		// reconcileNode persists the status transition (including offline on
		// failure), which is the whole point of the poll — the error is expected
		// and already logged there.
		_, _ = s.reconcileNode(ctx, n)
	}
}

// reconcileLiveStates are the states worth polling the Agent about. Offline and
// installing are driven by operator/install flows and left untouched.
func reconcileLive(st store.ServerState) bool {
	switch st {
	case store.StateStarting, store.StateRunning, store.StateStopping, store.StateCrashed:
		return true
	default:
		return false
	}
}

func (s *Server) reconcileOnce(ctx context.Context) {
	servers, err := s.store.ListServers(ctx)
	if err != nil {
		return
	}
	for _, sv := range servers {
		if !reconcileLive(sv.State) {
			continue
		}
		node, err := s.store.GetNode(ctx, sv.NodeID)
		if err != nil {
			continue
		}
		client, err := s.nodes.Client(node.Address)
		if err != nil {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		status, err := client.GetServerStatus(cctx, &agentpb.GetServerStatusRequest{ServerId: sv.ID})
		cancel()
		if err != nil {
			continue
		}
		newState := storeStateFromAgent(status.State)
		// Last-known online-player count (for the fleet list, no stream needed).
		var np, nmax int32
		var nknown bool
		if ls := status.LastStats; ls != nil && ls.PlayersKnown {
			np, nmax, nknown = ls.Players, ls.MaxPlayers, true
		}
		if newState == sv.State && np == sv.Players && nmax == sv.MaxPlayers && nknown == sv.PlayersKnown {
			continue
		}
		sv.State = newState
		sv.Players, sv.MaxPlayers, sv.PlayersKnown = np, nmax, nknown
		if uerr := s.store.UpdateServer(ctx, sv); uerr != nil {
			s.logger.Warn("reconcile: update server failed", "server", sv.ID, "err", uerr)
			continue
		}
	}
}
