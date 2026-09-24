package agent

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
)

// imageAPI is the slice of the Docker client the image paths use. It exists as a
// seam so the pull-then-fall-back policy below can be unit-tested without a
// daemon (#288); in production the field holds *client.Client itself.
type imageAPI interface {
	ImageInspect(ctx context.Context, ref string, opts ...client.ImageInspectOption) (image.InspectResponse, error)
	ImagePull(ctx context.Context, ref string, options image.PullOptions) (io.ReadCloser, error)
	ImagesPrune(ctx context.Context, pruneFilter filters.Args) (image.PruneReport, error)
}

// imagePullPolicy decides how hard the Agent tries the registry before settling
// for whatever copy of an image is already on the node. The names follow the
// model Kubernetes converged on for this exact problem, so an operator who has
// met one has met the other.
type imagePullPolicy string

const (
	// pullAlways contacts the registry first on every install and every
	// operator-driven start, falling back to a local copy when that fails. The
	// default: an unchanged moving tag is a manifest check of a few KB, and the
	// fallback means an offline node loses nothing.
	pullAlways imagePullPolicy = "always"
	// pullIfNotPresent is the pre-#288 behaviour — a locally-present image is
	// used as-is and the registry is only consulted when there is nothing on
	// disk. For a node on metered or very slow transit.
	pullIfNotPresent imagePullPolicy = "if-not-present"
	// pullNever never contacts the registry at all. Every image a spec names
	// must already exist on the node (built by hand, or side-loaded), and an
	// install that names a missing one fails immediately instead of hanging on
	// a registry the operator has deliberately cut off.
	pullNever imagePullPolicy = "never"
)

// parsePullPolicy maps the configured string onto a policy. Anything
// unrecognized is the default — config.Load already rejects bad values, so this
// only guards a runtime constructed straight from the environment.
func parsePullPolicy(s string) imagePullPolicy {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case string(pullIfNotPresent):
		return pullIfNotPresent
	case string(pullNever):
		return pullNever
	default:
		return pullAlways
	}
}

// installPullTimeout bounds any actual transfer, so a registry that accepts the
// connection and then stalls cannot wedge a pull forever. It is generous because
// a cold node legitimately downloads a multi-gigabyte base image
// (kraken-steam-win is several GB) over whatever link the operator has: on a
// first install there is no local copy to fall back to, so cutting this short
// would turn a slow link into a hard failure. The same ceiling applies to the
// background pulls the start path detaches (see refreshImageForStart) — those
// have all the time they need precisely because nothing is waiting on them.
const installPullTimeout = 30 * time.Minute

// startPullBudget is how long an operator-driven start is willing to *wait* for
// a refresh before proceeding without it.
//
// The Panel bounds the Power RPC — START 15s, RESTART 60s
// (internal/panel/api/handlers_server.go) — so a pull that blocks the RPC past
// that makes the Panel report "agent error: context deadline exceeded" while the
// Agent is still working, and the operator sees a failed start that then
// mysteriously succeeds. Eight seconds clears the tightest of those deadlines
// with room for the container recreate and ContainerStart that follow.
//
// The common case, an unchanged moving tag, is a manifest check of a few KB and
// finishes well inside this, so a start still comes up on the refreshed image. A
// tag that genuinely moved overruns it and is downloaded in the background
// instead — see refreshImageForStart.
//
// A var, not a const, only so tests can shorten it.
var startPullBudget = 8 * time.Second

// pullImage makes ref available locally, preferring the registry.
//
// The order is deliberately pull-first (#288). The original code short-circuited
// on any locally-present image and never contacted the registry, so an image fix
// reached a node only if a human ran `docker pull` there — silently, which
// nearly invalidated the #281 retest. Every image reference in every bundled
// spec is a moving tag, so "present locally" says nothing about "current".
//
// A failed pull is not fatal while a local copy exists: Kraken nodes are
// self-hosted and may be offline, behind a flaky link, or running an image built
// by hand that lives in no registry at all. Those keep working exactly as
// before — they just say so in the log now.
func (d *DockerRuntime) pullImage(ctx context.Context, ref string, timeout time.Duration, log func(string)) error {
	local, localErr := d.images.ImageInspect(ctx, ref)
	hasLocal := localErr == nil

	if reason, skip := d.skipPull(ref, hasLocal); skip {
		if !hasLocal {
			return fmt.Errorf("image %s is not present on this node and the image pull policy is %q", ref, d.pullPolicy)
		}
		log(fmt.Sprintf("Using local image %s (%s) — %s", ref, imageIdentity(local), reason))
		return nil
	}

	log("Pulling image " + ref)
	perr := d.doPull(ctx, ref, timeout)
	if perr != nil {
		// A cancelled parent context is the operator aborting, not a registry
		// problem: reporting it as a fallback would hide the real reason.
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !hasLocal {
			return fmt.Errorf("pull %s: %w", ref, perr)
		}
		log(fmt.Sprintf("Pull of %s failed (%v); using local image %s", ref, perr, imageIdentity(local)))
		return nil
	}
	// Re-inspect so the log answers "which image is this node actually on?"
	// without an SSH session — the install log survives completion now (#284).
	if pulled, err := d.images.ImageInspect(ctx, ref); err == nil {
		log(fmt.Sprintf("Image ready: %s (%s)", ref, imageIdentity(pulled)))
	} else {
		log("Image ready: " + ref)
	}
	return nil
}

// inflightPull is one background pull, shared by everyone waiting on the same
// reference. err is written before done is closed, so a reader that has taken
// the channel may read it.
type inflightPull struct {
	done chan struct{}
	err  error
}

