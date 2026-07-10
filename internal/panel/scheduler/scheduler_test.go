package scheduler

import (
	"errors"
	"strings"
	"testing"

	"github.com/briggleman/kraken/internal/panel/cluster"
	"github.com/briggleman/kraken/internal/shared/spec"
)

// crossPlatformSpec supports Linux natively, Wine, and native Windows — in that
// priority order — needing 2GB and one UDP port.
func crossPlatformSpec() *spec.Spec {
	return &spec.Spec{
		Slug: "cs2",
		Platforms: []spec.Platform{
			{Kind: spec.LinuxNative, Image: "img/linux"},
			{Kind: spec.LinuxWine, Image: "img/wine"},
			{Kind: spec.WindowsNative, Image: "img/win"},
		},
		Ports:     []spec.Port{{Name: "game", Protocol: spec.UDP, Default: 27015, Required: true}},
		Resources: spec.Resources{MinMemoryMB: 2048},
	}
}

// windowsOnlySpec has no Linux-native server: prefer Wine-on-Linux, else Windows.
func windowsOnlySpec() *spec.Spec {
	return &spec.Spec{
		Slug: "winonly",
		Platforms: []spec.Platform{
			{Kind: spec.LinuxWine, Image: "img/wine"},
			{Kind: spec.WindowsNative, Image: "img/win"},
		},
		Ports:     []spec.Port{{Name: "game", Protocol: spec.UDP, Default: 7777}},
		Resources: spec.Resources{MinMemoryMB: 1024},
	}
}

func linuxNode(id string, mem int, wine bool) *cluster.Node {
	return &cluster.Node{
		ID: id, Name: id, OS: cluster.OSLinux, WineEnabled: wine,
		Status: cluster.NodeOnline, TotalMemoryMB: mem,
		Ports: cluster.NewPortPool(cluster.PortRange{Start: 27000, End: 27100}),
	}
}

func windowsNode(id string, mem int) *cluster.Node {
	return &cluster.Node{
		ID: id, Name: id, OS: cluster.OSWindows,
		Status: cluster.NodeOnline, TotalMemoryMB: mem,
		Ports: cluster.NewPortPool(cluster.PortRange{Start: 27000, End: 27100}),
	}
}

func TestPlace_PrefersNativeLinux(t *testing.T) {
	lin := linuxNode("lin-1", 8192, true)
	win := windowsNode("win-1", 32768) // far more memory, but lower priority kind
	p, err := Place(crossPlatformSpec(), []*cluster.Node{win, lin})
	if err != nil {
		t.Fatalf("Place: %v", err)
	}
	if p.NodeID != "lin-1" || p.Kind != spec.LinuxNative {
		t.Fatalf("expected native Linux on lin-1, got node=%s kind=%s", p.NodeID, p.Kind)
	}
	if p.Ports["game"] != 27015 {
		t.Fatalf("expected preferred port 27015, got %d", p.Ports["game"])
	}
}

func TestPlace_FallsBackToWindowsWhenNoLinux(t *testing.T) {
	// Cross-platform spec but only a Windows node exists.
	win := windowsNode("win-1", 8192)
	p, err := Place(crossPlatformSpec(), []*cluster.Node{win})
	if err != nil {
		t.Fatalf("Place: %v", err)
	}
	if p.NodeID != "win-1" || p.Kind != spec.WindowsNative {
		t.Fatalf("expected native Windows fallback, got node=%s kind=%s", p.NodeID, p.Kind)
	}
}

func TestPlace_WindowsOnly_PrefersWineOnLinux(t *testing.T) {
	lin := linuxNode("lin-1", 8192, true) // wine enabled
	win := windowsNode("win-1", 8192)
	p, err := Place(windowsOnlySpec(), []*cluster.Node{lin, win})
	if err != nil {
		t.Fatalf("Place: %v", err)
	}
	if p.NodeID != "lin-1" || p.Kind != spec.LinuxWine {
		t.Fatalf("expected Wine-on-Linux, got node=%s kind=%s", p.NodeID, p.Kind)
	}
}

func TestPlace_WindowsOnly_NoWine_UsesWindows(t *testing.T) {
	lin := linuxNode("lin-1", 8192, false) // wine disabled → cannot run linux-wine
	win := windowsNode("win-1", 8192)
	p, err := Place(windowsOnlySpec(), []*cluster.Node{lin, win})
	if err != nil {
		t.Fatalf("Place: %v", err)
	}
	if p.NodeID != "win-1" || p.Kind != spec.WindowsNative {
		t.Fatalf("expected native Windows, got node=%s kind=%s", p.NodeID, p.Kind)
	}
}

func TestPlace_SkipsInsufficientMemory(t *testing.T) {
	small := linuxNode("lin-small", 1024, true) // < 2048 required
	big := linuxNode("lin-big", 4096, true)
	p, err := Place(crossPlatformSpec(), []*cluster.Node{small, big})
	if err != nil {
		t.Fatalf("Place: %v", err)
	}
	if p.NodeID != "lin-big" {
		t.Fatalf("expected lin-big (enough memory), got %s", p.NodeID)
	}
}

