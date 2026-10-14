package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/briggleman/kraken/internal/panel/cluster"
	"github.com/briggleman/kraken/internal/panel/store"
	"github.com/briggleman/kraken/internal/shared/agentpb"
	"github.com/briggleman/kraken/internal/shared/spec"
)

func TestServerMAC(t *testing.T) {
	a := serverMAC("4866d26c-7b21-47e8-9fd9-819db5ba71c7")
	if a != serverMAC("4866d26c-7b21-47e8-9fd9-819db5ba71c7") {
		t.Fatal("a server's MAC must not change between calls")
	}
	if !strings.HasPrefix(a, "02:4b:") || len(a) != len("02:4b:00:00:00:00") {
		t.Fatalf("got %q; want a locally administered unicast 02:4b:xx:xx:xx:xx", a)
	}
	if a == serverMAC("633dfe75-f1af-47ed-a3db-6cfbb32e5d4a") {
		t.Fatal("two servers got the same MAC")
	}
}

func TestStampNetwork(t *testing.T) {
	as := &agentpb.ServerSpec{}
	stampNetwork(as, &store.NodeConfig{}, "sv-1")
	if as.NetworkMode != "" || as.MacAddress != "" {
		t.Fatalf("a NAT node stamped %+v", as)
	}
	stampNetwork(as, &store.NodeConfig{ContainerNetwork: "transparent", TransparentAdapter: "Ethernet 3"}, "sv-1")
	if as.NetworkMode != "transparent" || as.MacAddress != serverMAC("sv-1") {
		t.Fatalf("got %q %q", as.NetworkMode, as.MacAddress)
	}
}

