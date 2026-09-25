package api

import (
	"testing"
)

func TestInstallLog_TailSnapshotThenLive(t *testing.T) {
	l := newInstallLog()
	l.Start("s1")
	l.Append("s1", "first")
	l.Append("s1", "second")

	snap, ch, done, cancel := l.Tail("s1")
	defer cancel()
	if done {
		t.Fatal("a running install must not read as done")
	}
	if len(snap) != 2 || snap[0].Text != "first" || snap[1].Text != "second" {
		t.Fatalf("snapshot = %+v, want the two buffered lines in order", snap)
	}

	l.Append("s1", "third")
	got := <-ch
	if got.Text != "third" {
		t.Fatalf("live line = %q, want %q", got.Text, "third")
	}
	if got.Stream != installStreamName {
		t.Fatalf("stream = %q, want %q", got.Stream, installStreamName)
	}
}

// A subscriber must see every line that lands after its snapshot and none that
// landed before it — the whole point of taking both under one lock.
func TestInstallLog_TailDoesNotDuplicateOrDropAcrossTheHandoff(t *testing.T) {
	l := newInstallLog()
	l.Start("s1")
	for _, txt := range []string{"a", "b", "c"} {
		l.Append("s1", txt)
	}
	snap, ch, _, cancel := l.Tail("s1")
	defer cancel()
	l.Append("s1", "d")

	seen := make([]string, 0, 4)
	for _, s := range snap {
		seen = append(seen, s.Text)
	}
	seen = append(seen, (<-ch).Text)
	want := []string{"a", "b", "c", "d"}
	if len(seen) != len(want) {
		t.Fatalf("seen = %v, want %v", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("seen = %v, want %v", seen, want)
		}
	}
}

func TestInstallLog_FinishClosesSubscribersAndKeepsTheBuffer(t *testing.T) {
	l := newInstallLog()
	l.Start("s1")
	l.Append("s1", "line")
	_, ch, _, cancel := l.Tail("s1")
	defer cancel()

	l.Finish("s1")
	if _, ok := <-ch; ok {
		t.Fatal("Finish must close live subscribers")
	}
	// The failure must stay readable after the attempt ends.
	snap, ch2, done, cancel2 := l.Tail("s1")
	defer cancel2()
	if !done {
		t.Fatal("a finished attempt must read as done")
	}
	if len(snap) != 1 || snap[0].Text != "line" {
		t.Fatalf("snapshot after Finish = %+v, want the buffered line", snap)
	}
	if _, ok := <-ch2; ok {
		t.Fatal("a done entry must hand back a closed channel")
	}
}

// Without this a drill-in on an install_failed server after a Panel restart
// would hold a socket open forever waiting for output that cannot come.
func TestInstallLog_UnknownServerReadsAsDone(t *testing.T) {
	l := newInstallLog()
	snap, ch, done, cancel := l.Tail("nobody")
	defer cancel()
	if !done || snap != nil {
		t.Fatalf("done=%v snap=%+v, want done with no lines", done, snap)
	}
	if _, ok := <-ch; ok {
		t.Fatal("want a closed channel for an unknown server")
	}
}

// The live tail is the new attempt only: the previous one is kept for the REST
// read (#381), never merged into what a subscriber is following.
func TestInstallLog_StartClosesThePreviousAttemptsTail(t *testing.T) {
	l := newInstallLog()
	l.Start("s1")
	l.Append("s1", "old attempt")
	_, ch, _, cancel := l.Tail("s1")
	defer cancel()

	l.Start("s1") // a reinstall
	if _, ok := <-ch; ok {
		t.Fatal("a reinstall must close the previous attempt's subscribers")
	}
	snap, _, _, cancel2 := l.Tail("s1")
	defer cancel2()
	if len(snap) != 0 {
		t.Fatalf("snapshot after Start = %+v, want empty", snap)
	}
}

func TestInstallLog_DropForgetsTheServer(t *testing.T) {
	l := newInstallLog()
	l.Start("s1")
	l.Append("s1", "line")
	l.Drop("s1")
	_, _, done, cancel := l.Tail("s1")
	defer cancel()
	if !done {
		t.Fatal("a dropped server must read as done, not as a live install")
	}
}

func TestInstallLog_CapsTheBufferAtTheTail(t *testing.T) {
	l := newInstallLog()
	l.Start("s1")
	for i := 0; i < maxInstallLines+50; i++ {
		l.Append("s1", string(rune('a'+i%26)))
	}
	snap, _, _, cancel := l.Tail("s1")
	defer cancel()
	if len(snap) != maxInstallLines {
		t.Fatalf("buffered %d lines, want the cap of %d", len(snap), maxInstallLines)
	}
}

