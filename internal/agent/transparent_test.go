package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// restoredWhileDockerDown is a Windows Agent whose startup restore of the
// transparent network ran before Docker was up: the settings are known, the
// network isn't, and the failure is recorded.
func restoredWhileDockerDown(t *testing.T) (*DockerRuntime, *fakeNetworks) {
	t.Helper()
	state := t.TempDir()
	f := &fakeNetworks{listErr: errors.New("open //./pipe/docker_engine: The system cannot find the file specified.")}
	d := &DockerRuntime{osType: "windows", specDir: filepath.Join(state, "agent-specs"), networks: f}
	if err := d.saveTransparent(&transparentSettings{Adapter: "Ethernet 3", VLAN: 20}); err != nil {
		t.Fatal(err)
	}
	d.RestoreTransparentNetwork(context.Background())
	if _, err := d.transparentNetwork(); err == nil || !strings.Contains(err.Error(), "docker_engine") {
		t.Fatalf("after a restore with Docker down: err = %v; want the daemon's failure", err)
	}
	return d, f
}

// waitTransparent waits for a background build to settle.
func waitTransparent(t *testing.T, d *DockerRuntime) (string, error) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		d.transparent.mu.Lock()
		building := d.transparent.building
		d.transparent.mu.Unlock()
		if !building {
			return d.transparentNetwork()
		}
		if time.Now().After(deadline) {
			t.Fatal("the background build never finished")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// After a reboot the Agent can start before Docker; once Docker is up, a start
// adopts the network that's still there instead of repeating the boot-time
// failure until someone restarts the Agent.
func TestReadyTransparentAdoptsAfterStartupFailure(t *testing.T) {
	d, f := restoredWhileDockerDown(t)
	f.listErr = nil
	f.nets = []network.Summary{transparentNetSummary("kraken-vlan20", "Ethernet 3", "20")}

	sp := &agentpb.ServerSpec{NetworkMode: "transparent"}
	host := &container.HostConfig{}
	if _, err := d.joinTransparent(context.Background(), sp, &container.Config{}, host); err != nil {
		t.Fatalf("start after Docker came back: %v", err)
	}
	if host.NetworkMode != "kraken-vlan20" || len(f.created) != 0 {
		t.Fatalf("network %q, created %v; want the existing network adopted", host.NetworkMode, f.created)
	}
	if name, err := d.transparentNetwork(); err != nil || name != "kraken-vlan20" {
		t.Fatalf("not remembered: %q, %v", name, err)
	}
}

// A start that finds the network gone doesn't create it in its own call (that
// drops the call); it fails saying so and builds it in the background.
func TestReadyTransparentBuildsMissingInBackground(t *testing.T) {
	d, f := restoredWhileDockerDown(t)
	f.listErr = nil

	_, err := d.readyTransparent(context.Background())
	if err == nil || !strings.Contains(err.Error(), "being built") {
		t.Fatalf("err = %v; want a being-built failure", err)
	}
	if name, err := waitTransparent(t, d); err != nil || name != "kraken-vlan20" {
		t.Fatalf("after the background build: %q, %v", name, err)
	}
	if len(f.created) != 1 {
		t.Fatalf("created %v; want exactly one network", f.created)
	}
}

// Docker answering for the first time retries a network the startup lookup
// missed, without waiting for a start.
func TestRetryTransparentOnDockerRecovery(t *testing.T) {
	d, f := restoredWhileDockerDown(t)
	f.listErr = nil
	f.nets = []network.Summary{transparentNetSummary("my-games", "Ethernet 3", "20")}

	d.retryTransparent()
	if name, err := waitTransparent(t, d); err != nil || name != "my-games" {
		t.Fatalf("after the retry: %q, %v", name, err)
	}

	// Ready already, or a NAT node: nothing to retry.
	d.retryTransparent()
	if _, err := waitTransparent(t, d); err != nil || len(f.created) != 0 {
		t.Fatalf("err %v, created %v; a ready network is left alone", err, f.created)
	}
	nat := &DockerRuntime{osType: "windows"}
	nat.retryTransparent()
	if nat.transparent.building {
		t.Fatal("a NAT node started a build")
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
	ctx := context.Background()
	netCfg, err := d.joinTransparent(ctx, sp, cfg, host)
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
	if netCfg, err := d.joinTransparent(ctx, &agentpb.ServerSpec{}, &container.Config{}, host2); err != nil || netCfg != nil || host2.NetworkMode != "" {
		t.Fatalf("NAT spec changed: %v %v %q", netCfg, err, host2.NetworkMode)
	}

	// Not ready: the start fails with the reason instead of falling back to NAT.
	d.networks = &fakeNetworks{listErr: errors.New("access denied")}
	d.transparent.name, d.transparent.err = "", "list networks: daemon down"
	if _, err := d.joinTransparent(ctx, sp, &container.Config{}, &container.HostConfig{}); err == nil || !strings.Contains(err.Error(), "access denied") {
		t.Fatalf("err = %v; want the network's failure", err)
	}
	// A Linux node never joins one.
	if _, err := (&DockerRuntime{osType: "linux"}).joinTransparent(ctx, sp, &container.Config{}, &container.HostConfig{}); err == nil {
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
