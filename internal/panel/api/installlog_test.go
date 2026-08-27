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

func TestInstallLog_StartDiscardsThePreviousAttempt(t *testing.T) {
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
