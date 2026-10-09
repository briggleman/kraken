package alerts

import (
	"testing"
	"time"

	"github.com/briggleman/kraken/internal/panel/cluster"
	"github.com/briggleman/kraken/internal/panel/push"
)

var t0 = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

func TestWatchdogFirstCountIsTheBaseline(t *testing.T) {
	w := NewWatchdogTracker()
	// A Panel that restarts finds a server the watchdog has already restarted
	// twice: that is history, not news.
	if v := w.Observe("s", 2, false, t0); v != (WatchdogVerdict{}) {
		t.Fatalf("baseline = %+v, want nothing", v)
	}
	if v := w.Observe("s", 2, false, t0.Add(4*time.Second)); v != (WatchdogVerdict{}) {
		t.Fatalf("unchanged = %+v, want nothing", v)
	}
	if v := w.Observe("s", 3, false, t0.Add(8*time.Second)); !v.Healed || v.CrashLoop || v.InWindow != 1 {
		t.Fatalf("an increase = %+v, want one healed", v)
	}
}

func TestWatchdogDropIsAReset(t *testing.T) {
	w := NewWatchdogTracker()
	w.Observe("s", 2, false, t0)
	if v := w.Observe("s", 0, false, t0.Add(time.Second)); v != (WatchdogVerdict{}) {
		t.Fatalf("a drop = %+v, want nothing", v)
	}
	if v := w.Observe("s", 1, false, t0.Add(2*time.Second)); !v.Healed {
		t.Fatalf("an increase from the new baseline = %+v, want healed", v)
	}
}

func TestWatchdogIncreaseWhileCrashedSendsNoHealed(t *testing.T) {
	w := NewWatchdogTracker()
	w.Observe("s", 0, false, t0)
	if v := w.Observe("s", 1, true, t0.Add(time.Second)); v.Healed || v.CrashLoop {
		t.Fatalf("a restart that failed = %+v, want neither healed nor crash_loop", v)
	}
	// It still counted toward the window: two more restarts make three.
	w.Observe("s", 2, false, t0.Add(2*time.Second))
	if v := w.Observe("s", 3, false, t0.Add(3*time.Second)); !v.CrashLoop || v.InWindow != 3 {
		t.Fatalf("third in the hour = %+v, want crash_loop", v)
	}
}

func TestWatchdogEscalatesOncePerWindow(t *testing.T) {
	w := NewWatchdogTracker()
	w.Observe("s", 0, false, t0)
	at := func(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }
	steps := []struct {
		count    int32
		minute   int
		healed   bool
		loop     bool
		inWindow int
	}{
		{1, 1, true, false, 1},
		{2, 10, true, false, 2},
		{3, 20, false, true, 3},  // the third in the hour
		{4, 30, false, false, 4}, // the crash_loop already said it: nothing on the fourth...
		{5, 40, false, false, 5}, // ...or the fifth
		{6, 75, false, false, 4}, // 1 and 10 have slid out; still inside an hour of the crash_loop
		{7, 81, false, true, 4},  // an hour after the crash_loop and still looping: once more
		{8, 200, true, false, 1}, // a quiet two hours later, one restart is just healed
	}
	for _, s := range steps {
		v := w.Observe("s", s.count, false, at(s.minute))
		if v.Healed != s.healed || v.CrashLoop != s.loop || v.InWindow != s.inWindow {
			t.Fatalf("restart %d at +%dm = %+v, want healed %v crash_loop %v in window %d",
				s.count, s.minute, v, s.healed, s.loop, s.inWindow)
		}
	}
}

// After a crash_loop the server's restarts are silent for an hour; once the
// hour is up, a restart that is not part of a new loop is healed again.
func TestWatchdogSilentAfterCrashLoopThenHealedAgain(t *testing.T) {
	w := NewWatchdogTracker()
	w.Observe("s", 0, false, t0)
	at := func(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }
	w.Observe("s", 1, false, at(0))
	w.Observe("s", 2, false, at(1))
	if v := w.Observe("s", 3, false, at(2)); !v.CrashLoop {
		t.Fatalf("third = %+v, want crash_loop", v)
	}
	if v := w.Observe("s", 4, false, at(30)); v.Healed || v.CrashLoop {
		t.Fatalf("a restart inside the crash_loop's hour = %+v, want nothing", v)
	}
	if v := w.Observe("s", 5, false, at(62)); !v.Healed || v.CrashLoop || v.InWindow != 2 {
		t.Fatalf("a restart after the hour = %+v, want healed with two in the window", v)
	}
}

