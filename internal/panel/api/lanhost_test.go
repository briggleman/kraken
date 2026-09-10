package api

import (
	"testing"

	"github.com/briggleman/kraken/internal/panel/cluster"
	"github.com/briggleman/kraken/internal/shared/agentpb"
)

func TestAdoptLANHostSeedsWithoutTouchingPublicHost(t *testing.T) {
	// First-ever observation: LANHost seeds, a PublicHost frozen at an older
	// first-contact guess is NOT moved — there is no evidence it was tracking.
	n := &cluster.Node{PublicHost: "192.168.0.87"}
	if !adoptLANHost(n, "192.168.0.88") {
		t.Fatal("want changed")
	}
	if n.LANHost != "192.168.0.88" {
		t.Fatalf("LANHost = %q", n.LANHost)
	}
	if n.PublicHost != "192.168.0.87" {
		t.Fatalf("PublicHost moved on seed: %q", n.PublicHost)
	}
}

func TestAdoptLANHostMovesTrackingPublicHost(t *testing.T) {
	// PublicHost equal to the previous observation was tracking the agent —
	// it moves with the LAN host (the #224 staleness fix).
	n := &cluster.Node{LANHost: "192.168.0.87", PublicHost: "192.168.0.87"}
	if !adoptLANHost(n, "192.168.0.88") {
		t.Fatal("want changed")
	}
	if n.LANHost != "192.168.0.88" || n.PublicHost != "192.168.0.88" {
		t.Fatalf("LANHost = %q, PublicHost = %q", n.LANHost, n.PublicHost)
	}
}

func TestAdoptLANHostNeverTouchesOperatorPublicHost(t *testing.T) {
	n := &cluster.Node{LANHost: "192.168.0.87", PublicHost: "play.example.com"}
	if !adoptLANHost(n, "192.168.0.88") {
		t.Fatal("want changed")
	}
	if n.PublicHost != "play.example.com" {
		t.Fatalf("operator PublicHost moved: %q", n.PublicHost)
	}
}

func TestAdoptLANHostNoOps(t *testing.T) {
	n := &cluster.Node{LANHost: "192.168.0.88", PublicHost: "192.168.0.88"}
	if adoptLANHost(n, "") || adoptLANHost(n, "192.168.0.88") {
		t.Fatal("empty/same observation must not report change")
	}
	if n.LANHost != "192.168.0.88" || n.PublicHost != "192.168.0.88" {
		t.Fatalf("state mutated: %+v", n)
	}
}

func TestNodeLANHostPrecedence(t *testing.T) {
	if got := nodeLANHost(nil); got != "" {
		t.Fatalf("nil node: %q", got)
	}
	// Tracked LANHost wins over the player-facing PublicHost.
	n := &cluster.Node{LANHost: "192.168.0.88", PublicHost: "play.example.com"}
	if got := nodeLANHost(n); got != "192.168.0.88" {
		t.Fatalf("got %q, want the tracked LANHost", got)
	}
	// Pre-#224 records fall back to nodeHost (PublicHost, then Address host).
	n = &cluster.Node{PublicHost: "play.example.com"}
	if got := nodeLANHost(n); got != "play.example.com" {
		t.Fatalf("fallback got %q", got)
	}
	n = &cluster.Node{Address: "10.0.0.5:9090"}
	if got := nodeLANHost(n); got != "10.0.0.5" {
		t.Fatalf("address fallback got %q", got)
	}
}

func TestHasHostAddress(t *testing.T) {
	cands := []*agentpb.HostAddress{
		{Interface: "eth1", Ip: "192.168.0.88"},
		{Interface: "docker0", Ip: "172.17.0.1"},
	}
	if !hasHostAddress(cands, "192.168.0.88") {
		t.Fatal("owned address not recognized")
	}
	// The panel's own NAT gateway (a containerized panel sees this as every
	// tunnel session's source) is NOT one of the agent's addresses.
	if hasHostAddress(cands, "192.168.65.1") {
		t.Fatal("foreign address accepted")
	}
	if hasHostAddress(nil, "192.168.0.88") {
		t.Fatal("empty candidate list must not match")
	}
}

func TestHostAddressStrings(t *testing.T) {
	if got := hostAddressStrings(nil); got != nil {
		t.Fatalf("empty input must stay nil, got %v", got)
	}
	got := hostAddressStrings([]*agentpb.HostAddress{
		{Interface: "eth0", Ip: "192.168.0.88"},
		{Interface: "", Ip: "172.17.0.1"},
	})
	want := []string{"eth0 192.168.0.88", "172.17.0.1"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %v, want %v", got, want)
	}
}
