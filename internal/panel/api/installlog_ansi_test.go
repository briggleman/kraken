package api

import "testing"

// SteamCMD wraps its output in ANSI colour resets. The console never renders
// them, and the ESC byte is invisible, so an operator saw `[0m` at the start of
// every Steam line (#392). The buffer strips the sequences on the way in, so
// the REST body, the WebSocket replay and the retained log are all clean.
func TestInstallLog_StripsANSISequences(t *testing.T) {
	cases := map[string]string{
		"\x1b[0m Update state (0x5) verifying install, progress: 36.08": " Update state (0x5) verifying install, progress: 36.08",
		"\x1b[0mSuccess! App '2394010' fully installed.":                "Success! App '2394010' fully installed.",
		"Loading Steam API...\x1b[0mOK":                                 "Loading Steam API...OK",
		"\x1b[1;32mgreen\x1b[0m and \x1b[38;5;208morange\x1b[0m":        "green and orange",
		"\x1b]0;window title\x07plain":                                  "plain",
		"no escapes here":                                               "no escapes here",
	}
	l := newInstallLog()
	l.Start("s1")
	var want []string
	for in, out := range cases {
		l.Append("s1", in)
		want = append(want, out)
	}
	snap := l.Snapshot("s1")
	if len(snap.Lines) != len(cases) {
		t.Fatalf("got %d lines, want %d", len(snap.Lines), len(cases))
	}
	for i, ln := range snap.Lines {
		if ln.Text != want[i] {
			t.Errorf("line %d = %q, want %q", i, ln.Text, want[i])
		}
	}
}
