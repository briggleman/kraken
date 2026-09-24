package agent

import (
	"sync"

	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"

	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// The pre-install guard (clearDataDir) is a check, not a lock: it proves
// nothing holds the data dir at the moment it looks. Two things on the Agent
// could start the game on that dir while the pass then runs —
//
//   - the crash watchdog, part-way through an auto-restart (markExpectedDown
//     only covers the next exit; only stopMonitor cancels its context), and
//   - any PowerAction START or RESTART arriving mid-install: a scheduled
//     restart, the node-scoped power endpoint, an older Panel.
//
// installGate closes both. Install enters it (and stops the watchdog) before
// the guard runs, and every path that starts the game container asks it
// first.

// errInstallRunning is the refusal a start gets while an install pass runs.
// Aborted — gRPC's code for an operation that lost to a concurrent one, and
// that is worth retrying once the other has finished — not FailedPrecondition:
// the Panel reads FailedPrecondition as a file in use and appends "a game
// container may still be running", which is the wrong thing to tell an
// operator whose server is mid-install. The Panel answers Aborted with 409
// `install_running` (agentFailure). It is a gRPC status already, so the
// Agent's error interceptor passes it through untouched.
var errInstallRunning = grpcstatus.Error(codes.Aborted, "an install pass is running for this server")

// installGate is the set of servers with an install pass in progress. The zero
// value is ready to use.
type installGate struct {
	mu  sync.Mutex
	ids map[string]int // serverID → passes in progress
}

// enter marks serverID as installing and returns the func that clears it.
func (g *installGate) enter(serverID string) (leave func()) {
	g.mu.Lock()
	if g.ids == nil {
		g.ids = make(map[string]int)
	}
	g.ids[serverID]++
	g.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			if g.ids[serverID]--; g.ids[serverID] <= 0 {
				delete(g.ids, serverID)
			}
			g.mu.Unlock()
		})
	}
}

// releaseOnVerdict wraps an install's emit so leave runs just before the pass's
// verdict — a Completed or Failed event — is sent. Whoever acts on the verdict
// must find the gate open. leave must be safe to call twice (enter's is).
func releaseOnVerdict(emit func(*agentpb.InstallEvent) error, leave func()) func(*agentpb.InstallEvent) error {
	return func(ev *agentpb.InstallEvent) error {
		switch ev.GetEvent().(type) {
		case *agentpb.InstallEvent_Completed, *agentpb.InstallEvent_Failed:
			leave()
		}
		return emit(ev)
	}
}

// check returns errInstallRunning while serverID is installing.
func (g *installGate) check(serverID string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.ids[serverID] > 0 {
		return errInstallRunning
	}
	return nil
}
