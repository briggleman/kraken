package agent

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/docker/docker/api/types/container"
)

// An install pass runs SteamCMD in a one-shot container that bind-mounts the
// server's data dir — the same dir the game container mounts. Anything else
// with that dir open while SteamCMD writes is how an update corrupts a tree.
//
// That is not hypothetical. On 2026-09-15 an update pass on a Windows node ran
// while a game container still held handles on the data dir: SteamCMD staged
// the new server binary as `…~RF<hex>.TMP`, deleted the target, and could not
// rename the staged file into place. The tree was left without its server
// binary, and every later pass re-staged and re-failed the same rename (#349).
// The Panel's pre-update stop had returned success (#351).
//
// So the Agent does not take the stop's word for it. Before it creates the
// install container it looks at every container that could hold the dir —
// running or not, tracked or not — and either clears it or refuses the pass.

// containerOps is the slice of the Docker client the install guard, the stop
// confirmation and the container-name helpers use. *client.Client satisfies it;
// tests satisfy it with a fake, which is what makes these paths testable
// without a daemon.
type containerOps interface {
	ContainerList(ctx context.Context, options container.ListOptions) ([]container.Summary, error)
	containerRemovalAPI // ContainerInspect, ContainerRemove
	ContainerStop(ctx context.Context, containerID string, options container.StopOptions) error
	ContainerWait(ctx context.Context, containerID string, condition container.WaitCondition) (<-chan container.WaitResponse, <-chan error)
}

// dataDirHolder is a container that has a server's data dir bound, whatever its
// state. Name is without Docker's leading slash.
type dataDirHolder struct {
	ID    string
	Name  string
	State string
}

// String names the container the way an operator can find it: `docker ps`
// shows both the name and the short id.
func (h dataDirHolder) String() string {
	id := h.ID
	if len(id) > 12 {
		id = id[:12]
	}
	if h.Name == "" {
		return id
	}
	return h.Name + " (" + id + ")"
}

// stopped reports whether the container has no process that could hold the
// data dir open. Only the states Docker defines as "no process" count. Anything
// else — running, paused (the process is frozen, not gone), restarting,
// removing (still tearing down), or a state this Agent does not know — is
// treated as live, because the safe answer to "is something writing under
// SteamCMD?" is yes.
func (h dataDirHolder) stopped() bool {
	switch h.State {
	case container.StateCreated, container.StateExited, container.StateDead:
		return true
	}
	return false
}

// findDataDirHolders picks out of a container listing every container that
// carries this server's id label or bind-mounts its data dir. Both matter: the
// label finds Kraken's own containers however they were mounted, and the mount
// finds a container that is bound to the dir but lost the label — untracked,
// or made by hand.
//
// A mount counts when it is the data dir, anything inside it, or — writable —
// anything above it: a container binding a child or a parent has files in the
// tree open just as surely as one binding the dir itself. Two containers that
// bind a parent are not holders:
//
//   - the Agent's own container (selfID), which binds the whole data root by
//     design (deploy/docker-compose.full.yml); and
//   - a container that also binds the Docker socket — a control-plane
//     container like the Agent, never a game. This is the fallback for an
//     Agent whose own id could not be read.
//
// A read-only bind of a parent (cAdvisor's `/:/rootfs:ro`, say) is not a
// holder either: it cannot write under SteamCMD, and counting it would refuse
// every install on a host that runs one.
//
// bindSource is the daemon's view of the data dir (see DockerRuntime.bindSource)
// because Mounts[].Source is reported in the daemon's view too. fold compares
// the paths case-insensitively, which Windows paths need.
func findDataDirHolders(list []container.Summary, serverID, bindSource string, fold bool, selfID string) []dataDirHolder {
	var out []dataDirHolder
	for _, c := range list {
		if isSelf(c.ID, selfID) {
			continue
		}
		match := serverID != "" && c.Labels[labelServerID] == serverID
		for _, m := range c.Mounts {
			if match {
				break
			}
			switch mountOverlap(m.Source, bindSource, fold) {
			case overlapSame, overlapInside:
				match = true
			case overlapAbove:
				match = m.RW && !bindsDockerSocket(c)
			}
		}
		if !match {
			continue
		}
		name := ""
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		out = append(out, dataDirHolder{ID: c.ID, Name: name, State: c.State})
	}
	return out
}

// dockerDesktopHostPrefix is how Docker Desktop's Linux engine, on a Windows
// host, reports a bind of a Windows path: `C:\kraken\data\x` comes back as
// `/run/desktop/mnt/host/c/kraken/data/x`. The Agent's bind source is the
// Windows form, so without translating one to the other an unlabelled holder
// on such a host is never seen.
const dockerDesktopHostPrefix = "/run/desktop/mnt/host/"

