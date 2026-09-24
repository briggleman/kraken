package agent

import (
	"sync"

	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
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
// FailedPrecondition, because retrying once the pass has finished is exactly
// right.
var errInstallRunning = grpcstatus.Error(codes.FailedPrecondition, "an install pass is running for this server")

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

// check returns errInstallRunning while serverID is installing.
func (g *installGate) check(serverID string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.ids[serverID] > 0 {
		return errInstallRunning
	}
	return nil
}
