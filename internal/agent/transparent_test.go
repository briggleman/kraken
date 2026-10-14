package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/go-connections/nat"

	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// fakeNetworks is a Docker daemon's network list, recording creates.
type fakeNetworks struct {
	nets    []network.Summary
	created []string
	opts    map[string]string
	listErr error
}

func (f *fakeNetworks) NetworkList(_ context.Context, o network.ListOptions) ([]network.Summary, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	var out []network.Summary
	for _, n := range f.nets {
		if d := o.Filters.Get("driver"); len(d) > 0 && !strings.EqualFold(n.Driver, d[0]) {
			continue
		}
		if nm := o.Filters.Get("name"); len(nm) > 0 && n.Name != nm[0] {
			continue
		}
		out = append(out, n)
	}
	return out, nil
}

func (f *fakeNetworks) NetworkCreate(_ context.Context, name string, o network.CreateOptions) (network.CreateResponse, error) {
	f.created = append(f.created, name)
	f.opts = o.Options
	f.nets = append(f.nets, network.Summary{Name: name, Driver: o.Driver, Options: o.Options})
	return network.CreateResponse{ID: "id-" + name}, nil
}

func transparentNetSummary(name, adapter, vlan string) network.Summary {
	opts := map[string]string{optWindowsInterface: adapter}
	if vlan != "" {
		opts[optWindowsVLAN] = vlan
	}
	return network.Summary{Name: name, Driver: "transparent", Options: opts}
}

func TestEnsureTransparent(t *testing.T) {
	want := transparentSettings{Adapter: "Ethernet 3", VLAN: 20}

	t.Run("adopts the operator's network on the same adapter and VLAN", func(t *testing.T) {
		f := &fakeNetworks{nets: []network.Summary{
			{Name: "nat", Driver: "nat"},
			transparentNetSummary("my-games", "ethernet 3", "20"),
		}}
		name, err := ensureTransparent(context.Background(), f, want)
		if err != nil || name != "my-games" || len(f.created) != 0 {
			t.Fatalf("got %q, %v, created %v; want my-games adopted", name, err, f.created)
		}
	})
	t.Run("creates one when none matches", func(t *testing.T) {
		f := &fakeNetworks{nets: []network.Summary{transparentNetSummary("other", "Ethernet 3", "30")}}
		name, err := ensureTransparent(context.Background(), f, want)
		if err != nil || name != "kraken-vlan20" {
			t.Fatalf("got %q, %v; want kraken-vlan20 created", name, err)
		}
		if f.opts[optWindowsInterface] != "Ethernet 3" || f.opts[optWindowsVLAN] != "20" {
			t.Fatalf("created with options %v", f.opts)
		}
	})
	t.Run("untagged: no VLAN option, its own name", func(t *testing.T) {
		f := &fakeNetworks{}
		name, err := ensureTransparent(context.Background(), f, transparentSettings{Adapter: "Ethernet 3"})
		if err != nil || name != "kraken-transparent" {
			t.Fatalf("got %q, %v", name, err)
		}
		if _, ok := f.opts[optWindowsVLAN]; ok {
			t.Fatalf("an untagged network was created with a VLAN option: %v", f.opts)
		}
	})
	t.Run("refuses a same-named network with other settings", func(t *testing.T) {
		f := &fakeNetworks{nets: []network.Summary{transparentNetSummary("kraken-vlan20", "Ethernet 5", "20")}}
		if _, err := ensureTransparent(context.Background(), f, want); err == nil || len(f.created) != 0 {
			t.Fatalf("err = %v, created %v; want a refusal and nothing created", err, f.created)
		}
	})
	t.Run("a daemon error is reported", func(t *testing.T) {
		f := &fakeNetworks{listErr: errors.New("daemon down")}
		if _, err := ensureTransparent(context.Background(), f, want); err == nil {
			t.Fatal("want an error")
		}
	})
}

func TestTransparentSettingsRoundTrip(t *testing.T) {
	state := t.TempDir()
	f := &fakeNetworks{}
	d := &DockerRuntime{osType: "windows", specDir: filepath.Join(state, "agent-specs"), networks: f}

	cfg := &agentpb.NodeConfig{ContainerNetwork: "transparent", TransparentAdapter: " Ethernet 3 ", TransparentVlan: 20}
	line := d.applyTransparentConfig(cfg)
	if !strings.Contains(line, "drops off") {
		t.Fatalf("first apply should warn about the drop, got %q", line)
	}
	if _, err := os.Stat(d.transparentStatePath()); err != nil {
		t.Fatalf("settings not saved: %v", err)
	}

	// A fresh Agent restores and ensures them before it dials the Panel. (Its
	// own daemon fake: the first runtime may still be building in the background.)
	d2 := &DockerRuntime{osType: "windows", specDir: filepath.Join(state, "agent-specs"), networks: &fakeNetworks{}}
	d2.RestoreTransparentNetwork(context.Background())
	if name, err := d2.transparentNetwork(); err != nil || name != "kraken-vlan20" {
		t.Fatalf("after restore: %q, %v", name, err)
	}
	// The Panel's next push of the same settings changes nothing.
	if line := d2.applyTransparentConfig(cfg); !strings.Contains(line, "kraken-vlan20") {
		t.Fatalf("unchanged push should report the network, got %q", line)
	}

	// Back to NAT: the saved settings go, the network stays.
	if line := d2.applyTransparentConfig(&agentpb.NodeConfig{}); !strings.Contains(line, "left in place") {
		t.Fatalf("got %q", line)
	}
	if _, err := os.Stat(d2.transparentStatePath()); !os.IsNotExist(err) {
		t.Fatalf("settings file should be gone, stat err = %v", err)
	}
	if _, err := d2.transparentNetwork(); err == nil {
		t.Fatal("a NAT node has no transparent network to join")
	}
}

