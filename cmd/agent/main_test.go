package main

import (
	"strconv"
	"testing"
)

// TestIsLoopbackAddr locks down the classifier the plaintext-gRPC guard
// depends on. If this ever loosens, an operator could accidentally expose
// the unauthenticated NodeService to the LAN.
func TestIsLoopbackAddr(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		// Loopback — safe to serve plaintext gRPC.
		{"127.0.0.1:9090", true},
		{"[::1]:9090", true},
		{"localhost:9090", true},
		// Non-loopback — LAN-reachable, must not serve plaintext.
		{":9090", false},         // empty host = all interfaces
		{"0.0.0.0:9090", false},  // explicit all-interfaces v4
		{"[::]:9090", false},     // explicit all-interfaces v6
		{"10.0.0.5:9090", false}, // private LAN
		{"192.168.1.20:9090", false},
		{"example.com:9090", false}, // resolves off-host
		// Malformed input is treated as non-loopback so the guard fires
		// rather than silently allowing something we can't classify.
		{"9090", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isLoopbackAddr(c.addr); got != c.want {
			t.Errorf("isLoopbackAddr(%q) = %v, want %v", c.addr, got, c.want)
		}
	}
}

// TestStripServiceFlag — the registered service command line must be the
// install invocation minus the --service control flag (both spellings), with
// every configuration flag preserved verbatim.
func TestStripServiceFlag(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{[]string{"--service", "install", "--root", `C:\kraken`}, `--root C:\kraken`},
		{[]string{"--root", `C:\kraken`, "-service", "install"}, `--root C:\kraken`},
		{[]string{"--service=install", "--addr", ":9091"}, "--addr :9091"},
		{[]string{"--root", `C:\kraken`}, `--root C:\kraken`},
		{[]string{}, ""},
	}
	for _, c := range cases {
		got := joinArgs(stripServiceFlag(c.in))
		if got != c.want {
			t.Errorf("stripServiceFlag(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestJoinArgsQuotesSpaces(t *testing.T) {
	got := joinArgs([]string{"--root", `C:\Program Files\kraken`})
	want := "--root " + strconv.Quote(`C:\Program Files\kraken`)
	if got != want {
		t.Errorf("joinArgs = %q, want %q", got, want)
	}
}
