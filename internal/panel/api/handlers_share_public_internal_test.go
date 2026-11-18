package api

import (
	"testing"

	"github.com/briggleman/kraken/internal/panel/cluster"
	"github.com/briggleman/kraken/internal/panel/share"
	"github.com/briggleman/kraken/internal/panel/store"
)

// A public card may show a DNS name or a public IP, never anything that is
// the operator's LAN or that a stranger's resolver cannot find.
func TestPublicHostOK(t *testing.T) {
	for h, want := range map[string]bool{
		"play.example.net": true, "203.0.113.24": true, "2001:db8::1": true, "8.8.8.8": true,
		"": false, "  ": false, "192.168.1.88": false, "10.0.0.5": false, "172.16.4.2": false,
		"127.0.0.1": false, "::1": false, "169.254.1.1": false, "fe80::1": false, "fd00::1": false,
		"0.0.0.0": false, "behemoth": false, "nas.local": false,
	} {
		if got := publicHostOK(h); got != want {
			t.Errorf("publicHostOK(%q) = %v, want %v", h, got, want)
		}
	}
}

func TestShareAddress(t *testing.T) {
	node := &cluster.Node{ExternalIP: "203.0.113.24", PublicHost: "192.168.1.88"}
	sv := &store.Server{Ports: map[string]int{"game": 7777}}
	if got := shareAddress(sv, node, "game"); got != "203.0.113.24:7777" {
		t.Errorf("WAN IP: %q", got)
	}
	if got := shareAddress(sv, node, "query"); got != "" {
		t.Errorf("a port the server was not allocated gives no address, got %q", got)
	}
	sv.DNS = &store.ServerDNS{Name: "play.example.net"}
	if got := shareAddress(sv, node, "game"); got != "play.example.net:7777" {
		t.Errorf("DNS name first: %q", got)
	}
	v6 := &cluster.Node{ExternalIP: "2001:db8::1"}
	if got := shareAddress(&store.Server{Ports: map[string]int{"game": 7777}}, v6, "game"); got != "[2001:db8::1]:7777" {
		t.Errorf("IPv6 is bracketed: %q", got)
	}
	lan := &cluster.Node{ExternalIP: "10.0.0.5", PublicHost: "192.168.1.88"}
	if got := shareAddress(&store.Server{Ports: map[string]int{"game": 7777}}, lan, "game"); got != "" {
		t.Errorf("LAN-only node must give no address, got %q", got)
	}
}

func TestShareState(t *testing.T) {
	online := &cluster.Node{Status: cluster.NodeOnline}
	for st, want := range map[store.ServerState]string{
		store.StateRunning: share.StateRunning, store.StateStarting: share.StateStarting,
		store.StateInstalling: share.StateStarting, store.StateRestoring: share.StateStarting,
		store.StateOffline: share.StateStopped, store.StateCrashed: share.StateStopped,
		store.StateStopping: share.StateStopped, store.StateRetired: share.StateStopped,
	} {
		if got := shareState(st, online); got != want {
			t.Errorf("%s on an online node = %s, want %s", st, got, want)
		}
	}
	for _, n := range []*cluster.Node{nil, {Status: cluster.NodeOffline}, {Status: cluster.NodePartial}} {
		if got := shareState(store.StateRunning, n); got != share.StateUnreachable {
			t.Errorf("running on %+v = %s, want unreachable", n, got)
		}
	}
}
