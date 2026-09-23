package agent

import (
	"context"
	"errors"
	"time"

	cerrdefs "github.com/containerd/errdefs"
)

// Recreating a server's container is the Agent's busiest path: every start of a
// stopped server and every crash auto-restart goes through ensureContainer,
// which removes the old container and creates a new one under the SAME name.
// Those two steps are not atomic.
//
// Docker's remove is asynchronous. The daemon accepts it, returns, and releases
// the name some time later. On Linux that window is short enough to be
// invisible. On Windows it is not — removal is slow, slower still under
// Hyper-V isolation — and a daemon that is restarting can fail the remove
// outright. Either way the create that follows lands on a name still held by
// the container that was just removed:
//
//	Conflict. The container name "/kraken_<server-id>" is already in use by
//	container "<hash>". You have to remove (or rename) that container to be
//	able to reuse that name.
//
// Before this guard the remove's error was discarded and nothing waited, so
// that conflict surfaced as `watchdog: auto-restart failed` and left the server
// CRASHED with no container at all — dead until an operator ran `docker rm` by
// hand. Observed live on abyss-win on 2026-09-23, on both servers on the node,
// after the Docker daemon cycled underneath them (issue #353).

// containerNameFreeAttempts and containerNameFreeDelay bound the wait for a
// removed container's name to come free: ~8s in total. Long enough for a slow
// Windows removal, short enough to stay well inside the Panel's 45s Power RPC
// budget — a name that is never coming back should fail the start with a clear
// reason rather than hold the operator's request until it times out.
const (
	containerNameFreeAttempts = 16
	containerNameFreeDelay    = 500 * time.Millisecond
)

// errNameStillTaken is the failure awaitNameFree reports when the name is still
// in use after every attempt. It reads as a stuck name rather than as the
// create conflict it would otherwise become two calls later.
var errNameStillTaken = errors.New("container name still in use after removal")

// awaitNameFree polls taken until it reports false, the context ends, or the
// attempts run out. It holds the timing policy with no Docker in it, so the
// part worth testing is testable without a daemon.
//
// taken is called before the first sleep: the common case is a removal that has
// already landed, and that costs one inspect and no delay.
func awaitNameFree(ctx context.Context, taken func(context.Context) (bool, error), attempts int, delay time.Duration) error {
	for i := 0; ; i++ {
		still, err := taken(ctx)
		if err != nil {
			return err
		}
		if !still {
			return nil
		}
		if i >= attempts-1 {
			return errNameStillTaken
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
}

// isNameConflict reports whether err is the daemon refusing to create a
// container because its name is already in use. Docker returns 409 for this,
// which the client maps to a conflict error.
func isNameConflict(err error) bool {
	return err != nil && cerrdefs.IsConflict(err)
}

// isNotFound reports whether err is the daemon saying the object is gone —
// which, for a removal or the inspect that follows one, is success.
func isNotFound(err error) bool {
	return err != nil && cerrdefs.IsNotFound(err)
}
