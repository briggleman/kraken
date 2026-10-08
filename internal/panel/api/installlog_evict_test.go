package api

import (
	"fmt"
	"testing"
)

// A chatty installer must not push Kraken's own step lines out of the buffer
// (#392): the ring evicts installer output first, so the stop, the image
// check and the guard's removals from the top of a pass survive thousands of
// SteamCMD progress ticks, and the failure note at the end survives too.
func TestInstallLog_EvictsInstallerOutputBeforeSystemLines(t *testing.T) {
	l := newInstallLog()
	l.Start("s1")
	l.AppendSystem("s1", "[panel] stopping sv before the update")
	l.AppendSystem("s1", "[panel] stop took 5ms")
	l.AppendSystem("s1", "[kraken] image check took 329ms")
	for i := 0; i < maxInstallLines*4; i++ {
		l.Append("s1", fmt.Sprintf(" Update state (0x5) validating, progress: %d", i))
	}
	l.AppendError("s1", "[panel] install failed: state is 0x6")

	snap := l.Snapshot("s1")
	if len(snap.Lines) != maxInstallLines {
		t.Fatalf("buffer holds %d lines, want the cap %d", len(snap.Lines), maxInstallLines)
	}
	heads := []string{"[panel] stopping sv before the update", "[panel] stop took 5ms", "[kraken] image check took 329ms"}
	for i, want := range heads {
		if snap.Lines[i].Text != want || snap.Lines[i].Stream != systemStreamName {
			t.Errorf("line %d = %+v, want the system line %q still at the top", i, snap.Lines[i], want)
		}
	}
	if last := snap.Lines[len(snap.Lines)-1]; last.Stream != "error" {
		t.Errorf("last line = %+v, want the failure note", last)
	}
	// What was evicted is the OLDEST installer output: the tail of the ticks is
	// intact, in order, right up to the failure note.
	ticks := snap.Lines[len(heads) : len(snap.Lines)-1]
	first := maxInstallLines*4 - len(ticks)
	for i, ln := range ticks {
		if want := fmt.Sprintf(" Update state (0x5) validating, progress: %d", first+i); ln.Text != want {
			t.Fatalf("tick %d = %q, want %q (the newest installer lines must be the ones kept)", i, ln.Text, want)
		}
	}
}

// With nothing but Kraken's own lines in the buffer the cap still holds: the
// oldest goes, so a pathological pass cannot grow without bound.
func TestInstallLog_EvictsOldestWhenNoInstallerOutput(t *testing.T) {
	l := newInstallLog()
	l.Start("s1")
	for i := 0; i < maxInstallLines+5; i++ {
		l.AppendSystem("s1", fmt.Sprintf("[panel] step %d", i))
	}
	snap := l.Snapshot("s1")
	if len(snap.Lines) != maxInstallLines {
		t.Fatalf("buffer holds %d lines, want %d", len(snap.Lines), maxInstallLines)
	}
	if snap.Lines[0].Text != "[panel] step 5" {
		t.Errorf("first line = %q, want the five oldest gone", snap.Lines[0].Text)
	}
}