func TestPlace_BestFitMostAvailableMemory(t *testing.T) {
	a := linuxNode("lin-a", 4096, true)
	b := linuxNode("lin-b", 16384, true) // most available → chosen
	c := linuxNode("lin-c", 8192, true)
	p, err := Place(crossPlatformSpec(), []*cluster.Node{a, b, c})
	if err != nil {
		t.Fatalf("Place: %v", err)
	}
	if p.NodeID != "lin-b" {
		t.Fatalf("expected best-fit lin-b, got %s", p.NodeID)
	}
}

func TestPlace_SkipsOfflineAndCordoned(t *testing.T) {
	off := linuxNode("lin-off", 16384, true)
	off.Status = cluster.NodeOffline
	cord := linuxNode("lin-cord", 16384, true)
	cord.Status = cluster.NodeCordoned
	ok := linuxNode("lin-ok", 4096, true)
	p, err := Place(crossPlatformSpec(), []*cluster.Node{off, cord, ok})
	if err != nil {
		t.Fatalf("Place: %v", err)
	}
	if p.NodeID != "lin-ok" {
		t.Fatalf("expected only schedulable node lin-ok, got %s", p.NodeID)
	}
}

func TestPlace_NoCapacity(t *testing.T) {
	tiny := linuxNode("lin-tiny", 256, true)
	_, err := Place(crossPlatformSpec(), []*cluster.Node{tiny})
	if !errors.Is(err, ErrNoPlacement) {
		t.Fatalf("expected ErrNoPlacement, got %v", err)
	}
	if !strings.Contains(err.Error(), "lin-tiny: insufficient memory (256MB available)") {
		t.Fatalf("error should name the node and its memory shortfall, got: %v", err)
	}
}

// The placement error must say WHY each node was rejected — an offline node, a
// platform mismatch, and an unconfigured port pool all look identical from the
// bare "no node can host this spec" message.
func TestPlace_ErrorNamesEachRejectedNode(t *testing.T) {
	off := linuxNode("lin-off", 16384, true)
	off.Status = cluster.NodeOffline
	win := windowsNode("win-1", 16384)
	noPorts := linuxNode("lin-noports", 16384, true)
	noPorts.Ports = cluster.NewPortPool()

	// linux-only spec: the Windows node is a platform mismatch.
	linuxOnly := &spec.Spec{
		Slug:      "linonly",
		Platforms: []spec.Platform{{Kind: spec.LinuxNative, Image: "img/linux"}},
		Ports:     []spec.Port{{Name: "game", Protocol: spec.UDP, Default: 7777}},
		Resources: spec.Resources{MinMemoryMB: 2048},
	}
	_, err := Place(linuxOnly, []*cluster.Node{off, win, noPorts})
	if !errors.Is(err, ErrNoPlacement) {
		t.Fatalf("expected ErrNoPlacement, got %v", err)
	}
	for _, want := range []string{
		"lin-off: offline",
		"win-1: windows node cannot run linux-native",
		"lin-noports: no port range configured",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q, got: %v", want, err)
		}
	}
}

func TestPlace_ErrorWithNoNodes(t *testing.T) {
	_, err := Place(crossPlatformSpec(), nil)
	if !errors.Is(err, ErrNoPlacement) {
		t.Fatalf("expected ErrNoPlacement, got %v", err)
	}
	if !strings.Contains(err.Error(), "no nodes registered") {
		t.Fatalf("expected 'no nodes registered' detail, got: %v", err)
	}
}

func TestPlace_ReservesResources(t *testing.T) {
	n := linuxNode("lin-1", 8192, true)
	before := n.AvailableMemoryMB()
	if _, err := Place(crossPlatformSpec(), []*cluster.Node{n}); err != nil {
		t.Fatalf("first Place: %v", err)
	}
	if got := n.AvailableMemoryMB(); got != before-2048 {
		t.Fatalf("memory not reserved: available %d, want %d", got, before-2048)
	}
	if n.Ports.IsFree(27015) {
		t.Fatal("port 27015 should be reserved after placement")
	}
	// A second placement must get a different port (preferred 27015 now taken).
	p2, err := Place(crossPlatformSpec(), []*cluster.Node{n})
	if err != nil {
		t.Fatalf("second Place: %v", err)
	}
	if p2.Ports["game"] == 27015 {
		t.Fatal("second placement reused an allocated port")
	}
}

func TestPlace_NoPortsAvailable_RollsBackMemory(t *testing.T) {
	n := linuxNode("lin-1", 8192, true)
	n.Ports = cluster.NewPortPool() // no ports at all
	memBefore := n.AllocatedMemoryMB
	_, err := Place(crossPlatformSpec(), []*cluster.Node{n})
	if !errors.Is(err, ErrNoPlacement) {
		t.Fatalf("expected ErrNoPlacement with no ports, got %v", err)
	}
	if n.AllocatedMemoryMB != memBefore {
		t.Fatalf("memory must be rolled back when port allocation fails: got %d, want %d",
			n.AllocatedMemoryMB, memBefore)
	}
}
