package agent

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
)

// imageAPI is the slice of the Docker client the image paths use. It exists as a
// seam so the pull-then-fall-back policy below can be unit-tested without a
// daemon (#288); in production the field holds *client.Client itself.
type imageAPI interface {
	ImageInspect(ctx context.Context, ref string, opts ...client.ImageInspectOption) (image.InspectResponse, error)
	ImagePull(ctx context.Context, ref string, options image.PullOptions) (io.ReadCloser, error)
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

// Pull deadlines. Both exist so a registry that accepts the connection and then
// stalls cannot wedge an install or an operator's START forever.
const (
	// installPullTimeout covers the install path, where a cold node legitimately
	// downloads a multi-gigabyte base image (kraken-steam-win is several GB) over
	// whatever link the operator has. Generous on purpose: there is no local copy
	// to fall back to on a first install, so cutting this short turns a slow link
	// into a hard failure.
	installPullTimeout = 30 * time.Minute
	// startPullTimeout covers the refresh on an operator-driven start, where the
	// image is almost always already current and the pull is a manifest check of
	// a few KB. A start must not hang on a slow or unreachable registry, and
	// timing out here is harmless: the fallback starts the server on the local
	// image. A tag that genuinely moved and needs a full layer transfer will
	// exceed this and be picked up by the next install instead.
	startPullTimeout = 2 * time.Minute
)

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
