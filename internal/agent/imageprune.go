package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/docker/docker/api/types/filters"
)

// Game-server base images are large — kraken-steam-win is multi-gigabyte — and
// every pull of a moving tag untags the previous image, which then sits on the
// node as a dangling <none>:<none> layer set forever. Nothing in Kraken ever
// reclaimed it, and #288 (pull on install and start) makes them accumulate
// faster. This is the collector, deliberately well away from the pull path.
//
// Scope is *dangling images only*. Docker itself refuses to remove an image
// that backs any container, running or stopped, so a live server and a merely
// stopped one are both safe without Kraken adding in-use checks of its own —
// the filter plus that guarantee is the whole safety story.
//
// There is no retention floor and no threshold, deliberately: a dangling image
// is lost a week after it was untagged, and rolling back to it means
// re-downloading it. Keeping the previous multi-GB image of every spec on every
// node indefinitely is the cost this exists to avoid.

const (
	// pruneInterval is how stale the last prune must be before another runs.
	pruneInterval = 7 * 24 * time.Hour
	// pruneCheckInterval is how often the persisted clock is consulted. The
	// clock is persisted rather than ticked from process start because agents
	// restart on every self-update, so a weekly in-process ticker would rarely
	// live long enough to fire.
	pruneCheckInterval = time.Hour
	// pruneStartDelay keeps the first-ever prune (and the one due after a long
	// downtime) off the startup path, where the Agent is enrolling, adopting
	// watchdogs, and answering the Panel's first polls.
	pruneStartDelay = 5 * time.Minute
	// pruneTimeout bounds one prune call, so a wedged daemon cannot leave the
	// loop blocked forever.
	pruneTimeout = 10 * time.Minute
)

// imagePruneStateName is the clock file under the state dir. Node-local like
// the backup-failure log (#221): it is state about this node's disk, and it has
// to survive the agent restarts that self-update causes.
const imagePruneStateName = "image-prune.json"

type pruneState struct {
	LastPruneUnixMs int64 `json:"last_prune_unix_ms"`
}

// pruneClock persists when this node last pruned.
type pruneClock struct {
	mu   sync.Mutex
	path string
}

func newPruneClock(stateDir string) *pruneClock {
	return &pruneClock{path: filepath.Join(stateDir, imagePruneStateName)}
}

// last returns the recorded time, and whether there is one. A missing, corrupt,
// or unreadable file reads as "never pruned" — the consequence is one extra
// prune, which is cheap and idempotent, whereas failing closed would silently
// disable the collector on the nodes that need it.
func (c *pruneClock) last() (time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	data, err := os.ReadFile(c.path)
	if err != nil {
		return time.Time{}, false
	}
	var s pruneState
	if err := json.Unmarshal(data, &s); err != nil || s.LastPruneUnixMs <= 0 {
		return time.Time{}, false
	}
	return time.UnixMilli(s.LastPruneUnixMs), true
}

// stamp records a completed prune, atomically (tmp + rename) so a crash
// mid-write cannot leave a torn file that last() would then discard.
func (c *pruneClock) stamp(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	data, err := json.Marshal(pruneState{LastPruneUnixMs: t.UnixMilli()})
	if err != nil {
		return
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		slog.Warn("could not record the image-prune timestamp", "path", c.path, "err", err)
		return
	}
	if err := os.Rename(tmp, c.path); err != nil {
		_ = os.Remove(tmp)
		slog.Warn("could not record the image-prune timestamp", "path", c.path, "err", err)
	}
}

// StartImagePruner runs the weekly dangling-image prune in the background until
// ctx is cancelled. Called once, from the Agent's startup, alongside the other
// long-lived loops.
func (d *DockerRuntime) StartImagePruner(ctx context.Context) {
	if !d.imagePrune {
		slog.Info("dangling-image prune is off (KRAKEN_IMAGE_PRUNE=off) — untagged images from previous pulls will accumulate on this node")
		return
	}
	go d.pruneLoop(ctx)
}

func (d *DockerRuntime) pruneLoop(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(pruneStartDelay):
	}
	d.pruneIfDue(ctx)

	t := time.NewTicker(pruneCheckInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			d.pruneIfDue(ctx)
		}
	}
}

// pruneIfDue prunes when the persisted clock says a week has passed (or when
// there is no clock yet).
func (d *DockerRuntime) pruneIfDue(ctx context.Context) {
	if !d.imagePrune {
		return
	}
	// A prune against an unreachable daemon can only fail, and hourly warnings
	// about it would drown the log of a node that is already visibly degraded.
	if ok, _ := d.RuntimeHealth(); !ok {
		return
	}
	if last, ok := d.pruneClock.last(); ok && time.Since(last) < pruneInterval {
		return
	}
	d.pruneDanglingImages(ctx)
}

// pruneDanglingImages asks the daemon to reclaim untagged images. The API call
// is OS-neutral, so this is the same on a Windows-container node as on a Linux
// one.
func (d *DockerRuntime) pruneDanglingImages(ctx context.Context) {
	pctx, cancel := context.WithTimeout(ctx, pruneTimeout)
	defer cancel()

	// dangling=true is the narrow filter: only images no tag points at any more.
	// Never a bare ImageRemove loop — that would have to reimplement the
	// in-use checks the daemon already enforces.
	report, err := d.images.ImagesPrune(pctx, filters.NewArgs(filters.Arg("dangling", "true")))
	if err != nil {
		// The clock is left alone on failure, so the next hourly check retries
		// rather than waiting out another week.
		slog.Warn("dangling-image prune failed", "err", err)
		return
	}
	d.pruneClock.stamp(time.Now())
	if len(report.ImagesDeleted) == 0 {
		slog.Info("dangling-image prune: nothing to reclaim")
		return
	}
	slog.Info("dangling-image prune complete",
		"images_deleted", len(report.ImagesDeleted), "bytes_reclaimed", report.SpaceReclaimed)
}
