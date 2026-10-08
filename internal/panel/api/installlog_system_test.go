package api

import "testing"

// A system line (#392) lands on its own stream, beside installer output on the
// install stream, so the console can tell Kraken's account of the attempt
// from what the installer printed.
func TestInstallLog_AppendSystemStream(t *testing.T) {
	l := newInstallLog()
	l.Start("s1")
	l.AppendSystem("s1", "[panel] stop took 12s")
	l.Append("s1", "Success! App '1' fully installed.")
	snap := l.Snapshot("s1")
	if len(snap.Lines) != 2 {
		t.Fatalf("snapshot = %+v, want two lines", snap.Lines)
	}
	if snap.Lines[0].Stream != systemStreamName || snap.Lines[1].Stream != installStreamName {
		t.Errorf("streams = %q, %q; want %q then %q",
			snap.Lines[0].Stream, snap.Lines[1].Stream, systemStreamName, installStreamName)
	}
}