// Two restarts between polls are one increase: one alert, both counted.
func TestWatchdogOneAlertPerIncrease(t *testing.T) {
	w := NewWatchdogTracker()
	w.Observe("s", 0, false, t0)
	if v := w.Observe("s", 2, false, t0.Add(time.Second)); !v.Healed || v.InWindow != 2 {
		t.Fatalf("a jump of two = %+v, want one healed with two in the window", v)
	}
	if v := w.Observe("s", 3, false, t0.Add(2*time.Second)); !v.CrashLoop {
		t.Fatalf("the third = %+v, want crash_loop", v)
	}
}

func TestWatchdogRetainForgetsDeletedServers(t *testing.T) {
	w := NewWatchdogTracker()
	w.Observe("gone", 1, false, t0)
	w.Observe("kept", 1, false, t0)
	w.Retain(map[string]bool{"kept": true})
	// A forgotten server starts over at a baseline.
	if v := w.Observe("gone", 5, false, t0.Add(time.Second)); v.Healed {
		t.Fatal("a forgotten server's next count was taken as an increase")
	}
	if v := w.Observe("kept", 2, false, t0.Add(time.Second)); !v.Healed {
		t.Fatal("a kept server lost its baseline")
	}
}

var pw = ServerRef{ID: "s2", Name: "palworld-01", NodeID: "n1"}

func running(names ...string) Roster {
	return Roster{Running: true, Known: true, Count: int32(len(names)), Names: names}
}

func TestRosterNamesAJoin(t *testing.T) {
	r := NewRosterTracker()
	if _, ok := r.Observe(pw, running("Kestrel"), t0); ok {
		t.Fatal("the first roster is a baseline")
	}
	e, ok := r.Observe(pw, running("Kestrel", "Wren"), t0.Add(4*time.Second))
	if !ok || e.Body != "Wren joined palworld-01 · 2 online" || e.Class != push.ClassAlive {
		t.Fatalf("a join = %+v %v", e, ok)
	}
	// Someone leaving is not news.
	if _, ok := r.Observe(pw, running("Wren"), t0.Add(2*time.Minute)); ok {
		t.Fatal("a leave sent an alert")
	}
}

// Joins inside a minute of the server's last alert fold into one sentence,
// sent once the minute is up.
func TestRosterFoldsJoinsInsideAMinute(t *testing.T) {
	r := NewRosterTracker()
	r.Observe(pw, running(), t0)
	if _, ok := r.Observe(pw, running("Kestrel"), t0.Add(time.Second)); !ok {
		t.Fatal("the first join is sent at once")
	}
	if _, ok := r.Observe(pw, running("Kestrel", "Wren"), t0.Add(10*time.Second)); ok {
		t.Fatal("a join inside the minute was sent at once")
	}
	if _, ok := r.Observe(pw, running("Kestrel", "Wren", "Ash"), t0.Add(30*time.Second)); ok {
		t.Fatal("a join inside the minute was sent at once")
	}
	if got := r.Flush(t0.Add(50 * time.Second)); len(got) != 0 {
		t.Fatalf("flushed before the minute was up: %+v", got)
	}
	got := r.Flush(t0.Add(61 * time.Second))
	if len(got) != 1 || got[0].Body != "Wren and Ash joined palworld-01 · 3 online" {
		t.Fatalf("the folded alert = %+v", got)
	}
	if got := r.Flush(t0.Add(5 * time.Minute)); len(got) != 0 {
		t.Fatalf("flushed twice: %+v", got)
	}
	// The next join after a quiet minute goes at once again.
	if _, ok := r.Observe(pw, running("Kestrel", "Wren", "Ash", "Moss"), t0.Add(3*time.Minute)); !ok {
		t.Fatal("a join after the interval was held")
	}
}

