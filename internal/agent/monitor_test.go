package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"

	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// The watchdog's crash line is what a journal-reading operator has, and a
// Windows NTSTATUS is unrecognisable in decimal: 3221225781 means nothing,
// 0xC0000135 is STATUS_DLL_NOT_FOUND. The uint32 conversion is the load-bearing
// part — a signed render would print -1073741515 and match no documentation
// anywhere.
func TestHexExit(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int64
		want string
	}{
		{"missing dll", 3221225781, "0xC0000135"},
		{"entry point not found", 3221225785, "0xC0000139"},
		{"access violation", 3221225477, "0xC0000005"},
		{"bad image format", 3221225595, "0xC000007B"},
		{"clean exit", 0, "0x00000000"},
		{"linux sigkill", 137, "0x00000089"},
	} {
		if got := hexExit(tc.code); got != tc.want {
			t.Errorf("%s: hexExit(%d) = %s, want %s", tc.name, tc.code, got, tc.want)
		}
	}
}

// watchdogContainers is a daemon with one game container that runs until the
// test crashes it. Inspect always finds it running, so the watchdog's restart
// goes straight to ContainerStart without recreating anything; each wait blocks
// until the test sends an exit code down exits.
type watchdogContainers struct {
	// containerOps is nil: the watchdog calls only the methods below, and
	// anything else panicking is the test telling you so.
	containerOps

	exits    chan int64    // one exit code per crash, received by the current wait
	started  chan struct{} // one per ContainerStart, in order
	startErr error         // what every ContainerStart fails with, when set
	gone     atomic.Bool   // the container is not there at all
}

func newWatchdogContainers() *watchdogContainers {
	return &watchdogContainers{exits: make(chan int64), started: make(chan struct{}, 16)}
}

func (f *watchdogContainers) ContainerInspect(_ context.Context, ref string) (container.InspectResponse, error) {
	if f.gone.Load() {
		return container.InspectResponse{}, errors.New("no such container: " + ref)
	}
	return container.InspectResponse{ContainerJSONBase: &container.ContainerJSONBase{
		ID: "id-" + ref, Name: "/" + ref, State: &container.State{Running: true, Status: "running"},
	}}, nil
}

func (f *watchdogContainers) ContainerWait(ctx context.Context, _ string, _ container.WaitCondition) (<-chan container.WaitResponse, <-chan error) {
	statusCh, errCh := make(chan container.WaitResponse, 1), make(chan error, 1)
	go func() {
		select {
		case code := <-f.exits:
			statusCh <- container.WaitResponse{StatusCode: code}
		case <-ctx.Done():
			errCh <- ctx.Err()
		}
	}()
	return statusCh, errCh
}

func (f *watchdogContainers) ContainerStart(context.Context, string, container.StartOptions) error {
	f.started <- struct{}{}
	return f.startErr
}

// crash hands the running container an exit code, failing the test if no
// watchdog is waiting for one.
func (f *watchdogContainers) crash(t *testing.T, code int64) {
	t.Helper()
	select {
	case f.exits <- code:
	case <-time.After(5 * time.Second):
		t.Fatal("no watchdog was waiting on the container")
	}
}

// awaitStart waits for the watchdog's auto-restart to reach the daemon.
func (f *watchdogContainers) awaitStart(t *testing.T) {
	t.Helper()
	select {
	case <-f.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the watchdog never restarted the container")
	}
}

// newWatchdogRuntime is a DockerRuntime holding one server's spec over a fake
// daemon, ready for startMonitor — the same arming Power(START|RESTART) does.
func newWatchdogRuntime(t *testing.T, f *watchdogContainers, spec *agentpb.ServerSpec) *DockerRuntime {
	t.Helper()
	d := &DockerRuntime{
		containers: f,
		specDir:    t.TempDir(),
		osType:     "linux",
		specs:      map[string]*agentpb.ServerSpec{},
		monitors:   map[string]*monitor{},
	}
	d.putSpec(spec)
	t.Cleanup(func() { d.stopMonitor(spec.ServerId) })
	return d
}

