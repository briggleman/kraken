// Package scheduler chooses which node runs a new server. It honors the
// platform-priority order declared on a Spec (by convention native Linux is
// listed first, so a Linux dedicated server is preferred whenever one exists,
// falling back to Wine-on-Linux, then native Windows). Among the nodes that can
// host the highest-priority feasible platform kind, it picks the best fit and
// reserves the node's memory + ports.
package scheduler

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/briggleman/kraken/internal/panel/cluster"
	"github.com/briggleman/kraken/internal/shared/spec"
)

// ErrNoPlacement is returned when no node can host any of the spec's platforms.
var ErrNoPlacement = errors.New("scheduler: no node can host this spec")

// Placement is the result of scheduling: the chosen node, the platform kind it
// will run, the reserved memory, and the port assignments (spec port name →
// allocated port number).
type Placement struct {
	NodeID   string            `json:"node_id"`
	Kind     spec.PlatformKind `json:"kind"`
	MemoryMB int               `json:"memory_mb"`
	Ports    map[string]int    `json:"ports"`
}

// Place selects a node for s from the given nodes and reserves its resources.
//
// It walks the spec's platforms in declared priority order; for each kind it
// considers the schedulable nodes that support that kind, ordered best-fit
// (most available memory first, node ID as a stable tiebreaker), and reserves
// the first that has enough memory and free ports. The reservation mutates the
// chosen node; on total failure no node is modified and the error names each
// node with the reason it was rejected.
func Place(s *spec.Spec, nodes []*cluster.Node) (*Placement, error) {
	memReq := s.Resources.MinMemoryMB
	reqs := make([]cluster.PortRequest, len(s.Ports))
	for i, p := range s.Ports {
		reqs[i] = cluster.PortRequest{Name: p.Name, Preferred: p.Default}
	}

	reserveErrs := make(map[string]error, len(nodes)) // node ID → last Reserve failure
	for _, plat := range s.Platforms {
		for _, n := range candidates(nodes, plat.Kind) {
			ports, err := n.Reserve(memReq, reqs)
			if err != nil {
				reserveErrs[n.ID] = err
				continue // try the next node for this kind
			}
			return &Placement{NodeID: n.ID, Kind: plat.Kind, MemoryMB: memReq, Ports: ports}, nil
		}
	}
	return nil, fmt.Errorf("%w: %q (memory %dMB, %d ports): %s",
		ErrNoPlacement, s.Slug, memReq, len(s.Ports), rejectionDetail(s, nodes, reserveErrs))
}

// candidates returns the schedulable nodes that support kind, ordered best-fit:
// most available memory first, then by node ID for deterministic tie-breaking.
func candidates(nodes []*cluster.Node, kind spec.PlatformKind) []*cluster.Node {
	out := make([]*cluster.Node, 0, len(nodes))
	for _, n := range nodes {
		if n.Schedulable() && n.Supports(kind) {
			out = append(out, n)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		ai, aj := out[i].AvailableMemoryMB(), out[j].AvailableMemoryMB()
		if ai != aj {
			return ai > aj
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// rejectionDetail names every node with the reason it was rejected, so the
// operator can tell an offline node from a full one from a platform mismatch
// without reading scheduler code.
func rejectionDetail(s *spec.Spec, nodes []*cluster.Node, reserveErrs map[string]error) string {
	if len(nodes) == 0 {
		return "no nodes registered"
	}
	parts := make([]string, 0, len(nodes))
	for _, n := range nodes {
		parts = append(parts, fmt.Sprintf("%s: %s", nodeLabel(n), rejectionReason(s, n, reserveErrs[n.ID])))
	}
	return strings.Join(parts, "; ")
}

func nodeLabel(n *cluster.Node) string {
	if n.Name != "" {
		return n.Name
	}
	return n.ID
}

// rejectionReason explains why one node did not receive the placement.
func rejectionReason(s *spec.Spec, n *cluster.Node, reserveErr error) string {
	if !n.Schedulable() {
		return string(n.Status)
	}
	supported := false
	for _, plat := range s.Platforms {
		if n.Supports(plat.Kind) {
			supported = true
			break
		}
	}
	if !supported {
		kinds := make([]string, len(s.Platforms))
		for i, plat := range s.Platforms {
			kinds[i] = string(plat.Kind)
		}
		return fmt.Sprintf("%s node cannot run %s", n.OS, strings.Join(kinds, "/"))
	}
	switch {
	case errors.Is(reserveErr, cluster.ErrInsufficientMemory):
		return fmt.Sprintf("insufficient memory (%dMB available)", n.AvailableMemoryMB())
	case errors.Is(reserveErr, cluster.ErrNoFreePort):
		if n.Ports == nil || len(n.Ports.Ranges) == 0 {
			return "no port range configured (re-register the node with a port range)"
		}
		return fmt.Sprintf("no free port (%d left, %d needed)", n.Ports.FreeCount(), len(s.Ports))
	case reserveErr != nil:
		return reserveErr.Error()
	default:
		// Supported and schedulable but never attempted — cannot happen today,
		// but don't lie if the loop structure changes.
		return "not selected"
	}
}