func TestValidateContainerNetwork(t *testing.T) {
	cases := []struct {
		name string
		cfg  store.NodeConfig
		os   cluster.NodeOS
		ok   bool
	}{
		{name: "unset is NAT", os: cluster.OSLinux, ok: true},
		{name: "nat on linux", cfg: store.NodeConfig{ContainerNetwork: "nat"}, os: cluster.OSLinux, ok: true},
		{name: "transparent, VLAN 20", cfg: store.NodeConfig{ContainerNetwork: "transparent", TransparentAdapter: "Ethernet 3", TransparentVLAN: 20}, os: cluster.OSWindows, ok: true},
		{name: "transparent, untagged", cfg: store.NodeConfig{ContainerNetwork: "transparent", TransparentAdapter: "Ethernet 3"}, os: cluster.OSWindows, ok: true},
		{name: "transparent on a Linux node", cfg: store.NodeConfig{ContainerNetwork: "transparent", TransparentAdapter: "eth0"}, os: cluster.OSLinux},
		{name: "no adapter", cfg: store.NodeConfig{ContainerNetwork: "transparent"}, os: cluster.OSWindows},
		{name: "VLAN out of range", cfg: store.NodeConfig{ContainerNetwork: "transparent", TransparentAdapter: "Ethernet 3", TransparentVLAN: 4095}, os: cluster.OSWindows},
		{name: "control characters in the adapter", cfg: store.NodeConfig{ContainerNetwork: "transparent", TransparentAdapter: "Ether\nnet"}, os: cluster.OSWindows},
		{name: "unknown mode", cfg: store.NodeConfig{ContainerNetwork: "host"}, os: cluster.OSWindows},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateContainerNetwork(&tc.cfg, tc.os)
			if (err == nil) != tc.ok {
				t.Fatalf("err = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}

func TestNextContainerIP(t *testing.T) {
	cases := []struct {
		stored, reported string
		state            store.ServerState
		want             string
	}{
		{"", "192.168.20.182", store.StateRunning, "192.168.20.182"},             // first lease
		{"192.168.20.182", "192.168.20.90", store.StateRunning, "192.168.20.90"}, // moved
		{"192.168.20.182", "", store.StateOffline, "192.168.20.182"},             // stopped: keep it
		{"192.168.20.182", "", store.StateCrashed, "192.168.20.182"},             // crashed: keep it
		{"192.168.20.182", "", store.StateRunning, ""},                           // running behind NAT again
		{"", "", store.StateRunning, ""},                                         // a NAT server
	}
	for _, tc := range cases {
		if got := nextContainerIP(tc.stored, tc.reported, tc.state); got != tc.want {
			t.Errorf("nextContainerIP(%q, %q, %s) = %q, want %q", tc.stored, tc.reported, tc.state, got, tc.want)
		}
	}
}

// The address the Agent reports reaches the record, and a change hands the
// server to the sync that re-points the reservation and forwards.
func TestReconcile_ContainerAddress(t *testing.T) {
	s, st, agent := reconcileFixture(t, store.StateRunning, &agentpb.ServerStatus{
		State: agentpb.ServerState_SERVER_STATE_RUNNING, ContainerIp: "192.168.20.182",
	})
	var synced []string
	serverAddressHook = func(id string) { synced = append(synced, id) }
	t.Cleanup(func() { serverAddressHook = nil })
	ctx := context.Background()

	s.reconcileOnce(ctx)
	sv, _ := st.GetServer(ctx, "sv-1")
	if sv.ContainerIP != "192.168.20.182" || len(synced) != 1 {
		t.Fatalf("container_ip = %q, syncs %v; want the address stored and one sync", sv.ContainerIP, synced)
	}

	s.reconcileOnce(ctx)
	if len(synced) != 1 {
		t.Fatalf("an unchanged address synced again: %v", synced)
	}

	// Stopped: the Agent reports nothing, and the address stays.
	agent.status = &agentpb.ServerStatus{State: agentpb.ServerState_SERVER_STATE_OFFLINE}
	s.reconcileOnce(ctx)
	if sv, _ = st.GetServer(ctx, "sv-1"); sv.ContainerIP != "192.168.20.182" || len(synced) != 1 {
		t.Fatalf("a stopped server lost its address: %q, syncs %v", sv.ContainerIP, synced)
	}

	// Running without one: back behind NAT.
	agent.status = &agentpb.ServerStatus{State: agentpb.ServerState_SERVER_STATE_RUNNING}
	s.reconcileOnce(ctx)
	if sv, _ = st.GetServer(ctx, "sv-1"); sv.ContainerIP != "" || len(synced) != 2 {
		t.Fatalf("container_ip = %q, syncs %v; want it cleared and a second sync", sv.ContainerIP, synced)
	}
}

// fakeUnifi is a controller holding one network for VLAN 20 and recording what
// the Panel writes.
type fakeUnifi struct {
	mu       sync.Mutex
	reserved map[string]any // the last rest/user write
	forgot   []any
	forward  map[string]any // the last port-forward write
}

func (f *fakeUnifi) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/proxy/network/api/s/default")
	var body map[string]any
	if b, _ := io.ReadAll(r.Body); len(b) > 0 {
		_ = json.Unmarshal(b, &body)
	}
	switch {
	case r.Method == http.MethodGet && path == "/rest/networkconf":
		_, _ = io.WriteString(w, `{"data":[{"_id":"n1","ip_subnet":"192.168.0.100/24"},{"_id":"n20","ip_subnet":"192.168.20.1/24","vlan":20}]}`)
	case r.Method == http.MethodGet && path == "/rest/user":
		_, _ = io.WriteString(w, `{"data":[]}`)
	case path == "/rest/user":
		f.reserved = body
		_, _ = io.WriteString(w, `{"data":[]}`)
	case path == "/cmd/stamgr":
		f.forgot = append(f.forgot, body["macs"])
		_, _ = io.WriteString(w, `{"data":[]}`)
	case strings.HasPrefix(path, "/rest/portforward"):
		f.forward = body
		_, _ = io.WriteString(w, `{"data":[{"_id":"rule-1"}]}`)
	default:
		http.Error(w, "unexpected "+r.Method+" "+path, http.StatusTeapot)
	}
}

func TestSyncServerAddress(t *testing.T) {
	s, st, _ := reconcileFixture(t, store.StateRunning, &agentpb.ServerStatus{})
	f := &fakeUnifi{}
	ctrl := httptest.NewServer(f)
	t.Cleanup(ctrl.Close)
	ctx := context.Background()
	if err := st.SaveSettings(ctx, &store.Settings{UnifiURL: ctrl.URL, UnifiAPIKey: "k"}); err != nil {
		t.Fatalf("save settings: %v", err)
	}
	if err := st.CreateSpec(ctx, &spec.Spec{ID: "spec-1", Slug: "enshrouded", Name: "Enshrouded",
		Ports: []spec.Port{{Name: "game", Protocol: "udp", Default: 15637}}}); err != nil {
		t.Fatalf("create spec: %v", err)
	}
	sv, _ := st.GetServer(ctx, "sv-1")
	sv.SpecID, sv.Ports = "spec-1", map[string]int{"game": 28002}
	sv.ContainerIP = "192.168.20.182"
	sv.Forwards = map[string]store.PortForward{"game": {RuleID: "rule-1", Enabled: true}}
	if err := st.UpdateServer(ctx, sv); err != nil {
		t.Fatalf("update server: %v", err)
	}

	if err := s.syncServerAddress(ctx, "sv-1"); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if f.reserved["mac"] != serverMAC("sv-1") || f.reserved["fixed_ip"] != "192.168.20.182" || f.reserved["network_id"] != "n20" {
		t.Fatalf("reservation write %v; want the server's MAC pinned to its address on the VLAN 20 network", f.reserved)
	}
	if f.forward["fwd"] != "192.168.20.182" || f.forward["fwd_port"] != "28002" {
		t.Fatalf("forward write %v; want it aimed at the container", f.forward)
	}
	sv, _ = st.GetServer(ctx, "sv-1")
	if sv.Reservation == nil || sv.Reservation.IP != "192.168.20.182" {
		t.Fatalf("reservation not recorded: %+v", sv.Reservation)
	}

	// Back behind NAT: the reservation goes and the forward follows the node.
	sv.ContainerIP = ""
	_ = st.UpdateServer(ctx, sv)
	node, _ := st.GetNode(ctx, "node-1")
	node.LANHost = "192.168.0.89"
	_ = st.UpdateNode(ctx, node)
	if err := s.syncServerAddress(ctx, "sv-1"); err != nil {
		t.Fatalf("sync back to NAT: %v", err)
	}
	if len(f.forgot) != 1 || f.forward["fwd"] != "192.168.0.89" {
		t.Fatalf("forgot %v, forward %v; want the reservation released and the forward on the node", f.forgot, f.forward)
	}
	if sv, _ = st.GetServer(ctx, "sv-1"); sv.Reservation != nil {
		t.Fatalf("reservation still recorded: %+v", sv.Reservation)
	}
}

// Without UniFi there is nothing to pin or forward; the sync does nothing.
func TestSyncServerAddressWithoutUnifi(t *testing.T) {
	s, _, _ := reconcileFixture(t, store.StateRunning, &agentpb.ServerStatus{})
	if err := s.syncServerAddress(context.Background(), "sv-1"); err != nil {
		t.Fatalf("got %v", err)
	}
}
