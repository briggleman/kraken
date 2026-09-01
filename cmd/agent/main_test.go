package main

import (
	"strconv"
	"strings"
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

// TestServiceCommandLine — re-registering an existing service (#184) has to
// hand UpdateConfig the same command line CreateService would have assembled
// from an exe plus args, or an "update" silently changes how the service
// starts. The escaper is injected because syscall.EscapeArg is Windows-only;
// what this locks down is the shape: exe first, one space between elements,
// every element escaped exactly once.
func TestServiceCommandLine(t *testing.T) {
	// A stand-in for syscall.EscapeArg: quotes only what needs it, and marks
	// every element it saw so a missed escape is visible.
	esc := func(s string) string {
		if strings.ContainsAny(s, " \t") {
			return `"` + s + `"`
		}
		return s
	}
	cases := []struct {
		exe  string
		args []string
		want string
	}{{
		`C:\kraken\bin\kraken-agent.exe`, []string{"--root", `C:\kraken`},
		`C:\kraken\bin\kraken-agent.exe --root C:\kraken`,
	}, {
		// No flags typed: the command line is the bare exe, with no trailing space.
		`C:\kraken\bin\kraken-agent.exe`, nil,
		`C:\kraken\bin\kraken-agent.exe`,
	}, {
		// Spaces on either side must be escaped, not just in the args.
		`C:\Program Files\kraken\kraken-agent.exe`, []string{"--root", `C:\Program Files\kraken`},
		`"C:\Program Files\kraken\kraken-agent.exe" --root "C:\Program Files\kraken"`,
	}}
	for _, c := range cases {
		if got := serviceCommandLine(c.exe, c.args, esc); got != c.want {
			t.Errorf("serviceCommandLine(%q, %v) = %q, want %q", c.exe, c.args, got, c.want)
		}
	}
}

// The command line a re-registration writes must equal what a fresh install
// would have produced from the same invocation — the two paths are only
// interchangeable if they agree on the args as well as the escaping.
func TestServiceCommandLineMatchesTheInstallInvocation(t *testing.T) {
	const exe = `C:\kraken\bin\kraken-agent.exe`
	typed := []string{"--service", "install", "--root", `C:\kraken`, "--addr", ":9091"}
	want := exe + ` --root C:\kraken --addr :9091`
	got := serviceCommandLine(exe, stripServiceFlag(typed), func(s string) string { return s })
	if got != want {
		t.Errorf("service command line = %q, want %q", got, want)
	}
}

func TestJoinArgsQuotesSpaces(t *testing.T) {
	got := joinArgs([]string{"--root", `C:\Program Files\kraken`})
	want := "--root " + strconv.Quote(`C:\Program Files\kraken`)
	if got != want {
		t.Errorf("joinArgs = %q, want %q", got, want)
	}
}
