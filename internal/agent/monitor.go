package agent

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"

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
	// exitCode is the status of the most recent observed container exit, and
	// exitKnown says one has been observed at all (0 is a real exit code). The
	// watchdog is the only thing that sees it: without carrying it out of here
	// the code lived in the agent's journal and nowhere an operator looks, so a
	// Windows game dying on 0xC0000135 read as a bare "crashed" (#280).
	exitCode  int64
	exitKnown bool
}

// startMonitor (re)arms the watchdog for a freshly started server. Any prior
// monitor is cancelled and replaced, which resets the crash-restart counter —
// so a manual start/restart always gets a clean budget.
func (d *DockerRuntime) startMonitor(serverID string) {
	d.armMonitor(serverID, time.Now(), true)
}

// armMonitor installs a watchdog for a server. `since` bounds the readiness log
// scan so an earlier run's ready line can't be matched. probeReady=false skips
// readiness detection entirely and treats the server as up — used when adopting a
// server that has been running long enough that its ready line may have rotated
// out of the log (see adoptRunning).
func (d *DockerRuntime) armMonitor(serverID string, since time.Time, probeReady bool) {
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
		if rx := spec.GetReadyRegex(); rx != "" && probeReady {
			if re, err := regexp.Compile(rx); err == nil {
				m.readyRe = re
			} else {
				slog.Warn("invalid ready_regex; treating server as ready when running", "server", serverID, "err", err)
			}
		}
	}

	// With no readiness probe the server is up as soon as its container is, so
	// settle on that here rather than leaving a STARTING window until the run
	// goroutine is scheduled. The Panel polls Status on its own clock, and an
	// adopted server that has been serving for hours must not blip back to
	// "starting" just because the Agent restarted.
	if m.readyRe == nil {
		m.state = agentpb.ServerState_SERVER_STATE_RUNNING
	}

	d.monMu.Lock()
	d.monitors[serverID] = m
	d.monMu.Unlock()

	go m.run(since)
}

// adoptReadyGrace bounds how far back adoption will scan for a ready line. A
// container that has been up longer than this is adopted as ready outright: its
// ready line may have rotated out of the log, and reporting a server that has
// been serving players for an hour as "starting" is worse than skipping the probe.
const adoptReadyGrace = 10 * time.Minute

// adoptRunning re-arms watchdogs for the servers that are already running.
//
// Monitors are otherwise only ever armed by Power(START|RESTART), so without this
// every Agent restart silently dropped restart_on_crash and ready-regex detection
// for servers that never stopped. It was invisible: Status falls back to
// inspecting the container, so the Panel kept reporting the right state while the
// crash watchdog was simply gone. Called at startup and whenever the Docker
// daemon comes back, which from the Agent's point of view is the same event.
//
// Idempotent: a server that already has a monitor is left alone, so a repeated
// call can't reset a live crash-restart budget.
func (d *DockerRuntime) adoptRunning(ctx context.Context) {
	list, err := d.cli.ContainerList(ctx, container.ListOptions{
		Filters: filters.NewArgs(filters.Arg("label", labelManaged+"=true")),
	})
	if err != nil {
		slog.Warn("watchdog: could not list managed containers to adopt", "err", err)
		return
	}
	for _, c := range list {
		serverID := c.Labels[labelServerID]
		if serverID == "" {
			continue
		}
		if _, watched := d.monitorState(serverID); watched {
			continue
		}
		spec, ok := d.getSpec(serverID)
		if !ok {
			slog.Warn("watchdog: adopting a running server with no known spec — crash auto-restart stays off until the Panel pushes it again",
				"server", serverID)
		}
		started := startedAt(ctx, d.cli, c.ID)
		probeReady := spec.GetReadyRegex() != "" && time.Since(started) < adoptReadyGrace
		d.armMonitor(serverID, started, probeReady)
		slog.Info("watchdog: adopted running server", "server", serverID,
			"restart_on_crash", spec.GetRestartOnCrash(), "ready_probe", probeReady)
	}
}

// startedAt returns when a container was last started, falling back to now when
// the daemon won't say (which only costs the readiness scan its history).
func startedAt(ctx context.Context, cli *client.Client, containerID string) time.Time {
	insp, err := cli.ContainerInspect(ctx, containerID)
	if err != nil || insp.State == nil || insp.State.StartedAt == "" {
		return time.Now()
	}
	t, perr := time.Parse(time.RFC3339Nano, insp.State.StartedAt)
	if perr != nil {
		return time.Now()
	}
	return t
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

// monitorExit returns the exit code of the last container exit the watchdog
// observed. known is false when it has seen none (a server that has not stopped
// since the monitor was armed), which is not the same as an exit code of 0.
func (d *DockerRuntime) monitorExit(serverID string) (code int64, known bool) {
	d.monMu.Lock()
	m := d.monitors[serverID]
	d.monMu.Unlock()
	if m == nil {
		return 0, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.exitCode, m.exitKnown
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

		// Record the code before deciding what the exit means: an operator stop
		// has one too, and a Status poll that arrives between here and the next
		// start should report what actually happened.
		m.mu.Lock()
		m.exitCode, m.exitKnown = code, true
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
			// Hex alongside the decimal: a Windows NTSTATUS (0xC0000135 =
			// STATUS_DLL_NOT_FOUND) is unrecognisable in decimal, and this line
			// is what a journal-reading operator has.
			slog.Warn("watchdog: server crashed", "server", m.serverID,
				"exit_code", code, "exit_hex", hexExit(code), "auto_restart", m.restartOnCrash)
			m.setState(agentpb.ServerState_SERVER_STATE_CRASHED)
			return
		}

		slog.Info("watchdog: server crashed — auto-restarting", "server", m.serverID,
			"exit_code", code, "exit_hex", hexExit(code), "attempt", attempt, "max", max)
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

// hexExit renders an exit code the way Windows names it. A game killed by a
// missing DLL exits 3221225781, which is meaningless until it reads 0xC0000135;
// Linux codes (0-255, or 128+signal) are small and read fine either way.
func hexExit(code int64) string {
	return fmt.Sprintf("0x%08X", uint32(code))
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
