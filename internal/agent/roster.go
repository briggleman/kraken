package agent

import (
	"context"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"

	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// logRoster is the "log" player-query method: the set of players a server's
// own console says are aboard. Games with no A2S and no REST endpoint (UE5
// early-access servers, typically) still print a line when someone arrives
// and another when they leave; two spec regexes name those lines and capture
// the player, and the roster is whatever has joined and not yet left.
//
// It is the only method that yields names, so it is also what the drill-in's
// roster pane renders. A count-only method never populates OnlinePlayers.
//
// One roster lives for the life of a server's watchdog; each container run
// gets a fresh follower (see monitor.run) that replays the run's log from its
// start, so an Agent restart recovers who is aboard rather than starting from
// nobody. Between runs, and while no follower is attached, the count reads
// unknown — the honest answer for a server whose console is not being read.
type logRoster struct {
	joinRe, leaveRe *regexp.Regexp

	mu      sync.Mutex
	players map[string]*agentpb.OnlinePlayer // key: id capture, else name
	alive   bool                             // a follower is attached to the current run
}

// newLogRoster compiles the spec's join/leave regexes. Validate already
// rejected a spec that would fail here; this guards a hand-pushed one.
func newLogRoster(q *agentpb.PlayerQuery) (*logRoster, error) {
	joinRe, err := regexp.Compile(q.GetJoinRegex())
	if err != nil {
		return nil, err
	}
	leaveRe, err := regexp.Compile(q.GetLeaveRegex())
	if err != nil {
		return nil, err
	}
	return &logRoster{joinRe: joinRe, leaveRe: leaveRe, players: map[string]*agentpb.OnlinePlayer{}}, nil
}

// capture reads the player out of a match: the key the roster stores under
// (the id capture, falling back to the name) and the display name (the name
// capture, falling back to the id). A regex with neither never validated.
func capture(re *regexp.Regexp, m []string) (key, name string) {
	get := func(group string) string {
		if i := re.SubexpIndex(group); i >= 0 && i < len(m) {
			return m[i]
		}
		return ""
	}
	id, nm := get("id"), get("name")
	key = id
	if key == "" {
		key = nm
	}
	name = nm
	if name == "" {
		name = id
	}
	return key, name
}

// observe feeds one console line through the two regexes. The leave regex is
// tried first: a line that a loose join pattern would also match is more
// likely a departure than an arrival, and a player who left must not linger.
func (r *logRoster) observe(line string, at time.Time) {
	if m := r.leaveRe.FindStringSubmatch(line); m != nil {
		key, _ := capture(r.leaveRe, m)
		if key != "" {
			r.mu.Lock()
			delete(r.players, key)
			r.mu.Unlock()
		}
		return
	}
	if m := r.joinRe.FindStringSubmatch(line); m != nil {
		key, name := capture(r.joinRe, m)
		if key == "" {
			return
		}
		r.mu.Lock()
		if p, seen := r.players[key]; seen {
			// A repeated join line (the game re-saves, or reconnects) keeps the
			// original arrival time; only the name may have changed.
			p.Name = name
		} else {
			r.players[key] = &agentpb.OnlinePlayer{Name: name, JoinedUnixMs: at.UnixMilli()}
		}
		r.mu.Unlock()
	}
}

// snapshot returns the roster in arrival order (ties by name, so the list is
// stable between ticks) and whether a follower is currently reading the log.
func (r *logRoster) snapshot() (players []*agentpb.OnlinePlayer, alive bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	players = make([]*agentpb.OnlinePlayer, 0, len(r.players))
	for _, p := range r.players {
		players = append(players, &agentpb.OnlinePlayer{Name: p.Name, JoinedUnixMs: p.JoinedUnixMs})
	}
	sort.Slice(players, func(i, j int) bool {
		if players[i].JoinedUnixMs != players[j].JoinedUnixMs {
			return players[i].JoinedUnixMs < players[j].JoinedUnixMs
		}
		return players[i].Name < players[j].Name
	})
	return players, r.alive
}

// reset empties the roster for a new container run and marks it live.
func (r *logRoster) reset() {
	r.mu.Lock()
	r.players = map[string]*agentpb.OnlinePlayer{}
	r.alive = true
	r.mu.Unlock()
}

// down marks the roster unread — the container exited or the follower lost
// the log — and empties it: nobody is aboard a server that is not running.
func (r *logRoster) down() {
	r.mu.Lock()
	r.players = map[string]*agentpb.OnlinePlayer{}
	r.alive = false
	r.mu.Unlock()
}

// followRoster tails the container's console from `since` (the current run's
// start, so an earlier run's joins are not replayed) and feeds every line to
// the roster until the container exits or the watchdog is cancelled. Log
// timestamps are used for the arrival time where Docker supplies them, so a
// replay after an Agent restart does not stamp everyone with "just now".
func (d *DockerRuntime) followRoster(ctx context.Context, serverID string, r *logRoster, since time.Time) {
	defer r.down()
	reader, err := d.cli.ContainerLogs(ctx, containerName(serverID), container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     true,
		Timestamps: true,
		Since:      strconv.FormatInt(since.Unix(), 10),
	})
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("roster: could not follow console", "server", serverID, "err", err)
		}
		return
	}
	defer reader.Close()
	_ = demux(reader, func(_ string, text string) error {
		at, line := splitLogTimestamp(text)
		r.observe(line, at)
		return nil
	})
}

// splitLogTimestamp separates Docker's leading RFC 3339 timestamp (present when
// LogsOptions.Timestamps is set) from the line. A line without one is stamped
// with the wall clock.
func splitLogTimestamp(text string) (time.Time, string) {
	if i := strings.IndexByte(text, ' '); i > 0 {
		if t, err := time.Parse(time.RFC3339Nano, text[:i]); err == nil {
			return t, text[i+1:]
		}
	}
	return time.Now(), text
}

// rosterFor returns the server's log roster, if its spec declares the "log"
// method and the watchdog has armed one.
func (d *DockerRuntime) rosterFor(serverID string) *logRoster {
	d.pcMu.Lock()
	defer d.pcMu.Unlock()
	return d.rosters[serverID]
}

// setRoster installs (or, with nil, forgets) a server's roster.
func (d *DockerRuntime) setRoster(serverID string, r *logRoster) {
	d.pcMu.Lock()
	defer d.pcMu.Unlock()
	if r == nil {
		delete(d.rosters, serverID)
		return
	}
	if d.rosters == nil {
		d.rosters = map[string]*logRoster{}
	}
	d.rosters[serverID] = r
}

// sampledRoster returns the names behind the player count, for the methods
// that have them. nil for count-only methods, and for a log roster nobody is
// currently reading.
func (d *DockerRuntime) sampledRoster(serverID string) []*agentpb.OnlinePlayer {
	r := d.rosterFor(serverID)
	if r == nil {
		return nil
	}
	players, alive := r.snapshot()
	if !alive {
		return nil
	}
	return players
}
