package cluster

import (
	"testing"

	"github.com/briggleman/kraken/internal/shared/spec"
)

func TestPortPool_AllocatePreferredThenLowest(t *testing.T) {
	p := NewPortPool(PortRange{Start: 100, End: 102})
	if p.FreeCount() != 3 {
		t.Fatalf("FreeCount = %d, want 3", p.FreeCount())
	}
	if got, ok := p.Allocate(101); !ok || got != 101 {
		t.Fatalf("Allocate(101) = %d,%v; want 101,true", got, ok)
	}
	// preferred taken → lowest free (100)
	if got, ok := p.Allocate(101); !ok || got != 100 {
		t.Fatalf("Allocate(101 again) = %d,%v; want 100,true", got, ok)
	}
	if got, ok := p.Allocate(0); !ok || got != 102 {
		t.Fatalf("Allocate(0) = %d,%v; want 102,true", got, ok)
	}
	if _, ok := p.Allocate(0); ok {
		t.Fatal("pool should be exhausted")
	}
}

func TestPortPool_OutOfRangePreferredIgnored(t *testing.T) {
	p := NewPortPool(PortRange{Start: 100, End: 101})
	if got, ok := p.Allocate(9999); !ok || got != 100 {
		t.Fatalf("out-of-range preferred should fall to lowest free; got %d,%v", got, ok)
	}
}

func TestPortPool_Release(t *testing.T) {
	p := NewPortPool(PortRange{Start: 100, End: 100})
	got, _ := p.Allocate(100)
	if p.FreeCount() != 0 {
		t.Fatal("pool should be full")
	}
	p.Release(got)
	if !p.IsFree(100) || p.FreeCount() != 1 {
		t.Fatal("release should free the port")
	}
}

func TestNode_SupportedKinds(t *testing.T) {
	lin := &Node{OS: OSLinux, WineEnabled: true}
	if !lin.Supports(spec.LinuxNative) || !lin.Supports(spec.LinuxWine) {
		t.Fatal("wine-enabled linux should support linux-native and linux-wine")
	}
	if lin.Supports(spec.WindowsNative) {
		t.Fatal("linux must not support windows-native")
	}
	linNoWine := &Node{OS: OSLinux, WineEnabled: false}
	if linNoWine.Supports(spec.LinuxWine) {
		t.Fatal("wine-disabled linux must not support linux-wine")
	}
	win := &Node{OS: OSWindows}
	if !win.Supports(spec.WindowsNative) || win.Supports(spec.LinuxNative) {
		t.Fatal("windows node supports only windows-native")
	}
}

func TestNode_ReserveRollbackOnPortExhaustion(t *testing.T) {
	n := &Node{
		ID: "n1", OS: OSLinux, Status: NodeOnline, TotalMemoryMB: 4096,
		Ports: NewPortPool(PortRange{Start: 100, End: 100}), // only one port
	}
	// Two port requests but only one port available → must fail and roll back.
	_, err := n.Reserve(1024, []PortRequest{{Name: "a", Preferred: 100}, {Name: "b", Preferred: 0}})
	if err != ErrNoFreePort {
		t.Fatalf("expected ErrNoFreePort, got %v", err)
	}
	if n.AllocatedMemoryMB != 0 {
		t.Fatalf("memory must not be reserved on failure, got %d", n.AllocatedMemoryMB)
	}
	if n.Ports.FreeCount() != 1 {
		t.Fatalf("port allocations must be rolled back, free=%d want 1", n.Ports.FreeCount())
	}
}

func TestNode_ReserveInsufficientMemory(t *testing.T) {
	n := &Node{ID: "n1", OS: OSLinux, Status: NodeOnline, TotalMemoryMB: 512,
		Ports: NewPortPool(PortRange{Start: 100, End: 200})}
	if _, err := n.Reserve(1024, nil); err != ErrInsufficientMemory {
		t.Fatalf("expected ErrInsufficientMemory, got %v", err)
	}
}
