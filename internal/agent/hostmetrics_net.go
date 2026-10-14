package agent

import (
	"os"
	"strings"
)

// netExcludeEnv names the interfaces the node network meter leaves out. It
// exists for one machine shape: two nodes on one box, where a NIC the host
// hands to another node's VM still shows up as hardware on this one. abyss-win
// and abyss-lnx are that shape — the Realtek port is a Hyper-V switch given
// over to the WSL VM, so its bytes are abyss-lnx's traffic, and counting them on
// abyss-win as well would report one node's game traffic on two meters.
const netExcludeEnv = "KRAKEN_NET_EXCLUDE"

// netExcludeFromEnv parses KRAKEN_NET_EXCLUDE: interface names, comma-separated,
// matched case-insensitively. On Windows a name matches the adapter's alias
// ("Ethernet 2") or its description ("Realtek Gaming 2.5GbE Family Controller").
func netExcludeFromEnv() map[string]bool {
	return parseNetExclude(os.Getenv(netExcludeEnv))
}

func parseNetExclude(v string) map[string]bool {
	out := map[string]bool{}
	for name := range strings.SplitSeq(v, ",") {
		if name = strings.ToLower(strings.TrimSpace(name)); name != "" {
			out[name] = true
		}
	}
	return out
}

// overlayIfacePrefixes are VPN and overlay interfaces. Their traffic rides a
// physical NIC that is already counted, so the name-based fallbacks skip them.
var overlayIfacePrefixes = []string{"tailscale", "wg", "wireguard", "zt", "zerotier"}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// ifCounters is one interface's octet counters plus what the OS says it is.
// Windows fills it from MIB_IF_ROW2; it is OS-neutral so the selection rules
// below are testable on any machine.
type ifCounters struct {
	alias, description string
	up                 bool
	loopback           bool
	hardware           bool // MIB_IF_ROW2 HardwareInterface
	filter             bool // MIB_IF_ROW2 FilterInterface: a lightweight-filter instance of another row
	rx, tx             uint64
}

// excluded reports whether the operator listed this interface.
func (c ifCounters) excluded(exclude map[string]bool) bool {
	return exclude[strings.ToLower(c.alias)] || exclude[strings.ToLower(c.description)]
}

// sumIfRows totals the node's traffic across the interface table.
//
// It counts the hardware NICs that are up. A NIC bound to a Hyper-V external
// switch still counts here: it carries the host's own traffic through the
// switch's vEthernet adapter, and a transparent container's traffic beside it,
// so it is the one row that sees everything (#432). The vEthernet adapters are
// skipped, being software, as are filter rows, which repeat a NIC's own
// counters and would double them.
//
// When no hardware NIC qualifies — a VM whose adapters all read as software —
// it falls back to every up row that is not loopback, a filter, an overlay or
// excluded, rather than reporting nothing. Double-counting a hardware NIC is
// impossible there by construction, since none was counted.
func sumIfRows(rows []ifCounters, exclude map[string]bool) (rx, tx uint64, ok bool) {
	usable := func(c ifCounters) bool {
		return c.up && !c.loopback && !c.filter && !c.excluded(exclude)
	}
	for _, c := range rows {
		if usable(c) && c.hardware {
			rx += c.rx
			tx += c.tx
			ok = true
		}
	}
	if ok {
		return rx, tx, true
	}
	for _, c := range rows {
		if usable(c) && !hasAnyPrefix(strings.ToLower(c.alias), overlayIfacePrefixes) {
			rx += c.rx
			tx += c.tx
			ok = true
		}
	}
	return rx, tx, ok
}