// normHostPath puts a host path into one comparable form: forward slashes,
// cleaned, and Docker Desktop's Linux-engine form translated back to a drive
// path (`/run/desktop/mnt/host/c/x` → `C:/x`). "" stays "".
func normHostPath(p string) string {
	p = strings.ReplaceAll(p, `\`, "/")
	if p == "" {
		return ""
	}
	p = path.Clean(p)
	if rest, ok := strings.CutPrefix(p, dockerDesktopHostPrefix); ok && len(rest) >= 1 {
		drive, tail, _ := strings.Cut(rest, "/")
		if len(drive) == 1 {
			p = strings.ToUpper(drive) + ":/" + tail
			p = path.Clean(p)
		}
	}
	return p
}

// overlap is how a mount source relates to the data dir.
type overlap int

const (
	overlapNone   overlap = iota
	overlapSame           // the data dir itself
	overlapInside         // a dir or file inside the data dir
	overlapAbove          // a parent of the data dir
)

// mountOverlap says how mount source src relates to the data dir. The
// comparison is by path segment, so `srv-1` and `srv-10` do not overlap. fold
// compares case-insensitively, which Windows paths need. An empty path never
// overlaps anything.
func mountOverlap(src, dataDir string, fold bool) overlap {
	ns, nd := normHostPath(src), normHostPath(dataDir)
	if ns == "" || nd == "" {
		return overlapNone
	}
	if fold {
		ns, nd = strings.ToLower(ns), strings.ToLower(nd)
	}
	switch {
	case ns == nd:
		return overlapSame
	case isPathWithin(ns, nd):
		return overlapInside
	case isPathWithin(nd, ns):
		return overlapAbove
	}
	return overlapNone
}

// bindsDockerSocket reports whether a container mounts the Docker socket.
func bindsDockerSocket(c container.Summary) bool {
	for _, m := range c.Mounts {
		if strings.HasSuffix(strings.ReplaceAll(m.Source, `\`, "/"), "/docker.sock") ||
			strings.Contains(strings.ToLower(m.Source), `pipe\docker_engine`) {
			return true
		}
	}
	return false
}

// isSelf reports whether container id is the Agent's own container. selfID may
// be a full id or a prefix; "" never matches.
func isSelf(id, selfID string) bool {
	return selfID != "" && strings.HasPrefix(id, selfID)
}

// isPathWithin reports whether child is strictly inside parent, by segment.
// Both are normalised and cleaned.
func isPathWithin(child, parent string) bool {
	if !strings.HasSuffix(parent, "/") {
		parent += "/"
	}
	return strings.HasPrefix(child, parent)
}

// dataDirPlan is what the guard will do about the holders it found.
type dataDirPlan struct {
	// remove are containers to force-remove before the pass: stopped holders
	// (the runtime container is recreated on the next start anyway, and "the
	// container is gone" is the best available proxy for its handles being
	// released on Windows) and a leftover of this pass's own install container,
	// whatever its state — the pass is about to replace it under the same name.
	remove []dataDirHolder
	// refuse are live holders. The pass does not run while any exists, and the
	// guard never stops them: a live container here means the stop before the
	// update was ineffective or the container is untracked, and either deserves
	// an operator's eyes rather than a silent kill.
	refuse []dataDirHolder
}

// planDataDirHolders sorts the holders into what gets removed and what blocks
// the pass. installName is this pass's install container name.
func planDataDirHolders(holders []dataDirHolder, installName string) dataDirPlan {
	var p dataDirPlan
	for _, h := range holders {
		switch {
		case h.Name == installName, h.stopped():
			p.remove = append(p.remove, h)
		default:
			p.refuse = append(p.refuse, h)
		}
	}
	return p
}

// dataDirRefusal is the failure the pass reports when a live container holds
// the data dir. It names every one of them, with its state, and says what the
// operator should do.
func dataDirRefusal(refuse []dataDirHolder) string {
	names := make([]string, 0, len(refuse))
	for _, h := range refuse {
		names = append(names, h.String()+" is "+h.State)
	}
	return "refused to run the install pass: container " + strings.Join(names, ", container ") +
		" with this server's data dir mounted, and SteamCMD writing under a live container corrupts the install tree." +
		" Nothing was changed. Stop that container (it may be one Kraken is not tracking), then run the install again."
}

// dataDirRemovalNote is the install-console line for a holder the guard removed.
func dataDirRemovalNote(h dataDirHolder, installName string) string {
	if h.Name == installName {
		return "[kraken] removed the previous install container " + h.String() + " before this pass"
	}
	return "[kraken] removed " + h.State + " container " + h.String() + " that still had this server's data dir mounted"
}

// clearDataDir is the guard itself: list every container on the node, find the
// ones holding this server's data dir, remove the stopped ones (waiting for
// each name to come free) and refuse the pass if any live one remains. note
// receives a line for each container removed.
//
// The refusal is checked before anything is removed, so a refused pass leaves
// the node exactly as it found it.
func clearDataDir(ctx context.Context, c containerOps, serverID, bindSource, installName string, fold bool, selfID string, note func(string)) error {
	list, err := c.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return fmt.Errorf("list containers to check nothing holds the data dir: %w", err)
	}
	plan := planDataDirHolders(findDataDirHolders(list, serverID, bindSource, fold, selfID), installName)
	if len(plan.refuse) > 0 {
		return fmt.Errorf("%s", dataDirRefusal(plan.refuse))
	}
	for _, h := range plan.remove {
		if err := removeAndAwait(ctx, c, h.ID, h.Name); err != nil {
			return fmt.Errorf("clear %s from the data dir: %w", h, err)
		}
		note(dataDirRemovalNote(h, installName))
	}
	return nil
}
