package alerts

import (
	"slices"
	"sync"
	"time"

	"github.com/briggleman/kraken/internal/panel/cluster"
)

// The trackers remember the last thing each source saw, in memory, so a
// source can tell a change from a state. They are deliberately not persisted:
// a Panel that restarts takes the first value it sees as the baseline and says
// nothing about it, because "something changed while I was down" is a guess,
// and a guess at 3 a.m. is worse than silence.
//
// Each is safe for concurrent use. The clock is always passed in, so the
// windows can be tested without waiting them out.

// ---- Watchdog restarts ----

// CrashLoopWindow is the sliding window the escalation counts restarts in, and
// CrashLoopAt the restart in it that is sent as crash_loop instead of healed.
const (
	CrashLoopWindow = time.Hour
	CrashLoopAt     = 3
)

// WatchdogVerdict is what one observation of a server's watchdog count means.
type WatchdogVerdict struct {
	// Healed is one healed alert to send: the watchdog restarted the server
	// and it is not crashed now.
	Healed bool
	// CrashLoop replaces Healed when this increase brought the restarts in the
	// last hour to CrashLoopAt or more and no crash_loop has gone out for the
	// past hour. While one has, neither is set: the crash_loop already said it.
	CrashLoop bool
	// InWindow is how many restarts the last hour holds, for the sentence.
	InWindow int
}

type watchdogState struct {
	count    int32
	restarts []time.Time // inside the window, oldest first
	loopAt   time.Time   // when crash_loop was last sent; zero when never
}

// WatchdogTracker diffs each server's ServerStatus.watchdog_restarts against
// the last value seen for it.
type WatchdogTracker struct {
	mu      sync.Mutex
	servers map[string]*watchdogState
}

// NewWatchdogTracker returns an empty tracker.
func NewWatchdogTracker() *WatchdogTracker {
	return &WatchdogTracker{servers: map[string]*watchdogState{}}
}

// Observe records the count the Agent reported for a server and says what to
// send. The rules are the contract's and the Agent's (#415):
//
//   - The first count seen for a server is the baseline: nothing is sent.
//   - A count lower than the last is a reset (an operator start or restart,
//     or an Agent restart, arms a fresh watchdog): new baseline, nothing sent.
//   - A count that rose while the server now reads crashed is a restart that
//     failed, or the last one the budget allowed. The crash alert covers it,
//     and a "the fleet fixed itself" alert beside "it stopped" would
//     contradict it, so nothing is sent here — but the restarts still count
//     toward the window.
//   - Otherwise one healed alert per increase, however many restarts it
//     spans, except the escalation: the increase that brings the last hour to
//     CrashLoopAt restarts is sent as crash_loop instead.
//   - For an hour after a crash_loop, further restarts send nothing. The phone
//     has been told the server is crash-looping; "the fleet fixed itself" on
//     the next restart would contradict it. Once that hour has passed, a
//     restart is healed again — or, if the server is still looping, the next
//     crash_loop.
func (t *WatchdogTracker) Observe(serverID string, count int32, crashed bool, now time.Time) WatchdogVerdict {
	t.mu.Lock()
	defer t.mu.Unlock()
	st, seen := t.servers[serverID]
	if !seen {
		t.servers[serverID] = &watchdogState{count: count}
		return WatchdogVerdict{}
	}
	if count <= st.count {
		st.count = count // equal, or a reset
		return WatchdogVerdict{}
	}
	for i := st.count; i < count; i++ {
		st.restarts = append(st.restarts, now)
	}
	st.count = count
	cutoff := now.Add(-CrashLoopWindow)
	st.restarts = slices.DeleteFunc(st.restarts, func(at time.Time) bool { return !at.After(cutoff) })
	v := WatchdogVerdict{InWindow: len(st.restarts)}
	if crashed || (!st.loopAt.IsZero() && now.Sub(st.loopAt) < CrashLoopWindow) {
		return v
	}
	if v.InWindow >= CrashLoopAt {
		st.loopAt = now
		v.CrashLoop = true
		return v
	}
	v.Healed = true
	return v
}

// Retain forgets every server not in keep — the ones that were deleted — so
// the tracker does not grow with every server the Panel ever had.
func (t *WatchdogTracker) Retain(keep map[string]bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for id := range t.servers {
		if !keep[id] {
			delete(t.servers, id)
		}
	}
}

// ---- Rosters ----

// AliveInterval is how often one server may send an alive alert. Joins inside
// it are folded into the next one, so a party of five arriving together is
// one notification, not five.
const AliveInterval = time.Minute

type rosterState struct {
	names map[string]bool
	count int32
}

type pendingAlive struct {
	ref    ServerRef
	names  []string
	joined int // count-only joins
	online int32
	at     time.Time
}

// RosterTracker diffs each server's online players against the previous poll's
// and folds the joins into at most one alive alert per server per
// AliveInterval.
type RosterTracker struct {
	mu       sync.Mutex
	servers  map[string]*rosterState
	lastSent map[string]time.Time
	pending  map[string]*pendingAlive
}

// NewRosterTracker returns an empty tracker.
func NewRosterTracker() *RosterTracker {
	return &RosterTracker{
		servers:  map[string]*rosterState{},
		lastSent: map[string]time.Time{},
		pending:  map[string]*pendingAlive{},
	}
}

