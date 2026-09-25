package api

import (
	"sync"
	"time"
)

// installLog holds the output of each server's install phase so an operator can
// read it — while it runs and after it fails.
//
// The install runs in the Panel's own provision goroutine (handlers_server.go),
// not in a container the console WebSocket can tail: the Agent streams the
// installer's lines to the Panel over gRPC and there is nothing left to attach
// to afterwards. Without this buffer those lines are consumed and dropped, so
// "installing" is a blank wait of unbounded length and "install failed" carries
// one summary line for what may have been a 20-minute SteamCMD download.
//
// Buffers live only in memory, one per server, capped at maxInstallLines. They
// survive the install — success as well as failure — so the account of what the
// installer actually did stays readable afterwards, and are dropped when the
// server is retired or deleted. A new attempt demotes the current one to
// `previous` rather than discarding it, and exactly one attempt back is kept:
// pressing REINSTALL on a failed pass used to erase the very output that said
// why it failed (#381). Retention is therefore bounded at two tails per server,
// and the server count is bounded by the node's memory and port reservations.
//
// Keeping a *successful* install's output is the fix for #280: an installer can
// exit 0 having produced a broken tree (a SteamCMD self-update race downloads
// half a game and still reports success), and once the state moves past
// installing the console has nothing left to tail. Being in memory, the buffer
// does not survive a Panel restart — the API says so with `retained`, and the UI
// says so in words rather than showing an empty pane.
type installLog struct {
	mu      sync.Mutex
	entries map[string]*installEntry
}

// maxInstallLines is the per-server tail kept in memory. SteamCMD prints one
// progress line per chunk, so a large install can emit thousands; the tail is
// what diagnoses a failure.
const maxInstallLines = 500

// installLine is one line of installer output. Stream names the source the way
// a console frame does: installStreamName for ordinary output, "error" for a
// Panel-side failure note, so the console surface colors it without needing to
// know an install is what it is reading.
type installLine struct {
	Ts     int64  `json:"ts"`
	Stream string `json:"stream"`
	Text   string `json:"text"`
}

type installEntry struct {
	lines []installLine
	// done is set when the install reached a verdict; a subscriber that has
	// drained the buffer of a done entry can stop rather than wait forever.
	done bool
	// startedAt/finishedAt bracket the attempt so a reader can say *which*
	// install it is looking at — "the last one" is not an answer when a server
	// has been reinstalled twice today.
	startedAt  time.Time
	finishedAt time.Time
	subs       map[chan installLine]struct{}
	// previous is the attempt this one replaced, kept read-only so a reinstall
	// does not erase the output of the pass it is retrying (#381). Only Start
	// writes it, and only ever one back: the entry it points at has its own
	// previous dropped, so the chain never grows past two tails.
	previous *installEntry
}

func newInstallLog() *installLog {
	return &installLog{entries: map[string]*installEntry{}}
}

// entry returns the server's entry, creating it if absent. Caller holds mu.
func (l *installLog) entry(id string) *installEntry {
	e := l.entries[id]
	if e == nil {
		e = &installEntry{subs: map[chan installLine]struct{}{}}
		l.entries[id] = e
	}
	return e
}

// Start opens a fresh buffer for an install attempt. The attempt it replaces is
// kept as the new entry's previous (with that one's own previous dropped — one
// back is the bound), so the output of a failed pass survives the reinstall
// that retries it. Live subscribers of the old entry are closed: what they
// were tailing is over.
//
// Handlers call Start synchronously, just before the store write that makes
// the row `installing` (#387): a client that sees `installing` and reads the
// log must get the new attempt, never the one before the button press. The
// returned undo is for that write failing: it puts the replaced attempt back
// as it was, so a request that changed nothing leaves the log as it found it.
// undo does nothing once the new attempt has a line or another Start has
// replaced it. Subscribers Start closed stay closed; a reader re-subscribes.
func (l *installLog) Start(id string) (undo func()) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := &installEntry{startedAt: time.Now(), subs: map[chan installLine]struct{}{}}
	old := l.entries[id]
	var oldDone bool
	var oldPrevious *installEntry
	if old != nil {
		oldDone, oldPrevious = old.done, old.previous
		for ch := range old.subs {
			close(ch)
		}
		old.subs = map[chan installLine]struct{}{}
		// Superseded is over, whether or not it reached a verdict first; its
		// finishedAt stays as it was, so a pass that was cut off still reads
		// as never having finished.
		old.done = true
		old.previous = nil
		e.previous = old
	}
	l.entries[id] = e
	return func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		if l.entries[id] != e || len(e.lines) > 0 || e.done {
			return
		}
		for ch := range e.subs {
			close(ch)
		}
		if old == nil {
			delete(l.entries, id)
			return
		}
		old.done, old.previous = oldDone, oldPrevious
		l.entries[id] = old
	}
}