// Joins held for the minute are dropped when the server stops: sent after its
// crash alert, they would describe a game that is down. (Found in the phase 5
// drill: "Kestrel and MossVeil joined" arrived nine seconds after the crash.)
func TestRosterDropsHeldJoinsWhenTheServerStops(t *testing.T) {
	r := NewRosterTracker()
	r.Observe(pw, running(), t0)
	if _, ok := r.Observe(pw, running("Kestrel"), t0.Add(time.Second)); !ok {
		t.Fatal("the first join is sent at once")
	}
	r.Observe(pw, running("Kestrel", "Wren"), t0.Add(10*time.Second)) // held
	r.Observe(pw, Roster{Running: false}, t0.Add(20*time.Second))     // the crash
	if got := r.Flush(t0.Add(2 * time.Minute)); len(got) != 0 {
		t.Fatalf("a held join was sent after the server stopped: %+v", got)
	}
}

func TestRosterCountOnly(t *testing.T) {
	r := NewRosterTracker()
	count := func(n int32) Roster { return Roster{Running: true, Known: true, Count: n} }
	r.Observe(pw, count(3), t0)
	e, ok := r.Observe(pw, count(4), t0.Add(4*time.Second))
	if !ok || e.Body != "a player joined · 4 online" {
		t.Fatalf("a count rise = %+v %v", e, ok)
	}
	if _, ok := r.Observe(pw, count(3), t0.Add(8*time.Second)); ok {
		t.Fatal("a count drop sent an alert")
	}
	r.Observe(pw, count(5), t0.Add(20*time.Second)) // +2, held
	got := r.Flush(t0.Add(70 * time.Second))
	if len(got) != 1 || got[0].Body != "2 players joined · 5 online" {
		t.Fatalf("folded count = %+v", got)
	}
}

// A query that did not answer keeps the baseline; a server that stopped has
// nobody on it, so whoever is there when it is back has joined.
func TestRosterUnknownKeepsAndStoppedEmpties(t *testing.T) {
	r := NewRosterTracker()
	r.Observe(pw, running("Kestrel"), t0)
	if _, ok := r.Observe(pw, Roster{Running: true}, t0.Add(4*time.Second)); ok {
		t.Fatal("an unknown poll sent an alert")
	}
	if _, ok := r.Observe(pw, running("Kestrel"), t0.Add(8*time.Second)); ok {
		t.Fatal("a player was announced again after one query blip")
	}
	r.Observe(pw, Roster{Running: false}, t0.Add(2*time.Minute))
	e, ok := r.Observe(pw, running("Kestrel"), t0.Add(3*time.Minute))
	if !ok || e.Body != "Kestrel joined palworld-01 · 1 online" {
		t.Fatalf("rejoin after the server came back = %+v %v", e, ok)
	}
}

func TestNodeTrackerOneAlertPerFall(t *testing.T) {
	n := NewNodeTracker()
	steps := []struct {
		status cluster.NodeStatus
		alert  bool
	}{
		{cluster.NodeOnline, false},   // baseline
		{cluster.NodeOnline, false},   // no change
		{cluster.NodeOffline, true},   // the fall
		{cluster.NodeOffline, false},  // every tick after it: not again
		{cluster.NodeOnline, false},   // back up
		{cluster.NodeCordoned, false}, // still up
		{cluster.NodePartial, true},   // cordoned → partial is a fall
		{cluster.NodeOffline, false},  // partial → offline: already down
		{cluster.NodeOnline, false},
		{cluster.NodePartial, true},
	}
	for i, s := range steps {
		if got := n.Observe("n1", s.status); got != s.alert {
			t.Fatalf("step %d (%s): alert %v, want %v", i, s.status, got, s.alert)
		}
	}
}

// The first status after a Panel start is a baseline: a tunnel node that has
// not reconnected yet reads offline, and that is not a fall.
func TestNodeTrackerBaselineIsNotAFall(t *testing.T) {
	n := NewNodeTracker()
	if n.Observe("n1", cluster.NodeOffline) {
		t.Fatal("the first observation alerted")
	}
	if n.Observe("n1", cluster.NodeOnline) || !n.Observe("n1", cluster.NodeOffline) {
		t.Fatal("a fall after the baseline did not alert")
	}
}
