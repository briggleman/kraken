package agent

import "testing"

// The rows abyss-win showed on 2026-10-14, with a Docker transparent network on
// its I226-V and the Realtek given to the WSL VM's "WSL LAN" switch. The
// physical NIC has no IP (the host's address moved to its vEthernet adapter),
// which is what took it out of net.Interfaces() and the meter dark (#432).
func abyssWinRows() []ifCounters {
	return []ifCounters{
		{alias: "Ethernet 3", description: "Intel(R) Ethernet Controller I226-V", up: true, hardware: true, rx: 1000, tx: 100},
		{alias: "Ethernet 3-WFP Native MAC Layer LightWeight Filter-0000", description: "Intel(R) Ethernet Controller I226-V", up: true, hardware: true, filter: true, rx: 1000, tx: 100},
		{alias: "vEthernet (Ethernet 3)", description: "Hyper-V Virtual Ethernet Adapter", up: true, rx: 600, tx: 60},
		{alias: "Ethernet", description: "Realtek Gaming 2.5GbE Family Controller", up: true, hardware: true, rx: 5000, tx: 500},
		{alias: "Ethernet 4", description: "Intel(R) Ethernet Converged Network Adapter X710", hardware: true, rx: 0, tx: 0}, // no cable
		{alias: "vEthernet (Default Switch)", description: "Hyper-V Virtual Ethernet Adapter #2", up: true, rx: 7, tx: 7},
		{alias: "Tailscale", description: "Tailscale Tunnel", up: true, rx: 30, tx: 30},
		{alias: "Loopback Pseudo-Interface 1", up: true, loopback: true, rx: 9, tx: 9},
	}
}

func TestSumIfRows(t *testing.T) {
	cases := []struct {
		name    string
		rows    []ifCounters
		exclude string
		rx, tx  uint64
		ok      bool
	}{
		{
			name: "counts every up hardware NIC, one bound to a vSwitch included, once",
			rows: abyssWinRows(), rx: 1000 + 5000, tx: 100 + 500, ok: true,
		},
		{
			name: "a NIC handed to another node's VM is left out by alias",
			rows: abyssWinRows(), exclude: "ethernet", rx: 1000, tx: 100, ok: true,
		},
		{
			name: "or by description, ignoring case and spaces around the list",
			rows: abyssWinRows(), exclude: " realtek gaming 2.5gbe family controller , nope", rx: 1000, tx: 100, ok: true,
		},
		{
			name: "no hardware NIC: falls back to up software rows, skipping overlays and filters",
			rows: []ifCounters{
				{alias: "vEthernet (WSL)", up: true, rx: 40, tx: 4},
				{alias: "Ethernet 2-Npcap Packet Driver (NPCAP)-0000", up: true, filter: true, rx: 40, tx: 4},
				{alias: "Tailscale", up: true, rx: 30, tx: 3},
				{alias: "Loopback Pseudo-Interface 1", up: true, loopback: true, rx: 9, tx: 9},
				{alias: "Ethernet 5", rx: 1, tx: 1}, // down
			},
			rx: 40, tx: 4, ok: true,
		},
		{
			name:    "every hardware NIC excluded: the fallback still honours the list",
			rows:    []ifCounters{{alias: "Ethernet", up: true, hardware: true, rx: 5, tx: 5}},
			exclude: "Ethernet",
		},
		{name: "empty table is unknown", rows: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rx, tx, ok := sumIfRows(tc.rows, parseNetExclude(tc.exclude))
			if rx != tc.rx || tx != tc.tx || ok != tc.ok {
				t.Fatalf("sumIfRows = (%d, %d, %v), want (%d, %d, %v)", rx, tx, ok, tc.rx, tc.tx, tc.ok)
			}
		})
	}
}
