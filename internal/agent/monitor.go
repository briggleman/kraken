package agent

import (
	"context"
	"io"
	"log/slog"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"

	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// defaultMaxRestarts caps consecutive crash-restarts when a spec enables
// auto-restart without specifying its own limit.
const defaultMaxRestarts = 3

// monitor is the per-server crash watchdog. One runs for the lifetime of a
// started server (across auto-restarts) and owns the authoritative lifecycle
// state the Agent reports via Status. It distinguishes an operator stop/kill
// (→ offline) from an unexpected exit (→ crashed, with optional auto-restart),
// and—when the spec sets a ready_regex—holds the server in STARTING until a
// matching console line flips it to RUNNING.
type monitor struct {
	d        *DockerRuntime
	serverID string

	readyRe        *regexp.Regexp // nil → ready as soon as the container runs
	restartOnCrash bool
	maxRestarts    int

	ctx    context.Context
	cancel context.CancelFunc

	mu           sync.Mutex
	state        agentpb.ServerState
	expectedDown bool // an operator stop/kill was requested; the next exit is intentional
	restarts     int
}

// startMonitor (re)arms the watchdog for a freshly started server. Any prior
// monitor is cancelled and replaced, which resets the crash-restart counter —
// so a manual start/restart always gets a clean budget.
func (d *DockerRuntime) startMonitor(serverID string) {
	spec, _ := d.getSpec(serverID)

	d.stopMonitor(serverID)

	ctx, cancel := context.WithCancel(context.Background())
	m := &monitor{
		d:        d,
		serverID: serverID,
		ctx:      ctx,
		cancel:   cancel,
		state:    agentpb.ServerState_SERVER_STATE_STARTING,
	}
	if spec != nil {
		m.restartOnCrash = spec.GetRestartOnCrash()
		m.maxRestarts = int(spec.GetMaxRestarts())
		if m.maxRestarts <= 0 {
			m.maxRestarts = defaultMaxRestarts
		}
		if rx := spec.GetReadyRegex(); rx != "" {
			if re, err := regexp.Compile(rx); err == nil {
				m.readyRe = re
			} else {
				slog.Warn("invalid ready_regex; treating server as ready when running", "server", serverID, "err", err)
			}
		}
	}

	d.monMu.Lock()
	d.monitors[serverID] = m
	d.monMu.Unlock()

	go m.run(time.Now())
}

// stopMonitor cancels and forgets a server's watchdog (e.g. on server removal).
func (d *DockerRuntime) stopMonitor(serverID string) {
	d.monMu.Lock()
	if m, ok := d.monitors[serverID]; ok {
		m.cancel()
		delete(d.monitors, serverID)
	}
	d.monMu.Unlock()
}

// markExpectedDown tells a server's watchdog that the next exit is an operator
// action (stop/kill/restart), not a crash.
func (d *DockerRuntime) markExpectedDown(serverID string) {
	d.monMu.Lock()
	m := d.monitors[serverID]
	d.monMu.Unlock()
	if m != nil {
		m.mu.Lock()
		m.expectedDown = true
		m.mu.Unlock()
	}
}

// setMonitorState overrides a server's reported state (e.g. to STOPPING while a
// graceful stop is in flight). No-op if there is no monitor.
func (d *DockerRuntime) setMonitorState(serverID string, st agentpb.ServerState) {
	d.monMu.Lock()
	m := d.monitors[serverID]
	d.monMu.Unlock()
	if m != nil {
		m.setState(st)
	}
}

// monitorState returns the watchdog's current state for a server, if one exists.
func (d *DockerRuntime) monitorState(serverID string) (agentpb.ServerState, bool) {
	d.monMu.Lock()
	m := d.monitors[serverID]
	d.monMu.Unlock()
	if m == nil {
		return agentpb.ServerState_SERVER_STATE_UNSPECIFIED, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state, true
}

func (m *monitor) setState(st agentpb.ServerState) {
	m.mu.Lock()
	m.state = st
	m.mu.Unlock()
}

// run is the watchdog loop: arm readiness detection, wait for the container to
// exit, then decide offline vs. crashed vs. auto-restart. `since` bounds the log
// scan so an earlier run's ready line can't be matched after a restart.
func (m *monitor) run(since time.Time) {
	for {
		if m.readyRe == nil {
			// No readiness probe: a running container is considered up.
			m.setState(agentpb.ServerState_SERVER_STATE_RUNNING)
		} else {
			m.setState(agentpb.ServerState_SERVER_STATE_STARTING)
			go m.scanReady(since)
		}

		code, err := m.d.waitExit(m.ctx, m.serverID)
		if m.ctx.Err() != nil {
			return // monitor cancelled (server removed or superseded by a new start)
		}
		if err != nil {
			// Couldn't observe the exit; leave the last state and stop watching.
			slog.Warn("watchdog: wait failed", "server", m.serverID, "err", err)
			return
		}

		m.mu.Lock()
		intentional := m.expectedDown
		m.mu.Unlock()
		if intentional {
			m.setState(agentpb.ServerState_SERVER_STATE_OFFLINE)
			return
		}

		// Unexpected exit → crash.
		m.mu.Lock()
		canRestart := m.restartOnCrash && m.restarts < m.maxRestarts
		if canRestart {
			m.restarts++
		}
		attempt, max := m.restarts, m.maxRestarts
		m.mu.Unlock()

		if !canRestart {
			slog.Warn("watchdog: server crashed", "server", m.serverID, "exit_code", code, "auto_restart", m.restartOnCrash)
			m.setState(agentpb.ServerState_SERVER_STATE_CRASHED)
			return
		}

		slog.Info("watchdog: server crashed — auto-restarting", "server", m.serverID, "exit_code", code, "attempt", attempt, "max", max)
		m.setState(agentpb.ServerState_SERVER_STATE_STARTING)
		since = time.Now()
		if err := m.d.ensureAndStart(m.ctx, m.serverID); err != nil {
			slog.Error("watchdog: auto-restart failed", "server", m.serverID, "err", err)
			m.setState(agentpb.ServerState_SERVER_STATE_CRASHED)
			return
		}
		// Loop: re-arm readiness and wait for the next exit.
	}
}

// scanReady tails the container's logs (only lines since the given time) and
// flips the state to RUNNING on the first line matching the ready regex.
func (m *monitor) scanReady(since time.Time) {
	reader, err := m.d.cli.ContainerLogs(m.ctx, containerName(m.serverID), container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     true,
		Since:      strconv.FormatInt(since.Unix(), 10),
	})
	if err != nil {
		return
	}
	defer reader.Close()

	_ = demux(reader, func(_ string, text string) error {
		if m.readyRe.MatchString(text) {
			m.mu.Lock()
			if m.state == agentpb.ServerState_SERVER_STATE_STARTING {
				m.state = agentpb.ServerState_SERVER_STATE_RUNNING
			}
			m.mu.Unlock()
			return io.EOF // stop scanning once ready
		}
		return nil
	})
}

// waitExit blocks until the server's container is no longer running and returns
// its exit code. It honors ctx cancellation (monitor shutdown).
func (d *DockerRuntime) waitExit(ctx context.Context, serverID string) (int64, error) {
	statusCh, errCh := d.cli.ContainerWait(ctx, containerName(serverID), container.WaitConditionNotRunning)
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case err := <-errCh:
		return 0, err
	case st := <-statusCh:
		return st.StatusCode, nil
	}
}