// awaitState polls the watchdog until it reports want; the run loop settles a
// crash on its own goroutine.
func awaitState(t *testing.T, d *DockerRuntime, serverID string, want agentpb.ServerState) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		st, _ := d.monitorState(serverID)
		if st == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("watchdog state = %v, want %v", st, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// awaitRestarts polls until the watchdog has begun n restarts and returns when
// the latest began.
func awaitRestarts(t *testing.T, d *DockerRuntime, serverID string, n int) time.Time {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, last := d.monitorRestarts(serverID)
		if got == n {
			return last
		}
		if time.Now().After(deadline) {
			t.Fatalf("watchdog restarts = %d, want %d", got, n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func statusOf(t *testing.T, d *DockerRuntime, serverID string) *agentpb.ServerStatus {
	t.Helper()
	st, err := d.Status(context.Background(), serverID)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	return st
}

// The watchdog's restarts are the only trace of a crash it healed: they happen
// inside the Agent, and one can fall between two of the Panel's status polls.
// So Status carries the count and when the latest began (#348), the Panel diffs
// it, and the count has to behave the way that diff assumes — up by one per
// restart, standing when the watchdog gives up, back to 0 only when an operator
// start arms a fresh watchdog.
func TestWatchdogRestartCount(t *testing.T) {
	const serverID = "watch-1"
	f := newWatchdogContainers()
	d := newWatchdogRuntime(t, f, &agentpb.ServerSpec{ServerId: serverID, RestartOnCrash: true, MaxRestarts: 2})

	d.startMonitor(serverID)
	if st := statusOf(t, d, serverID); st.State != agentpb.ServerState_SERVER_STATE_RUNNING ||
		st.WatchdogRestarts != 0 || st.LastWatchdogRestartUnixMs != 0 {
		t.Fatalf("freshly started: %+v", st)
	}

	// First crash: healed.
	before := time.Now()
	f.crash(t, 1)
	f.awaitStart(t)
	first := awaitRestarts(t, d, serverID, 1)
	if first.Before(before) || first.After(time.Now()) {
		t.Errorf("first restart stamped %v, outside the crash window starting %v", first, before)
	}
	awaitState(t, d, serverID, agentpb.ServerState_SERVER_STATE_RUNNING)
	if st := statusOf(t, d, serverID); st.WatchdogRestarts != 1 || st.LastWatchdogRestartUnixMs != first.UnixMilli() {
		t.Errorf("after one healed crash: %+v, want 1 restart at %d", st, first.UnixMilli())
	}

	// Second crash: healed again, and the stamp moves to it.
	time.Sleep(2 * time.Millisecond) // a distinct millisecond for the second stamp
	f.crash(t, 1)
	f.awaitStart(t)
	second := awaitRestarts(t, d, serverID, 2)
	if !second.After(first) {
		t.Errorf("second restart stamped %v, not after the first at %v", second, first)
	}
	if st := statusOf(t, d, serverID); st.WatchdogRestarts != 2 || st.LastWatchdogRestartUnixMs != second.UnixMilli() {
		t.Errorf("after two healed crashes: %+v", st)
	}

	// Third crash: the budget of two is spent, so the watchdog gives up. The
	// count keeps the two restarts it made — dropping to 0 here would read to
	// the Panel as a reset and lose the loop that just ended in a crash.
	f.crash(t, 3221225781)
	awaitState(t, d, serverID, agentpb.ServerState_SERVER_STATE_CRASHED)
	select {
	case <-f.started:
		t.Fatal("the watchdog restarted past its budget")
	default:
	}
	st := statusOf(t, d, serverID)
	if st.State != agentpb.ServerState_SERVER_STATE_CRASHED || st.LastExitCode != 3221225781 || !st.ExitCodeKnown {
		t.Errorf("gave up: %+v", st)
	}
	if st.WatchdogRestarts != 2 || st.LastWatchdogRestartUnixMs != second.UnixMilli() {
		t.Errorf("a watchdog that gave up must keep its count: %+v", st)
	}

	// An operator start arms a fresh watchdog: a clean budget and a clean count.
	d.startMonitor(serverID)
	if st := statusOf(t, d, serverID); st.WatchdogRestarts != 0 || st.LastWatchdogRestartUnixMs != 0 {
		t.Errorf("an operator start must reset the count: %+v", st)
	}
	f.crash(t, 1)
	f.awaitStart(t)
	awaitRestarts(t, d, serverID, 1)

	// Removal stops the watchdog, and no watchdog reads as no restarts.
	d.stopMonitor(serverID)
	f.gone.Store(true)
	if st := statusOf(t, d, serverID); st.WatchdogRestarts != 0 || st.LastWatchdogRestartUnixMs != 0 {
		t.Errorf("a removed server has no watchdog and no restarts: %+v", st)
	}
}

// A restart that fails to start the container has still been attempted: it was
// counted as it began, and the count must not run back down when the server
// lands crashed — a drop would read to the Panel as an operator reset.
func TestWatchdogRestartCountKeepsAFailedRestart(t *testing.T) {
	const serverID = "watch-2"
	f := newWatchdogContainers()
	f.startErr = errors.New("daemon refused the start")
	d := newWatchdogRuntime(t, f, &agentpb.ServerSpec{ServerId: serverID, RestartOnCrash: true, MaxRestarts: 3})

	d.startMonitor(serverID)
	f.crash(t, 1)
	f.awaitStart(t)
	awaitState(t, d, serverID, agentpb.ServerState_SERVER_STATE_CRASHED)
	if st := statusOf(t, d, serverID); st.WatchdogRestarts != 1 || st.LastWatchdogRestartUnixMs == 0 {
		t.Errorf("a failed restart still counts: %+v", st)
	}
}

// A spec without restart_on_crash has a watchdog that never restarts anything,
// so a crash reports no restarts — only the crash.
func TestWatchdogRestartCountWithoutAutoRestart(t *testing.T) {
	const serverID = "watch-3"
	f := newWatchdogContainers()
	d := newWatchdogRuntime(t, f, &agentpb.ServerSpec{ServerId: serverID})

	d.startMonitor(serverID)
	f.crash(t, 1)
	awaitState(t, d, serverID, agentpb.ServerState_SERVER_STATE_CRASHED)
	if st := statusOf(t, d, serverID); st.WatchdogRestarts != 0 || st.LastWatchdogRestartUnixMs != 0 {
		t.Errorf("no auto-restart, no restarts: %+v", st)
	}
}

// Without a watchdog — a server that was never started — Status falls back to
// the container, and there are no restarts to report.
func TestWatchdogRestartCountWithoutWatchdog(t *testing.T) {
	const serverID = "watch-4"
	f := newWatchdogContainers()
	d := newWatchdogRuntime(t, f, &agentpb.ServerSpec{ServerId: serverID, RestartOnCrash: true})

	if n, last := d.monitorRestarts(serverID); n != 0 || !last.IsZero() {
		t.Errorf("no watchdog: %d restarts, last %v", n, last)
	}
	f.gone.Store(true)
	if st := statusOf(t, d, serverID); st.WatchdogRestarts != 0 || st.LastWatchdogRestartUnixMs != 0 {
		t.Errorf("no watchdog, no restarts: %+v", st)
	}
}
