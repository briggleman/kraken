package api

import (
	"context"
	"crypto/sha256"
	"fmt"

	"github.com/briggleman/kraken/internal/panel/cluster"
	"github.com/briggleman/kraken/internal/panel/store"
	"github.com/briggleman/kraken/internal/shared/agentpb"
	"github.com/briggleman/kraken/internal/shared/spec"
)

// Transparent networking (#426). On a Windows node set to "transparent", each
// game container joins a Docker transparent network and takes its own LAN
// address by DHCP instead of sharing the node's address behind Windows NAT.
// Windows NAT cannot publish the random UDP port a Steam-networking game opens
// for its session traffic, so behind it a game like Enshrouded falls back to
// Steam's relay and a join sits on the loading screen; with its own address
// every port the game opens is reachable.
//
// The pieces the Panel owns:
//   - stamping each server's spec with the mode and a stable MAC (agentSpecFor);
//   - reading where a server is reached: its container's address when it has
//     one, else its node's (serverLANHost);
//   - pinning that address in UniFi so it survives restarts, and pointing port
//     forwards at it (network_unifi.go).

// serverMAC is a server's MAC on a transparent network: stable for the life of
// the server, so the router's DHCP hands the same address to every container
// the server is recreated as. 02 marks it locally administered unicast; 4b is
// "K"; the last four octets come from the server id.
func serverMAC(serverID string) string {
	h := sha256.Sum256([]byte(serverID))
	return fmt.Sprintf("02:4b:%02x:%02x:%02x:%02x", h[0], h[1], h[2], h[3])
}

// agentSpecFor is toAgentSpec plus the node's container network. The mode is
// stamped on the spec rather than read by the Agent from its node config at
// create time, so a node switching modes moves each server on that server's
// next start, never under a running one.
func (s *Server) agentSpecFor(ctx context.Context, sv *store.Server, sp *spec.Spec) (*agentpb.ServerSpec, error) {
	as := toAgentSpec(sv, sp)
	cfg, err := s.nodeConfigOrEmpty(ctx, sv.NodeID)
	if err != nil {
		return nil, fmt.Errorf("load node config: %w", err)
	}
	stampNetwork(as, cfg, sv.ID)
	return as, nil
}

func stampNetwork(as *agentpb.ServerSpec, cfg *store.NodeConfig, serverID string) {
	if !cfg.Transparent() {
		return
	}
	as.NetworkMode = store.NetworkTransparent
	as.MacAddress = serverMAC(serverID)
}

// serversPendingNetwork names the servers on a node whose container is not yet
// on the node's configured network. A mode change moves each server on its own
// next start, so after switching, this is the list still waiting for one. A
// transparent container is told apart by the address the Agent reports for it,
// which the Panel keeps while the server is stopped: one that has never had an
// address is still behind NAT, and one with an address on a NAT node is still
// transparent.
func (s *Server) serversPendingNetwork(ctx context.Context, nodeID string, cfg *store.NodeConfig) []string {
	servers, err := s.store.ListServers(ctx)
	if err != nil {
		return nil
	}
	var out []string
	for _, sv := range servers {
		if sv.NodeID != nodeID {
			continue
		}
		onTransparent := sv.ContainerIP != ""
		switch {
		case cfg.Transparent() && !onTransparent:
		case !cfg.Transparent() && onTransparent:
		default:
			continue
		}
		out = append(out, sv.Name)
	}
	return out
}

// serverLANHost is where a server is reached on the LAN: its container's own
// address on a transparent network, else its node's LAN address with the
// server's ports published there.
func serverLANHost(sv *store.Server, node *cluster.Node) string {
	if sv != nil && sv.ContainerIP != "" {
		return sv.ContainerIP
	}
	return nodeLANHost(node)
}
