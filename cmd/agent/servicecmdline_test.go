package main

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The registered command line is the one string the SCM hands back, so every
// shape install.ps1 or an operator can produce has to decompose back into the
// argv the service process itself will see.
func TestSplitCommandLine(t *testing.T) {
	cases := []struct {
		name    string
		cmdLine string
		want    []string
	}{
		{"no args", `C:\kraken\bin\kraken-agent.exe`, []string{`C:\kraken\bin\kraken-agent.exe`}},
		{"root flag", `C:\kraken\bin\kraken-agent.exe --root C:\kraken`,
			[]string{`C:\kraken\bin\kraken-agent.exe`, "--root", `C:\kraken`}},
		{"root with a trailing backslash", `C:\kraken\bin\kraken-agent.exe --root C:\kraken\`,
			[]string{`C:\kraken\bin\kraken-agent.exe`, "--root", `C:\kraken\`}},
		{"quoted exe path with spaces", `"C:\Program Files\Kraken\kraken-agent.exe" --root C:\kraken`,
			[]string{`C:\Program Files\Kraken\kraken-agent.exe`, "--root", `C:\kraken`}},
		// syscall.EscapeArg doubles the trailing backslashes of a quoted
		// argument so the closing quote is not escaped away.
		{"quoted root with a trailing backslash", `kraken-agent.exe --root "C:\Program Files\Kraken\\"`,
			[]string{"kraken-agent.exe", "--root", `C:\Program Files\Kraken\`}},
		{"embedded quote", `kraken-agent.exe --node-id \"abyss\"`,
			[]string{"kraken-agent.exe", "--node-id", `"abyss"`}},
		{"doubled quote inside quotes", `kraken-agent.exe "a""b"`,
			[]string{"kraken-agent.exe", `a"b`}},
		{"runs of whitespace", "  kraken-agent.exe \t --root  C:\\kraken ",
			[]string{"kraken-agent.exe", "--root", `C:\kraken`}},
		{"empty", "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := splitCommandLine(tc.cmdLine)
			if len(got) != len(tc.want) {
				t.Fatalf("splitCommandLine(%q) = %q, want %q", tc.cmdLine, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("splitCommandLine(%q) = %q, want %q", tc.cmdLine, got, tc.want)
				}
			}
		})
	}
}

// Decomposition has to be the exact inverse of the escaping serviceCommandLine
// applies, or the status row would report drift on a command line that is
// byte-for-byte what install wrote.
func TestSplitCommandLineRoundTripsServiceCommandLine(t *testing.T) {
	exe := `C:\Program Files\Kraken\kraken-agent.exe`
	args := []string{"--root", `C:\kraken`, "--addr", ":9091"}
	cmdLine := serviceCommandLine(exe, args, testEscapeArg)

	argv := splitCommandLine(cmdLine)
	want := append([]string{exe}, args...)
	if len(argv) != len(want) {
		t.Fatalf("round trip of %q gave %q, want %q", cmdLine, argv, want)
	}
	for i := range argv {
		if argv[i] != want[i] {
			t.Fatalf("round trip of %q gave %q, want %q", cmdLine, argv, want)
		}
	}
}

// testEscapeArg is syscall.EscapeArg's rule (Windows-only, so it cannot be
// called from a test that runs on Linux) for the subset the round trip needs:
// quote an argument containing spaces, doubling the backslashes that would
// otherwise escape the closing quote.
func testEscapeArg(s string) string {
	if s == "" {
		return `""`
	}
	if !strings.ContainsAny(s, " \t\"") {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	slashes := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			slashes++
		case '"':
			for ; slashes >= 0; slashes-- {
				b.WriteByte('\\')
			}
			slashes = 0
		default:
			slashes = 0
		}
		b.WriteByte(s[i])
	}
	for ; slashes > 0; slashes-- {
		b.WriteByte('\\')
	}
	b.WriteByte('"')
	return b.String()
}

// testRoot is an absolute root for the platform the test is running on: the
// state-dir derivation goes through the real config package, which makes paths
// absolute, and a Windows path is not absolute on the Linux CI runner.
func testRoot() string {
	if runtime.GOOS == "windows" {
		return `C:\kraken`
	}
	return "/opt/kraken"
}

