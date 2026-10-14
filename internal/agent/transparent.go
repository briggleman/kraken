package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"

	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// Transparent networking for game containers on a Windows node (#426).
//
// Behind Windows container NAT, a game that opens a random UDP port for its
// session traffic — Enshrouded, through Steam networking — has that port
// unpublished, falls back to Steam's relay, and its players sit on the loading
// screen. On a Docker transparent network the container takes its own address
// on the LAN by DHCP and every port it opens is reachable.
//
// The network is bound to a host adapter and, optionally, tags a VLAN. Creating
// it binds a Hyper-V external switch to that adapter: Windows moves the host's
// own address onto the switch's vEthernet adapter, and the host's connections —
// this Agent's tunnel to the Panel among them — drop for up to a minute. So the
// Agent creates it only at startup, before it dials the Panel, or in the
// background after answering the node-config push that asked for it, and it
// never removes one: removing it drops the host the same way. An operator who
// turns the mode off removes the network by hand when nothing needs it.
//
// The node's choice is stamped on each server's spec by the Panel
// (ServerSpec.network_mode), so a server joins the network on its next start;
// the network settings here only say which network that is.

const (
	transparentDriver   = "transparent"
	optWindowsInterface = "com.docker.network.windowsshim.interface"
	optWindowsVLAN      = "com.docker.network.windowsshim.vlanid"
	networkModeTransp   = "transparent"
	transparentStateFn  = "transparent-network.json"
	transparentTimeout  = 3 * time.Minute
)

// transparentSettings is the node's transparent network: the host adapter it
// binds and the VLAN it tags (0 = untagged).
type transparentSettings struct {
	Adapter string `json:"adapter"`
	VLAN    int32  `json:"vlan,omitempty"`
}

// transparentNetName is the name the Agent gives a network it creates. An
// existing transparent network on the same adapter and VLAN is adopted under
// whatever name it has.
func (t transparentSettings) netName() string {
	if t.VLAN > 0 {
		return fmt.Sprintf("kraken-vlan%d", t.VLAN)
	}
	return "kraken-transparent"
}

// networkOps is the slice of the Docker client the transparent network needs.
// *client.Client satisfies it; tests use a fake.
type networkOps interface {
	NetworkList(ctx context.Context, options network.ListOptions) ([]network.Summary, error)
	NetworkCreate(ctx context.Context, name string, options network.CreateOptions) (network.CreateResponse, error)
}

// transparentNet is the Agent's view of the node's transparent network.
type transparentNet struct {
	mu       sync.Mutex
	settings *transparentSettings // nil: the node is on NAT
	name     string               // the resolved network, "" until ensured
	err      string               // why it isn't ready, "" when it is or is being built
	building bool
}

// matchTransparent finds a transparent network bound to the adapter and VLAN.
// A network that holds the name the Agent would use but differs is reported:
// creating beside it would fail, and adopting it would put containers on the
// wrong adapter.
func matchTransparent(nets []network.Summary, want transparentSettings) (string, error) {
	for _, n := range nets {
		if !strings.EqualFold(n.Driver, transparentDriver) {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(n.Options[optWindowsInterface]), want.Adapter) {
			continue
		}
		if vlanOption(n.Options) != want.VLAN {
			continue
		}
		return n.Name, nil
	}
	for _, n := range nets {
		if n.Name == want.netName() {
			return "", fmt.Errorf("a network named %s already exists but is not a transparent network on %q, VLAN %d; remove it or change the node's settings",
				n.Name, want.Adapter, want.VLAN)
		}
	}
	return "", nil
}

func vlanOption(opts map[string]string) int32 {
	v, err := strconv.Atoi(strings.TrimSpace(opts[optWindowsVLAN]))
	if err != nil || v < 0 || v > 4094 {
		return 0
	}
	return int32(v) // #nosec G115 -- range-checked above
}

