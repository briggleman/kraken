package api

import (
	"context"
	"time"

	"github.com/briggleman/kraken/internal/panel/cluster"
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
	var reached []*cluster.Node
	for _, n := range nodes {
		// reconcileNode persists the status transition (including offline on
		// failure), which is the whole point of the poll — the error is expected
		// and already logged there.
		if _, err := s.reconcileNode(ctx, n); err != nil {
			continue
		}
		reached = append(reached, n)
	}
	// Every node that answered can be handed the removals it is owed. Which
	// ones those are is read from the stored record, not this pass's snapshot,
	// so a removal queued a moment ago is not left for the next pass. The
	// replays run beside this pass and are never waited on here.
	s.startPendingRemovals(ctx, reached)
}

// reconcileLive are the states worth polling the Agent about for a running
// server's live facts (state, players, exit code). Installing is driven by the
// install flow and left untouched; the stopped states are handled by
// reconcileAdoptable, which asks a narrower question.
func reconcileLive(st store.ServerState) bool {
	switch st {
	case store.StateStarting, store.StateRunning, store.StateStopping, store.StateCrashed:
		return true
	default:
		return false
	}
}

// reconcileAdoptable are the stopped states the Agent is allowed to contradict
// (#328): the Panel says this server is not running, and if the Agent has a
// managed container running for it, the Agent is right.
//
// The Agent adopts running containers when it boots, so this is what a
// reconnect after a Panel-side outage looks like from here — including the case
// that produced the issue, where a server was marked install_failed by a pass
// that never reached the node while the game kept running. install_failed is
// adoptable for exactly that reason: a running container is proof the install
// tree is fine, and clearing the state is what returns START/RESTART to the
// operator.
//
// `installing` is deliberately absent. An install pass owns the row from the
// moment it starts until it settles, and a stale container still running under
// it must never be read as success.
func reconcileAdoptable(st store.ServerState) bool {
	switch st {
	case store.StateOffline, store.StateInstallFailed:
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
		live := reconcileLive(sv.State)
		adopt := !live && reconcileAdoptable(sv.State)
		if !live && !adopt {
			continue
		}
		node, err := s.store.GetNode(ctx, sv.NodeID)
		if err != nil {
			continue
		}
		// A stopped server is only worth a round trip when the node is believed
		// reachable: this pass runs every few seconds, and a fleet of stopped
		// servers on a dead node must not become a fleet of dial timeouts.
		if adopt && !s.believedLive(node) {
			continue
		}
		client, err := s.nodes.Client(node.DialTarget())
		if err != nil {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		status, err := client.GetServerStatus(cctx, &agentpb.GetServerStatusRequest{ServerId: sv.ID})
		cancel()
		if err != nil {
			continue
		}
		if adopt {
			s.adoptRunning(ctx, sv, status)
			continue
		}
		newState := storeStateFromAgent(status.State)
		// Last-known online-player count (for the fleet list, no stream needed).
		var np, nmax int32
		var nknown bool
		if ls := status.LastStats; ls != nil && ls.PlayersKnown {
			np, nmax, nknown = ls.Players, ls.MaxPlayers, true
		}
		// The exit code of the run that ended, persisted only for a crash: that
		// is the state where the operator has nothing else to go on, and holding
		// it past a successful start would let a stale number explain a server
		// that is plainly fine.
		var nexit int64
		var nexitKnown bool
		if newState == store.StateCrashed && status.ExitCodeKnown {
			nexit, nexitKnown = status.LastExitCode, true
		}
		if newState == sv.State && np == sv.Players && nmax == sv.MaxPlayers && nknown == sv.PlayersKnown &&
			nexit == sv.LastExitCode && nexitKnown == sv.LastExitCodeKnown {
			continue
		}
		sv.State = newState
		sv.Players, sv.MaxPlayers, sv.PlayersKnown = np, nmax, nknown
		sv.LastExitCode, sv.LastExitCodeKnown = nexit, nexitKnown
		if uerr := s.store.UpdateServer(ctx, sv); uerr != nil {
			s.logger.Warn("reconcile: update server failed", "server", sv.ID, "err", uerr)
			continue
		}
	}
}

// adoptRunning corrects a stopped server row the Agent contradicts: its managed
// container is running, so the Panel believes the Agent (#328). Anything else
// the Agent reports for a stopped row is ignored — this pass answers one
// question, and a stopped server the Agent also calls stopped is no divergence.
func (s *Server) adoptRunning(ctx context.Context, sv *store.Server, status *agentpb.ServerStatus) {
	if storeStateFromAgent(status.State) != store.StateRunning {
		return
	}
	from := sv.State
	sv.State = store.StateRunning
	// The stored reason described a server that is plainly up, and while it
	// stands the fleet shows a failure beside a running game. The exit code goes
	// for the reason it goes on a power action: it explains a run that ended,
	// and this one has not.
	sv.LastError = ""
	sv.LastExitCode, sv.LastExitCodeKnown = 0, false
	if ls := status.LastStats; ls != nil && ls.PlayersKnown {
		sv.Players, sv.MaxPlayers, sv.PlayersKnown = ls.Players, ls.MaxPlayers, true
	}
	if err := s.store.UpdateServer(ctx, sv); err != nil {
		s.logger.Warn("reconcile: adopt running failed", "server", sv.ID, "err", err)
		return
	}
	s.logger.Info("reconcile: adopted the agent's running container", "server", sv.ID, "was", from)
}