// refreshImageForStart gives an operator-driven start the current image when
// that is cheap, and never lets it block on the registry when it is not.
//
// It waits startPullBudget for the pull and then stops waiting — but does NOT
// cancel it. The pull runs on its own context to completion in the background,
// and the server starts now on the copy already on the node. ensureContainer
// removes and recreates a non-running container from whatever is local, so the
// freshly-downloaded image takes effect on the next start with no further code.
//
// The crash watchdog never comes through here (see imageRefresh): a crash loop
// must recover on the image it was already running.
func (d *DockerRuntime) refreshImageForStart(ctx context.Context, ref, serverID string) error {
	local, localErr := d.images.ImageInspect(ctx, ref)
	hasLocal := localErr == nil

	if reason, skip := d.skipPull(ref, hasLocal); skip {
		if !hasLocal {
			return fmt.Errorf("image %s is not present on this node and the image pull policy is %q", ref, d.pullPolicy)
		}
		slog.Info("image refresh on start skipped", "server", serverID, "image", ref,
			"local", imageIdentity(local), "reason", reason)
		return nil
	}

	p := d.backgroundPull(ref)
	select {
	case <-p.done:
		if p.err != nil {
			if !hasLocal {
				return fmt.Errorf("pull %s: %w", ref, p.err)
			}
			slog.Warn("image refresh on start failed; starting on the local image",
				"server", serverID, "image", ref, "local", imageIdentity(local), "err", p.err)
			return nil
		}
		return nil
	case <-time.After(startPullBudget):
		if !hasLocal {
			// Nothing to start from, so failing fast beats hanging the RPC: the
			// operator gets one clear sentence and a start that will work.
			return fmt.Errorf("image %s is not on this node yet; the pull is running in the background — start again once it completes", ref)
		}
		slog.Info("newer image for "+ref+" still downloading in the background; it takes effect on the next start",
			"server", serverID, "image", ref, "local", imageIdentity(local), "waited", startPullBudget)
		return nil
	case <-ctx.Done():
		// The RPC is over — the Panel's deadline passed, or the caller went
		// away. Stop waiting now rather than holding the start past a deadline
		// nobody is listening to; the pull itself carries on in the background.
		return fmt.Errorf("waiting for image %s: %w", ref, ctx.Err())
	}
}

// backgroundPull returns the in-flight pull for ref, starting one if there is
// none. De-duplicating by reference matters because a second START while a
// multi-gigabyte transfer is running must join that pull rather than launch a
// competing one — and because several servers commonly share one base image.
func (d *DockerRuntime) backgroundPull(ref string) *inflightPull {
	d.pullMu.Lock()
	if p, ok := d.pulls[ref]; ok {
		d.pullMu.Unlock()
		return p
	}
	p := &inflightPull{done: make(chan struct{})}
	if d.pulls == nil {
		d.pulls = map[string]*inflightPull{}
	}
	d.pulls[ref] = p
	d.pullMu.Unlock()

	go func() {
		// context.Background(), not the caller's: the Power RPC that started this
		// is bounded by the Panel and will usually be gone long before a real
		// layer transfer ends. Cancelling a multi-GB download at the 15-second
		// mark, every time, would mean the node never converges on the new image.
		err := d.doPull(context.Background(), ref, installPullTimeout)

		d.pullMu.Lock()
		delete(d.pulls, ref)
		d.pullMu.Unlock()

		p.err = err
		close(p.done)

		if err != nil {
			slog.Warn("image pull failed", "image", ref, "err", err)
			return
		}
		id := "unknown"
		if info, ierr := d.images.ImageInspect(context.Background(), ref); ierr == nil {
			id = imageIdentity(info)
		}
		slog.Info("image pull complete", "image", ref, "id", id)
	}()
	return p
}

// skipPull reports whether the registry should be left alone for this reference,
// and why — the reason is logged, so an operator can tell a policy decision from
// a failed pull.
func (d *DockerRuntime) skipPull(ref string, hasLocal bool) (string, bool) {
	switch {
	case d.pullPolicy == pullNever:
		return `image pull policy is "never"`, true
	case d.pullPolicy == pullIfNotPresent && hasLocal:
		return `image pull policy is "if-not-present"`, true
	case hasLocal && isDigestRef(ref):
		// A digest reference is immutable by definition, so a local hit is
		// authoritative and re-pulling could only fetch identical bytes.
		return "digest-pinned reference is immutable", true
	}
	return "", false
}

// doPull runs the pull under its own deadline and drains the progress stream —
// the pull is only complete once the stream ends, so discarding it is how we
// wait, not a shortcut.
func (d *DockerRuntime) doPull(ctx context.Context, ref string, timeout time.Duration) error {
	pctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	rc, err := d.images.ImagePull(pctx, ref, image.PullOptions{})
	if err != nil {
		return err
	}
	defer rc.Close()
	_, err = io.Copy(io.Discard, rc)
	return err
}

// isDigestRef reports whether ref pins an image by digest (name@sha256:…).
func isDigestRef(ref string) bool { return strings.Contains(ref, "@") }

// imageIdentity renders a short, operator-readable identity for an image. The
// registry's own digest for the tag comes first, since that is what "is this
// node on the current image?" is really asking; the local image ID is the
// fallback for an image that was never pulled from anywhere.
func imageIdentity(info image.InspectResponse) string {
	for _, rd := range info.RepoDigests {
		if _, digest, ok := strings.Cut(rd, "@"); ok {
			return shortDigest(digest)
		}
	}
	if info.ID == "" {
		return "unknown"
	}
	return shortDigest(info.ID)
}

// shortDigest trims the algorithm prefix and keeps the leading hex, the form
// Docker itself prints.
func shortDigest(d string) string {
	if _, hex, ok := strings.Cut(d, ":"); ok {
		d = hex
	}
	if len(d) > 12 {
		d = d[:12]
	}
	return d
}