// A browser that stops reading must not wedge the install: the slow subscriber
// is dropped, the installer keeps going.
func TestInstallLog_SlowSubscriberIsDroppedNotBlocked(t *testing.T) {
	l := newInstallLog()
	l.Start("s1")
	_, ch, _, cancel := l.Tail("s1")
	defer cancel()
	for i := 0; i < 1000; i++ { // far past the subscription buffer
		l.Append("s1", "flood")
	}
	drained := 0
	for range ch {
		drained++
	}
	if drained == 0 {
		t.Fatal("want the dropped subscriber's channel closed after draining")
	}
	snap, _, _, cancel2 := l.Tail("s1")
	defer cancel2()
	if len(snap) != maxInstallLines {
		t.Fatalf("install buffer = %d lines, want %d — the append path stalled", len(snap), maxInstallLines)
	}
}

func TestInstallLog_ErrorLinesCarryTheErrorStream(t *testing.T) {
	l := newInstallLog()
	l.Start("s1")
	l.AppendError("s1", "[panel] install failed: no subscription")
	snap, _, _, cancel := l.Tail("s1")
	defer cancel()
	if len(snap) != 1 || snap[0].Stream != "error" {
		t.Fatalf("snapshot = %+v, want one line on the error stream", snap)
	}
}

// Snapshot is what the REST endpoint reads: the whole attempt, without taking a
// subscription. A finished install must still hand back its lines — the #280
// case, where an installer exited 0 and left a broken tree behind.
func TestInstallLog_SnapshotSurvivesTheAttempt(t *testing.T) {
	l := newInstallLog()
	l.Start("s1")
	l.Append("s1", "downloading")
	l.Finish("s1")

	snap := l.Snapshot("s1")
	if !snap.Retained {
		t.Fatal("a finished install must still read as retained")
	}
	if !snap.Done {
		t.Error("a finished install must read as done")
	}
	if len(snap.Lines) != 1 || snap.Lines[0].Text != "downloading" {
		t.Fatalf("lines = %+v, want the buffered line", snap.Lines)
	}
	if snap.StartedAt.IsZero() || snap.FinishedAt.IsZero() {
		t.Errorf("want both timestamps set; started=%v finished=%v", snap.StartedAt, snap.FinishedAt)
	}
	if snap.FinishedAt.Before(snap.StartedAt) {
		t.Errorf("finished %v is before started %v", snap.FinishedAt, snap.StartedAt)
	}
}

// "Nothing retained" and "an install that printed nothing" are different
// answers: only the first means the Panel restarted since the attempt, and the
// UI says so in words rather than showing a blank pane.
func TestInstallLog_SnapshotOfAnUnknownServerIsNotRetained(t *testing.T) {
	l := newInstallLog()
	snap := l.Snapshot("nobody")
	if snap.Retained {
		t.Fatal("a server with no buffer must not read as retained")
	}
	if len(snap.Lines) != 0 {
		t.Fatalf("lines = %+v, want none", snap.Lines)
	}

	l.Start("s1")
	l.Finish("s1")
	if empty := l.Snapshot("s1"); !empty.Retained {
		t.Fatal("an attempt that printed nothing is still retained")
	}
}

// A reinstall replaces the current attempt rather than adding to it: an
// operator reading the log after a retry must see the retry, not a merge. (The
// replaced attempt is kept apart, as Previous — see below.)
func TestInstallLog_SnapshotAfterAReinstallShowsOnlyTheNewAttempt(t *testing.T) {
	l := newInstallLog()
	l.Start("s1")
	l.Append("s1", "first attempt")
	l.Finish("s1")

	l.Start("s1")
	l.Append("s1", "second attempt")
	snap := l.Snapshot("s1")
	if snap.Done {
		t.Error("a running reinstall must not read as done")
	}
	if len(snap.Lines) != 1 || snap.Lines[0].Text != "second attempt" {
		t.Fatalf("lines = %+v, want only the second attempt", snap.Lines)
	}
}

// #381: pressing REINSTALL on a failed pass used to throw away the output of
// the pass it was retrying — the only record of why it failed. Start keeps it,
// read-only, as Previous.
func TestInstallLog_StartKeepsThePreviousAttempt(t *testing.T) {
	l := newInstallLog()
	l.Start("s1")
	l.Append("s1", "downloading")
	l.AppendError("s1", "[panel] install failed: state is 0x6")
	l.Finish("s1")
	first := l.Snapshot("s1")

	l.Start("s1")
	snap := l.Snapshot("s1")
	if len(snap.Lines) != 0 {
		t.Fatalf("current lines = %+v, want the new attempt empty", snap.Lines)
	}
	if snap.Previous == nil {
		t.Fatal("the attempt a reinstall replaced must be kept as Previous")
	}
	p := snap.Previous
	if len(p.Lines) != 2 || p.Lines[0].Text != "downloading" || p.Lines[1].Stream != "error" {
		t.Fatalf("previous lines = %+v, want the failed attempt's two lines, error stream kept", p.Lines)
	}
	if !p.Done || !p.Retained {
		t.Errorf("previous done=%v retained=%v, want both true", p.Done, p.Retained)
	}
	if !p.StartedAt.Equal(first.StartedAt) || !p.FinishedAt.Equal(first.FinishedAt) {
		t.Errorf("previous bracket %v–%v, want the first attempt's %v–%v",
			p.StartedAt, p.FinishedAt, first.StartedAt, first.FinishedAt)
	}
	if p.Previous != nil {
		t.Error("a previous attempt carries no Previous of its own")
	}
}

