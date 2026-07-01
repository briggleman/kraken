package cluster

import (
	"errors"

	"github.com/briggleman/kraken/internal/shared/spec"
)

// NodeOS is the operating system of a node, which determines what platform kinds
// it can run. Linux nodes run native Linux servers (and, if Wine is enabled,
// Windows servers via Wine); Windows nodes run native Windows containers.
type NodeOS string

const (
	OSLinux   NodeOS = "linux"
	OSWindows NodeOS = "windows"
)

// NodeStatus is a node's scheduling availability.
type NodeStatus string

const (
	// NodeOnline nodes accept new servers.
	NodeOnline NodeStatus = "online"
	// NodeOffline nodes are unreachable and not schedulable.
	NodeOffline NodeStatus = "offline"
	// NodeCordoned nodes are reachable but excluded from new placements.
	NodeCordoned NodeStatus = "cordoned"
)

// Reservation errors.
var (
	ErrInsufficientMemory = errors.New("cluster: insufficient memory")
	ErrNoFreePort         = errors.New("cluster: no free port available")
)

// Node is a host that runs game-server containers, with its capacity and current
// allocations. The scheduler reads SupportedKinds/AvailableMemoryMB and reserves
// resources via Reserve.
type Node struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	OS          NodeOS     `json:"os"`
	WineEnabled bool       `json:"wine_enabled"`
	Status      NodeStatus `json:"status"`
	// Address is the Agent's gRPC endpoint (host:port) the Panel dials (control plane).
	Address string `json:"address"`
	// PublicHost is the address players use to connect to game servers on this
	// node (its public IP or DNS name). Set by the operator at registration, or
	// auto-filled from the Agent's detected host IP on first contact.
	PublicHost string `json:"public_host"`
	// ExternalIP is the node's detected outward-facing/WAN IP (from the Agent's
	// egress echo, or a UniFi gateway override). Used for DNS records + the
	// player-facing connect address; empty when undetermined.
	ExternalIP string `json:"external_ip,omitempty"`

	TotalMemoryMB     int `json:"total_memory_mb"`
	AllocatedMemoryMB int `json:"allocated_memory_mb"`

	// SFTPPort is the port the node's Agent SFTP server listens on (from NodeInfo);
	// 0 when SFTP is disabled. Used to show connection details on the Files tab.
	SFTPPort int `json:"sftp_port,omitempty"`

	Ports *PortPool `json:"ports"`
}

// SupportedKinds returns the platform kinds this node can run, derived from its
// OS and Wine setting.
func (n *Node) SupportedKinds() []spec.PlatformKind {
	switch n.OS {
	case OSLinux:
		kinds := []spec.PlatformKind{spec.LinuxNative}
		if n.WineEnabled {
			kinds = append(kinds, spec.LinuxWine)
		}
		return kinds
	case OSWindows:
		return []spec.PlatformKind{spec.WindowsNative}
	default:
		return nil
	}
}

// Supports reports whether the node can run the given platform kind.
func (n *Node) Supports(kind spec.PlatformKind) bool {
	for _, k := range n.SupportedKinds() {
		if k == kind {
			return true
		}
	}
	return false
}

// AvailableMemoryMB is the unreserved memory on the node.
func (n *Node) AvailableMemoryMB() int { return n.TotalMemoryMB - n.AllocatedMemoryMB }

// Schedulable reports whether the node currently accepts new placements.
func (n *Node) Schedulable() bool { return n.Status == NodeOnline }

// PortRequest names a port a server needs and the preferred number to try first
// (typically the spec's declared default).
type PortRequest struct {
	Name      string
	Preferred int
}

// Reserve atomically reserves memMB of memory and one port per request. On
// success it returns a map of request name → allocated port and mutates the
// node's accounting. On failure it makes no changes (any partial port
// allocations are rolled back) and returns an error.
func (n *Node) Reserve(memMB int, reqs []PortRequest) (map[string]int, error) {
	if n.AvailableMemoryMB() < memMB {
		return nil, ErrInsufficientMemory
	}
	if n.Ports == nil {
		n.Ports = NewPortPool()
	}
	allocated := make(map[string]int, len(reqs))
	taken := make([]int, 0, len(reqs))
	for _, req := range reqs {
		port, ok := n.Ports.Allocate(req.Preferred)
		if !ok {
			for _, p := range taken {
				n.Ports.Release(p)
			}
			return nil, ErrNoFreePort
		}
		allocated[req.Name] = port
		taken = append(taken, port)
	}
	n.AllocatedMemoryMB += memMB
	return allocated, nil
}

// Release returns reserved memory and ports to the node.
func (n *Node) Release(memMB int, ports []int) {
	n.AllocatedMemoryMB -= memMB
	if n.AllocatedMemoryMB < 0 {
		n.AllocatedMemoryMB = 0
	}
	if n.Ports != nil {
		for _, p := range ports {
			n.Ports.Release(p)
		}
	}
}