// Append records a line of ordinary installer output.
func (l *installLog) Append(id, text string) {
	l.append(id, installStreamName, text)
}

// AppendError records a line that reports a failure, so it reads as one.
func (l *installLog) AppendError(id, text string) {
	l.append(id, "error", text)
}

// append records a line and fans it out to live subscribers. A subscriber whose
// buffer is full is dropped rather than allowed to stall the install: the
// installer's progress must never wait on a slow browser.
func (l *installLog) append(id, stream, text string) {
	line := installLine{Ts: time.Now().UnixMilli(), Stream: stream, Text: text}
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.entry(id)
	e.lines = append(e.lines, line)
	if len(e.lines) > maxInstallLines {
		e.lines = e.lines[len(e.lines)-maxInstallLines:]
	}
	for ch := range e.subs {
		select {
		case ch <- line:
		default:
			delete(e.subs, ch)
			close(ch)
		}
	}
}

// Finish marks the attempt complete (either verdict) and releases live
// subscribers. The buffer stays for later reading.
func (l *installLog) Finish(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.entry(id)
	e.done = true
	if e.finishedAt.IsZero() {
		e.finishedAt = time.Now()
	}
	for ch := range e.subs {
		close(ch)
	}
	e.subs = map[chan installLine]struct{}{}
}

// installSnapshot is a completed-or-running attempt's whole output, as read by
// something that is not tailing it — the REST endpoint. Retained is false when
// this Panel process holds no buffer for the server at all (it never ran the
// install, or restarted since), which is a different answer from "an install
// that printed nothing".
type installSnapshot struct {
	Lines      []installLine
	Done       bool
	Retained   bool
	StartedAt  time.Time
	FinishedAt time.Time
	// Previous is the attempt the current one replaced, or nil when there is
	// none. A previous attempt is over by definition; Snapshot never fills its
	// Previous or its Retained.
	Previous *installSnapshot
}

// Snapshot returns the buffered lines without subscribing to later ones.
func (l *installLog) Snapshot(id string) installSnapshot {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.entries[id]
	if e == nil {
		return installSnapshot{Done: true}
	}
	snap := e.snapshot()
	snap.Retained = true
	if e.previous != nil {
		prev := e.previous.snapshot()
		snap.Previous = &prev
	}
	return snap
}

// snapshot copies one entry's lines and bracket, without its previous and
// without Retained, which is a fact about the server's buffer as a whole and
// is set by Snapshot on the current attempt only. Caller holds mu.
func (e *installEntry) snapshot() installSnapshot {
	lines := make([]installLine, len(e.lines))
	copy(lines, e.lines)
	return installSnapshot{
		Lines: lines, Done: e.done,
		StartedAt: e.startedAt, FinishedAt: e.finishedAt,
	}
}

// Drop forgets a server's install output entirely (server retired or deleted)
// — the current attempt and the previous one with it.
func (l *installLog) Drop(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e := l.entries[id]; e != nil {
		for ch := range e.subs {
			close(ch)
		}
	}
	delete(l.entries, id)
}

// Tail returns the buffered lines plus a channel of subsequent ones. The
// snapshot and the subscription are taken under one lock, so no line can slip
// between them. done reports that the attempt already reached a verdict, in
// which case ch is closed and the snapshot is the whole story.
//
// A server with no buffer at all (never installed under this Panel process, or
// dropped) reads as done with nothing to show, rather than an open socket that
// will never produce a line.
//
// The returned cancel must be called to release the subscription.
func (l *installLog) Tail(id string) (snapshot []installLine, ch <-chan installLine, done bool, cancel func()) {
	l.mu.Lock()
	defer l.mu.Unlock()
	closedChan := func() <-chan installLine {
		c := make(chan installLine)
		close(c)
		return c
	}
	e := l.entries[id]
	if e == nil {
		return nil, closedChan(), true, func() {}
	}
	snapshot = make([]installLine, len(e.lines))
	copy(snapshot, e.lines)
	if e.done {
		return snapshot, closedChan(), true, func() {}
	}
	sub := make(chan installLine, 256)
	e.subs[sub] = struct{}{}
	return snapshot, sub, false, func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		cur := l.entries[id]
		if cur == nil {
			return
		}
		if _, ok := cur.subs[sub]; ok {
			delete(cur.subs, sub)
			close(sub)
		}
	}
}