// One back is the bound: a third attempt keeps the second and drops the first.
func TestInstallLog_ASecondStartDropsTheOlderAttempt(t *testing.T) {
	l := newInstallLog()
	for _, txt := range []string{"attempt one", "attempt two"} {
		l.Start("s1")
		l.Append("s1", txt)
		l.Finish("s1")
	}
	l.Start("s1")
	snap := l.Snapshot("s1")
	if snap.Previous == nil || len(snap.Previous.Lines) != 1 || snap.Previous.Lines[0].Text != "attempt two" {
		t.Fatalf("previous = %+v, want attempt two only", snap.Previous)
	}
	if snap.Previous.Previous != nil {
		t.Fatal("attempt one is still reachable — the chain must stop at one back")
	}
	l.mu.Lock()
	chained := l.entries["s1"].previous.previous
	l.mu.Unlock()
	if chained != nil {
		t.Fatal("the entry kept a pointer two back; memory is no longer bounded at two tails")
	}
}

// Append and Finish address the current attempt. Neither may reach into the
// previous one — it is a read-only record of a pass that is over.
func TestInstallLog_AppendAndFinishLeaveThePreviousAlone(t *testing.T) {
	l := newInstallLog()
	l.Start("s1")
	l.Append("s1", "old")
	l.Finish("s1")
	before := l.Snapshot("s1")

	l.Start("s1")
	l.Append("s1", "new")
	l.AppendError("s1", "[panel] new failure")
	mid := l.Snapshot("s1")
	if mid.Done {
		t.Error("the running reinstall must not read as done because its previous is")
	}
	l.Finish("s1")
	after := l.Snapshot("s1")

	for _, snap := range []installSnapshot{mid, after} {
		p := snap.Previous
		if p == nil || len(p.Lines) != 1 || p.Lines[0].Text != "old" {
			t.Fatalf("previous = %+v, want only the old attempt's line", p)
		}
		if !p.FinishedAt.Equal(before.FinishedAt) {
			t.Errorf("previous finished %v, want it untouched at %v", p.FinishedAt, before.FinishedAt)
		}
	}
	if len(after.Lines) != 2 || after.Lines[0].Text != "new" {
		t.Fatalf("current lines = %+v, want the reinstall's own two", after.Lines)
	}
}

// A pass superseded before it reached a verdict (never the normal path — the
// reinstall handler refuses while installing — but Start must not leave a
// half-open entry behind) reads as done, with no finish time: it was cut off.
func TestInstallLog_ASupersededRunningAttemptReadsAsDoneWithoutAFinish(t *testing.T) {
	l := newInstallLog()
	l.Start("s1")
	l.Append("s1", "half way")
	l.Start("s1")
	p := l.Snapshot("s1").Previous
	if p == nil || !p.Done {
		t.Fatalf("previous = %+v, want a done attempt", p)
	}
	if !p.FinishedAt.IsZero() {
		t.Errorf("previous finished %v, want zero — it never reached a verdict", p.FinishedAt)
	}
}

func TestInstallLog_FirstAttemptHasNoPrevious(t *testing.T) {
	l := newInstallLog()
	if l.Snapshot("nobody").Previous != nil {
		t.Error("an unknown server has no previous attempt")
	}
	l.Start("s1")
	if l.Snapshot("s1").Previous != nil {
		t.Error("a first attempt has no previous attempt")
	}
}

func TestInstallLog_DropForgetsThePreviousToo(t *testing.T) {
	l := newInstallLog()
	l.Start("s1")
	l.Append("s1", "old")
	l.Start("s1")
	l.Drop("s1")
	snap := l.Snapshot("s1")
	if snap.Retained || snap.Previous != nil {
		t.Fatalf("snapshot after Drop = %+v, want nothing retained and no previous", snap)
	}
	l.Start("s1") // a new server that reuses nothing
	if l.Snapshot("s1").Previous != nil {
		t.Fatal("a Start after Drop resurrected the dropped attempt")
	}
}