// ensureTransparent returns the node's transparent network, creating it when
// none on the adapter and VLAN exists.
func ensureTransparent(ctx context.Context, ops networkOps, want transparentSettings) (string, error) {
	nets, err := ops.NetworkList(ctx, network.ListOptions{Filters: filters.NewArgs(filters.Arg("driver", transparentDriver))})
	if err != nil {
		return "", fmt.Errorf("list networks: %w", err)
	}
	// A driver filter is advisory on some daemons; list everything named ours too.
	all, err := ops.NetworkList(ctx, network.ListOptions{Filters: filters.NewArgs(filters.Arg("name", want.netName()))})
	if err == nil {
		nets = append(nets, all...)
	}
	if name, err := matchTransparent(nets, want); err != nil || name != "" {
		return name, err
	}
	opts := map[string]string{optWindowsInterface: want.Adapter}
	if want.VLAN > 0 {
		opts[optWindowsVLAN] = strconv.Itoa(int(want.VLAN))
	}
	slog.Warn("creating the transparent network for game containers; the host's network connections drop while Windows moves its address onto the new switch",
		"network", want.netName(), "adapter", want.Adapter, "vlan", want.VLAN)
	if _, err := ops.NetworkCreate(ctx, want.netName(), network.CreateOptions{Driver: transparentDriver, Options: opts}); err != nil {
		return "", fmt.Errorf("create network %s on %q: %w", want.netName(), want.Adapter, err)
	}
	return want.netName(), nil
}

// transparentFromConfig reads the node config's network settings; nil for NAT.
func transparentFromConfig(cfg *agentpb.NodeConfig) *transparentSettings {
	if cfg.GetContainerNetwork() != networkModeTransp || strings.TrimSpace(cfg.GetTransparentAdapter()) == "" {
		return nil
	}
	return &transparentSettings{Adapter: strings.TrimSpace(cfg.GetTransparentAdapter()), VLAN: cfg.GetTransparentVlan()}
}

func (d *DockerRuntime) transparentStatePath() string {
	return filepath.Join(filepath.Dir(d.specDir), transparentStateFn)
}

// applyTransparentConfig takes the network settings from a node-config push.
// A change is saved, so the next startup can ensure the network before it
// dials the Panel, and a network to build is built in the background: building
// it drops the tunnel this push arrived on. It returns a line for the push's
// detail.
func (d *DockerRuntime) applyTransparentConfig(cfg *agentpb.NodeConfig) string {
	if !d.isWindows() {
		return ""
	}
	want := transparentFromConfig(cfg)
	t := &d.transparent
	t.mu.Lock()
	unchanged := (want == nil && t.settings == nil) || (want != nil && t.settings != nil && *want == *t.settings)
	if unchanged {
		line := t.statusLine()
		t.mu.Unlock()
		return line
	}
	t.settings, t.name, t.err = want, "", ""
	t.mu.Unlock()

	if err := d.saveTransparent(want); err != nil {
		slog.Warn("could not save the transparent network settings; they apply until the Agent restarts", "err", err)
	}
	if want == nil {
		return "container network: nat (an existing transparent network is left in place; remove it by hand when nothing uses it)"
	}
	d.buildTransparent(*want)
	return "container network: transparent — building the network on " + strconv.Quote(want.Adapter) +
		"; this node drops off for up to a minute while Windows moves its address"
}

// buildTransparent ensures the network in the background.
func (d *DockerRuntime) buildTransparent(want transparentSettings) {
	t := &d.transparent
	t.mu.Lock()
	if t.building {
		t.mu.Unlock()
		return
	}
	t.building = true
	t.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), transparentTimeout)
		defer cancel()
		d.ensureTransparentNow(ctx, want)
		t.mu.Lock()
		t.building = false
		t.mu.Unlock()
	}()
}

// ensureTransparentNow ensures the network and records the outcome, unless the
// settings moved on while it ran.
func (d *DockerRuntime) ensureTransparentNow(ctx context.Context, want transparentSettings) {
	name, err := ensureTransparent(ctx, d.netOps(), want)
	t := &d.transparent
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.settings == nil || *t.settings != want {
		return
	}
	if err != nil {
		t.err = err.Error()
		slog.Error("transparent network not ready; transparent game servers on this node cannot start", "err", err)
		return
	}
	t.name, t.err = name, ""
	slog.Info("transparent network ready", "network", name, "adapter", want.Adapter, "vlan", want.VLAN)
}

func (d *DockerRuntime) netOps() networkOps {
	if d.networks != nil {
		return d.networks
	}
	return d.cli
}

// statusLine describes the network for a node-config push's detail. Caller
// holds t.mu.
func (t *transparentNet) statusLine() string {
	switch {
	case t.settings == nil:
		return ""
	case t.name != "":
		return "container network: transparent on " + t.name
	case t.err != "":
		return "container network: transparent, not ready: " + t.err
	default:
		return "container network: transparent, being built"
	}
}