// The whole point of #273: the log path comes from the SERVICE's registered
// root, whatever the operator typed.
func TestStartLogNoticeUsesTheRegisteredStateDir(t *testing.T) {
	root := testRoot()
	cmdLine := serviceCommandLine(filepath.Join(root, "bin", "kraken-agent.exe"), []string{"--root", root}, testEscapeArg)

	got := startLogNotice(cmdLine, filepath.Join("C:", "Users", "benri"))
	want := "logs: " + filepath.Join(root, "state", "agent.log")
	if got != want {
		t.Errorf("startLogNotice = %q, want %q", got, want)
	}
}

// An explicit --state-dir outranks --root for the service too, so the notice
// must resolve it the same way the service process does.
func TestStartLogNoticeHonorsAnExplicitStateDir(t *testing.T) {
	root := testRoot()
	stateDir := filepath.Join(root, "agent-state")
	cmdLine := serviceCommandLine("kraken-agent.exe", []string{"--root", root, "--state-dir", stateDir}, testEscapeArg)

	got := startLogNotice(cmdLine, "typed")
	want := "logs: " + filepath.Join(stateDir, "agent.log")
	if got != want {
		t.Errorf("startLogNotice = %q, want %q", got, want)
	}
}

// When the SCM config could not be read the typed flags are all there is — but
// the message has to say so, because that path is how the wrong-path bug read.
func TestStartLogNoticeFallsBackToTheTypedFlags(t *testing.T) {
	typed := filepath.Join(testRoot(), "state")
	got := startLogNotice("", typed)
	want := "logs: " + filepath.Join(typed, "agent.log") +
		" (from the flags you typed; could not read the service command line)"
	if got != want {
		t.Errorf("startLogNotice = %q, want %q", got, want)
	}
}

// A command line the config package rejects is as unusable as an empty one.
func TestStartLogNoticeFallsBackOnAnUnparseableCommandLine(t *testing.T) {
	typed := filepath.Join(testRoot(), "state")
	got := startLogNotice(`kraken-agent.exe --node-os solaris`, typed)
	if !strings.Contains(got, "could not read the service command line") {
		t.Errorf("startLogNotice = %q, want the fallback wording", got)
	}
}

// No flags typed: compare the service against the flags it is registered with,
// so a healthy install does not report a DRIFT row it invented itself.
func TestStatusBaselineArgsUsesTheRegisteredFlagsWhenNoneWereTyped(t *testing.T) {
	args, fromRegistered := statusBaselineArgs(nil, `C:\kraken\bin\kraken-agent.exe --root C:\kraken`)
	if !fromRegistered {
		t.Fatal("expected the registered args to be used as the baseline")
	}
	if strings.Join(args, " ") != `--root C:\kraken` {
		t.Errorf("baseline args = %q, want [--root C:\\kraken]", args)
	}
}

// A service registered with no flags at all still yields a baseline (an empty
// one), which is the same comparison as before — not the typed-flags fallback.
func TestStatusBaselineArgsHandlesAServiceWithNoFlags(t *testing.T) {
	args, fromRegistered := statusBaselineArgs(nil, `C:\kraken\bin\kraken-agent.exe`)
	if !fromRegistered || len(args) != 0 {
		t.Errorf("baseline = (%q, %v), want (empty, true)", args, fromRegistered)
	}
}

// Typed flags win: "did install.ps1 register what I meant?" is a real question
// and status must keep answering it.
func TestStatusBaselineArgsPrefersTypedFlags(t *testing.T) {
	typed := []string{"--root", `D:\kraken`}
	args, fromRegistered := statusBaselineArgs(typed, `C:\kraken\bin\kraken-agent.exe --root C:\kraken`)
	if fromRegistered {
		t.Fatal("typed flags must win over the registered ones")
	}
	if strings.Join(args, " ") != `--root D:\kraken` {
		t.Errorf("baseline args = %q, want the typed flags", args)
	}
}

// Nothing typed and nothing readable from the SCM: fall back to the old
// behaviour rather than inventing a baseline.
func TestStatusBaselineArgsFallsBackWhenTheCommandLineIsUnreadable(t *testing.T) {
	args, fromRegistered := statusBaselineArgs(nil, "")
	if fromRegistered || len(args) != 0 {
		t.Errorf("baseline = (%q, %v), want (empty, false)", args, fromRegistered)
	}
}
