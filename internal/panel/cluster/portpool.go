// Package cluster models the hosts (nodes) that run game servers and their
// finite resources — memory and network ports — that the scheduler allocates.
package cluster

import (
	"encoding/json"
	"sort"
)

// PortRange is an inclusive range of port numbers a node may allocate from.
type PortRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// PortPool tracks which ports within a node's configured ranges are free vs.
// allocated. It is not safe for concurrent use; callers serialize access (the
// scheduler holds a lock around placement).
type PortPool struct {
	Ranges    []PortRange `json:"ranges"`
	allocated map[int]bool
}

// NewPortPool builds a pool over the given ranges.
func NewPortPool(ranges ...PortRange) *PortPool {
	return &PortPool{Ranges: ranges, allocated: make(map[int]bool)}
}

func (p *PortPool) ensure() {
	if p.allocated == nil {
		p.allocated = make(map[int]bool)
	}
}

// contains reports whether port falls within any configured range.
func (p *PortPool) contains(port int) bool {
	for _, r := range p.Ranges {
		if port >= r.Start && port <= r.End {
			return true
		}
	}
	return false
}

// IsFree reports whether port is within the pool and not yet allocated.
func (p *PortPool) IsFree(port int) bool {
	p.ensure()
	return p.contains(port) && !p.allocated[port]
}

// FreeCount returns how many ports remain available across all ranges.
func (p *PortPool) FreeCount() int {
	p.ensure()
	total := 0
	for _, r := range p.Ranges {
		if r.End >= r.Start {
			total += r.End - r.Start + 1
		}
	}
	return total - len(p.allocated)
}

// Allocate reserves a port and returns it. It prefers `preferred` when that port
// is in-range and free; otherwise it returns the lowest available port. The
// second result is false when the pool is exhausted.
func (p *PortPool) Allocate(preferred int) (int, bool) {
	p.ensure()
	if preferred != 0 && p.IsFree(preferred) {
		p.allocated[preferred] = true
		return preferred, true
	}
	// Lowest free port for deterministic behavior.
	ranges := append([]PortRange(nil), p.Ranges...)
	sort.Slice(ranges, func(i, j int) bool { return ranges[i].Start < ranges[j].Start })
	for _, r := range ranges {
		for port := r.Start; port <= r.End; port++ {
			if !p.allocated[port] {
				p.allocated[port] = true
				return port, true
			}
		}
	}
	return 0, false
}

// Release returns a previously allocated port to the pool.
func (p *PortPool) Release(port int) {
	p.ensure()
	delete(p.allocated, port)
}

// SetRanges replaces the pool's configured ranges while preserving current
// allocations. Ports already allocated outside the new ranges stay reserved
// (their servers keep running); they simply return to no pool when released.
func (p *PortPool) SetRanges(ranges ...PortRange) {
	p.ensure()
	p.Ranges = ranges
}

// portPoolJSON is the serialized form of a PortPool, including allocations so
// reservations survive a round-trip through storage.
type portPoolJSON struct {
	Ranges    []PortRange `json:"ranges"`
	Allocated []int       `json:"allocated,omitempty"`
}

// MarshalJSON serializes the pool's ranges and current allocations.
func (p *PortPool) MarshalJSON() ([]byte, error) {
	p.ensure()
	alloc := make([]int, 0, len(p.allocated))
	for port := range p.allocated {
		alloc = append(alloc, port)
	}
	sort.Ints(alloc)
	return json.Marshal(portPoolJSON{Ranges: p.Ranges, Allocated: alloc})
}

// UnmarshalJSON restores the pool's ranges and allocations.
func (p *PortPool) UnmarshalJSON(b []byte) error {
	var j portPoolJSON
	if err := json.Unmarshal(b, &j); err != nil {
		return err
	}
	p.Ranges = j.Ranges
	p.allocated = make(map[int]bool, len(j.Allocated))
	for _, port := range j.Allocated {
		p.allocated[port] = true
	}
	return nil
}

// Clone returns a deep copy of the pool, preserving current allocations.
func (p *PortPool) Clone() *PortPool {
	p.ensure()
	np := &PortPool{
		Ranges:    append([]PortRange(nil), p.Ranges...),
		allocated: make(map[int]bool, len(p.allocated)),
	}
	for port := range p.allocated {
		np.allocated[port] = true
	}
	return np
}