// transparentNetwork is the network a transparent game container joins, or why
// there is none.
func (d *DockerRuntime) transparentNetwork() (string, error) {
	t := &d.transparent
	t.mu.Lock()
	defer t.mu.Unlock()
	switch {
	case t.settings == nil:
		return "", errors.New("this node has no transparent network configured; set its container network in node settings")
	case t.name != "":
		return t.name, nil
	case t.err != "":
		return "", errors.New("the node's transparent network is not ready: " + t.err)
	default:
		return "", errors.New("the node's transparent network is still being built; try again in a minute")
	}
}

func (d *DockerRuntime) saveTransparent(s *transparentSettings) error {
	p := d.transparentStatePath()
	if s == nil {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o600)
}

// RestoreTransparentNetwork ensures the saved transparent network at startup,
// before the Agent dials the Panel. The network normally exists already —
// Docker keeps it across restarts — so this is a lookup; it is only slow, and
// only drops the host's connections, when it has to rebuild one. Without saved
// settings, or on Linux, it does nothing.
func (d *DockerRuntime) RestoreTransparentNetwork(ctx context.Context) {
	if !d.isWindows() {
		return
	}
	b, err := os.ReadFile(d.transparentStatePath())
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("could not read the saved transparent network settings", "err", err)
		}
		return
	}
	var s transparentSettings
	if err := json.Unmarshal(b, &s); err != nil || s.Adapter == "" {
		slog.Warn("ignoring unreadable transparent network settings", "path", d.transparentStatePath(), "err", err)
		return
	}
	d.transparent.mu.Lock()
	d.transparent.settings = &s
	d.transparent.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, transparentTimeout)
	defer cancel()
	d.ensureTransparentNow(ctx, s)
}

// containerAddress is a transparent game container's own address (#426): where
// the Panel points players, the port forward and the reservation, and where the
// Agent sends its player query. "" for a server on the default network, a
// container that isn't running, or one whose lease hasn't landed.
//
// Docker's inspect is asked first. A Windows container that leased its address
// by DHCP can be reported without it, so the container itself is asked next —
// once per container, the answer kept until the container is replaced.
func (d *DockerRuntime) containerAddress(ctx context.Context, serverID string) string {
	sp, ok := d.getSpec(serverID)
	if !ok || sp.GetNetworkMode() != networkModeTransp {
		return ""
	}
	insp, err := d.containers.ContainerInspect(ctx, containerName(serverID))
	if err != nil || insp.State == nil || !insp.State.Running {
		return ""
	}
	name, _ := d.transparentNetwork()
	if insp.NetworkSettings != nil {
		if ep, ok := insp.NetworkSettings.Networks[name]; ok && ep != nil && validLeasedIP(ep.IPAddress) {
			return ep.IPAddress
		}
	}
	if ip, ok := d.addrCache.get(insp.ID); ok {
		return ip
	}
	out, err := d.execCapture(ctx, serverID, []string{"ipconfig"})
	if err != nil {
		return ""
	}
	ip := parseIPConfig(string(out))
	if ip != "" {
		d.addrCache.put(insp.ID, ip)
	}
	return ip
}

// validLeasedIP rejects what a container reports before DHCP answers: nothing,
// or the 169.254 link-local address Windows assigns itself.
func validLeasedIP(ip string) bool {
	a, err := netip.ParseAddr(strings.TrimSpace(ip))
	return err == nil && a.Is4() && !a.IsLinkLocalUnicast() && !a.IsLoopback() && !a.IsUnspecified()
}

// parseIPConfig takes the first leased IPv4 address from Windows `ipconfig`.
// A container has one adapter, so the first is the one.
func parseIPConfig(out string) string {
	for line := range strings.SplitSeq(out, "\n") {
		label, value, found := strings.Cut(line, ":")
		if !found || !strings.Contains(label, "IPv4 Address") {
			continue
		}
		ip := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(value), "(Preferred)"))
		if validLeasedIP(ip) {
			return ip
		}
	}
	return ""
}

// addrCache keeps a container's exec-read address until it is replaced, which
// is the only way its lease can move.
type addrCache struct {
	mu sync.Mutex
	m  map[string]string // container id → address
}

func (c *addrCache) get(id string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ip, ok := c.m[id]
	return ip, ok
}

func (c *addrCache) put(id, ip string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil || len(c.m) > 256 {
		c.m = map[string]string{}
	}
	c.m[id] = ip
}
