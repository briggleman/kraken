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
// survive the install so a failure stays readable, and are dropped when the
// server is deleted or a reinstall starts. Retention is therefore bounded by
// the server count, which the node's memory and port reservations already bound.
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
	subs map[chan installLine]struct{}
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

// Start opens a fresh buffer for an install attempt, discarding any previous
// attempt's output. Live subscribers are closed: what they were tailing is over.
func (l *installLog) Start(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if old := l.entries[id]; old != nil {
		for ch := range old.subs {
			close(ch)
		}
	}
	l.entries[id] = &installEntry{subs: map[chan installLine]struct{}{}}
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
	for ch := range e.subs {
		close(ch)
	}
	e.subs = map[chan installLine]struct{}{}
}

// Drop forgets a server's install output entirely (server deleted).
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