func TestApplyTransparentConfigIgnoredOnLinux(t *testing.T) {
	d := &DockerRuntime{osType: "linux", specDir: filepath.Join(t.TempDir(), "agent-specs")}
	if line := d.applyTransparentConfig(&agentpb.NodeConfig{ContainerNetwork: "transparent", TransparentAdapter: "eth0"}); line != "" {
		t.Fatalf("got %q", line)
	}
}

func TestJoinTransparent(t *testing.T) {
	d := &DockerRuntime{osType: "windows"}
	d.transparent.settings = &transparentSettings{Adapter: "Ethernet 3", VLAN: 20}
	d.transparent.name = "kraken-vlan20"

	cfg := &container.Config{ExposedPorts: nat.PortSet{"28000/udp": {}}}
	host := &container.HostConfig{PortBindings: nat.PortMap{"28000/udp": {{HostPort: "28000"}}}}
	sp := &agentpb.ServerSpec{NetworkMode: "transparent", MacAddress: "02:4b:aa:bb:cc:dd"}
	netCfg, err := d.joinTransparent(sp, cfg, host)
	if err != nil {
		t.Fatalf("joinTransparent: %v", err)
	}
	if host.NetworkMode != "kraken-vlan20" || host.PortBindings != nil || cfg.ExposedPorts != nil {
		t.Fatalf("network %q, bindings %v, exposed %v; want the network and nothing published", host.NetworkMode, host.PortBindings, cfg.ExposedPorts)
	}
	if ep := netCfg.EndpointsConfig["kraken-vlan20"]; ep == nil || ep.MacAddress != "02:4b:aa:bb:cc:dd" {
		t.Fatalf("endpoint config %+v; want the server's MAC", netCfg.EndpointsConfig)
	}

	// A NAT spec is untouched.
	host2 := &container.HostConfig{PortBindings: host.PortBindings}
	if netCfg, err := d.joinTransparent(&agentpb.ServerSpec{}, &container.Config{}, host2); err != nil || netCfg != nil || host2.NetworkMode != "" {
		t.Fatalf("NAT spec changed: %v %v %q", netCfg, err, host2.NetworkMode)
	}

	// Not ready: the start fails with the reason instead of falling back to NAT.
	d.transparent.name, d.transparent.err = "", "create network kraken-vlan20: access denied"
	if _, err := d.joinTransparent(sp, &container.Config{}, &container.HostConfig{}); err == nil || !strings.Contains(err.Error(), "access denied") {
		t.Fatalf("err = %v; want the network's failure", err)
	}
	// A Linux node never joins one.
	if _, err := (&DockerRuntime{osType: "linux"}).joinTransparent(sp, &container.Config{}, &container.HostConfig{}); err == nil {
		t.Fatal("want an error on Linux")
	}
}

func TestParseIPConfig(t *testing.T) {
	out := "\r\nWindows IP Configuration\r\n\r\n\r\nEthernet adapter vEthernet (Ethernet):\r\n\r\n" +
		"   Connection-specific DNS Suffix  . : localdomain\r\n" +
		"   Link-local IPv6 Address . . . . . : fe80::1c4e:9b1a:2d3f:4a5b%17\r\n" +
		"   IPv4 Address. . . . . . . . . . . : 192.168.20.182\r\n" +
		"   Subnet Mask . . . . . . . . . . . : 255.255.255.0\r\n" +
		"   Default Gateway . . . . . . . . . : 192.168.20.1\r\n"
	if got := parseIPConfig(out); got != "192.168.20.182" {
		t.Fatalf("got %q", got)
	}
	auto := "   Autoconfiguration IPv4 Address. . : 169.254.12.7(Preferred)\r\n"
	if got := parseIPConfig(auto); got != "" {
		t.Fatalf("a link-local address is not a lease, got %q", got)
	}
	if got := parseIPConfig("   IPv4 Address. . . : 192.168.20.90(Preferred) \r\n"); got != "192.168.20.90" {
		t.Fatalf("the /all form: got %q", got)
	}
}