// Roster is one poll's view of a server's players.
type Roster struct {
	// Running is whether the Agent reports the server running. A server that
	// is not has nobody on it: its baseline empties, so the players who come
	// back after a crash or a start are joins.
	Running bool
	// Known is whether the game answered the player query. An unknown poll
	// while running keeps the previous baseline — one query that timed out
	// must not make everyone "join" again on the next.
	Known bool
	// Count is the number online; Names the roster, empty for a spec whose
	// query reports a count only.
	Count int32
	Names []string
}

// Observe records one poll and returns the alive alert to send now, if one is
// due. A join inside AliveInterval of the server's last alert is held and
// comes out of Flush once the interval has passed.
func (t *RosterTracker) Observe(ref ServerRef, r Roster, now time.Time) (Event, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !r.Running {
		t.servers[ref.ID] = &rosterState{names: map[string]bool{}}
		// Joins still held for the minute go with the players: sent after the
		// server stopped, "Kestrel joined · 2 online" would describe a game
		// that is down, right after the alert saying it stopped.
		delete(t.pending, ref.ID)
		return Event{}, false
	}
	if !r.Known {
		return Event{}, false
	}
	cur := &rosterState{names: make(map[string]bool, len(r.Names)), count: r.Count}
	for _, n := range r.Names {
		cur.names[n] = true
	}
	prev, seen := t.servers[ref.ID]
	t.servers[ref.ID] = cur
	if !seen {
		return Event{}, false
	}
	var joinedNames []string
	joined := 0
	if len(r.Names) > 0 {
		// Names in the order the Agent listed them, which is join order on
		// the runtimes that know it.
		for _, n := range r.Names {
			if !prev.names[n] && !slices.Contains(joinedNames, n) {
				joinedNames = append(joinedNames, n)
			}
		}
	} else if len(prev.names) == 0 && r.Count > prev.count {
		// A count-only spec: the game says how many, never who.
		joined = int(r.Count - prev.count)
	}
	if len(joinedNames) == 0 && joined == 0 {
		return Event{}, false
	}
	p := t.pending[ref.ID]
	if p == nil {
		p = &pendingAlive{}
		t.pending[ref.ID] = p
	}
	p.ref, p.online, p.at = ref, r.Count, now
	for _, n := range joinedNames {
		if !slices.Contains(p.names, n) {
			p.names = append(p.names, n)
		}
	}
	p.joined += joined
	if last, ok := t.lastSent[ref.ID]; ok && now.Sub(last) < AliveInterval {
		return Event{}, false
	}
	return t.take(ref.ID, now), true
}

// Flush returns the held alerts whose server's interval has passed.
func (t *RosterTracker) Flush(now time.Time) []Event {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []Event
	for id := range t.pending {
		if now.Sub(t.lastSent[id]) >= AliveInterval {
			out = append(out, t.take(id, now))
		}
	}
	return out
}

// take turns a server's held joins into one event and starts its interval.
// Called with the lock held.
func (t *RosterTracker) take(id string, now time.Time) Event {
	p := t.pending[id]
	delete(t.pending, id)
	t.lastSent[id] = now
	if len(p.names) > 0 {
		return PlayersJoined(p.ref, p.names, p.online, p.at)
	}
	return PlayersJoinedCount(p.ref, p.joined, p.online, p.at)
}

// Retain forgets every server not in keep.
func (t *RosterTracker) Retain(keep map[string]bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for id := range t.servers {
		if !keep[id] {
			delete(t.servers, id)
			delete(t.lastSent, id)
			delete(t.pending, id)
		}
	}
}

// ---- Node status ----

// NodeTracker turns node status observations into one alert per transition
// from up (online or cordoned) to down (offline or partial).
//
// It judges a transition by the last status it was told about, never by the
// caller's copy of the node: two paths can probe one node at once (the
// reconciler and an operator's ping, or a tunnel dropping mid-pass), each
// holding a copy that still read online, and both would otherwise report the
// same fall.
//
// The first status seen for a node after the Panel starts is its baseline and
// is never a fall. That is the trackers' rule everywhere, and here it matters
// most: a tunnel-mode node has not reconnected yet when the first pass after a
// Panel restart probes it, and its stored "online" against that first
// "offline" would page every operator on every Panel upgrade.
type NodeTracker struct {
	mu    sync.Mutex
	nodes map[string]cluster.NodeStatus
}

// NewNodeTracker returns an empty tracker.
func NewNodeTracker() *NodeTracker { return &NodeTracker{nodes: map[string]cluster.NodeStatus{}} }

// Observe records a node's status as a probe found it and reports whether it
// is a fall worth an alert.
func (t *NodeTracker) Observe(nodeID string, next cluster.NodeStatus) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	last, seen := t.nodes[nodeID]
	t.nodes[nodeID] = next
	return seen && nodeUp(last) && nodeDown(next)
}

func nodeUp(s cluster.NodeStatus) bool {
	return s == cluster.NodeOnline || s == cluster.NodeCordoned
}

func nodeDown(s cluster.NodeStatus) bool {
	return s == cluster.NodeOffline || s == cluster.NodePartial
}
