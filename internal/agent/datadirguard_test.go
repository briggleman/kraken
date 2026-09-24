package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/pkg/stdcopy"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// fakeOps is the containerOps seam: enough of a Docker daemon to drive the
// install guard, the stop confirmation and the name helpers without one.
type fakeOps struct {
	list    []container.Summary
	listErr error

	// names answers ContainerInspect by name. A removal deletes the name after
	// slowFree more inspects, which is how a slow Windows removal looks.
	names       map[string]container.InspectResponse
	slowFree    int
	inspects    int
	inspectErrs []error // returned, in order, before any name lookup

	removeErr error
	removed   []string // ids, in order

	stopErr   error
	stops     int
	kills     int
	waitErr   error // sent on the error channel when set
	waitBlock bool  // neither channel ever fires (only ctx ends the wait)
	waits     int

	pending map[string]int // name → inspects left before it frees

	lists    int        // ContainerList calls — one per guard run
	creates  []string   // names passed to ContainerCreate, in order
	passLogs [][]string // the output of each created container, in order
}

func (f *fakeOps) ContainerList(_ context.Context, _ container.ListOptions) ([]container.Summary, error) {
	f.lists++
	return f.list, f.listErr
}

// ContainerCreate records the name and hands out install-<n> ids; the n-th
// created container's logs are passLogs[n-1].
func (f *fakeOps) ContainerCreate(_ context.Context, _ *container.Config, _ *container.HostConfig, _ *network.NetworkingConfig, _ *ocispec.Platform, name string) (container.CreateResponse, error) {
	f.creates = append(f.creates, name)
	return container.CreateResponse{ID: fmt.Sprintf("install-%d", len(f.creates))}, nil
}

func (f *fakeOps) ContainerStart(context.Context, string, container.StartOptions) error { return nil }

// ContainerLogs replays the pass's scripted output in Docker's multiplexed
// stdout format, the way the daemon streams it.
func (f *fakeOps) ContainerLogs(_ context.Context, id string, _ container.LogsOptions) (io.ReadCloser, error) {
	var n int
	_, _ = fmt.Sscanf(id, "install-%d", &n)
	var buf bytes.Buffer
	w := stdcopy.NewStdWriter(&buf, stdcopy.Stdout)
	if n >= 1 && n <= len(f.passLogs) {
		for _, line := range f.passLogs[n-1] {
			_, _ = w.Write([]byte(line + "\n"))
		}
	}
	return io.NopCloser(&buf), nil
}

func (f *fakeOps) ContainerInspect(_ context.Context, name string) (container.InspectResponse, error) {
	f.inspects++
	if len(f.inspectErrs) > 0 {
		err := f.inspectErrs[0]
		f.inspectErrs = f.inspectErrs[1:]
		return container.InspectResponse{}, err
	}
	if left, ok := f.pending[name]; ok {
		if left > 0 {
			f.pending[name] = left - 1
			return f.names[name], nil
		}
		delete(f.pending, name)
		delete(f.names, name)
	}
	if info, ok := f.names[name]; ok {
		return info, nil
	}
	return container.InspectResponse{}, fmt.Errorf("no such container %s: %w", name, cerrdefs.ErrNotFound)
}

func (f *fakeOps) ContainerRemove(_ context.Context, id string, _ container.RemoveOptions) error {
	f.removed = append(f.removed, id)
	for name, info := range f.names {
		if info.ID == id {
			if f.pending == nil {
				f.pending = make(map[string]int)
			}
			f.pending[name] = f.slowFree
		}
	}
	return f.removeErr
}

func (f *fakeOps) ContainerKill(context.Context, string, string) error {
	f.kills++
	return nil
}

func (f *fakeOps) ContainerStop(_ context.Context, _ string, _ container.StopOptions) error {
	f.stops++
	return f.stopErr
}

func (f *fakeOps) ContainerWait(ctx context.Context, _ string, _ container.WaitCondition) (<-chan container.WaitResponse, <-chan error) {
	f.waits++
	status := make(chan container.WaitResponse, 1)
	errs := make(chan error, 1)
	switch {
	case f.waitBlock:
		go func() {
			<-ctx.Done()
			errs <- ctx.Err()
		}()
	case f.waitErr != nil:
		errs <- f.waitErr
	default:
		status <- container.WaitResponse{StatusCode: 0}
	}
	return status, errs
}

const (
	guardServer = "srv-1"
	guardSource = `C:\kraken\server-data\srv-1`
	guardName   = "kraken_srv-1"
	guardInst   = "kraken_srv-1_install"
)

var (
	runningID = strings.Repeat("a", 64)
	exitedID  = strings.Repeat("b", 64)
	otherID   = strings.Repeat("c", 64)
)

func summary(id, name, state string, labels map[string]string, sources ...string) container.Summary {
	s := container.Summary{ID: id, Names: []string{"/" + name}, State: state, Labels: labels}
	for _, src := range sources {
		s.Mounts = append(s.Mounts, container.MountPoint{Type: "bind", Source: src, Destination: `C:\data`, RW: true})
	}
	return s
}

func ours() map[string]string {
	return map[string]string{labelServerID: guardServer, labelManaged: "true"}
}

func withName(f *fakeOps, id, name string) {
	if f.names == nil {
		f.names = make(map[string]container.InspectResponse)
	}
	f.names[name] = container.InspectResponse{ContainerJSONBase: &container.ContainerJSONBase{ID: id, Name: "/" + name}}
}

func runGuard(t *testing.T, f *fakeOps) ([]string, error) {
	t.Helper()
	var notes []string
	err := clearDataDir(context.Background(), f, guardServer, guardSource, guardInst, true, "", func(s string) { notes = append(notes, s) })
	return notes, err
}

// A running container on the data dir refuses the pass, names the container,
// and touches nothing — not even the stopped holder beside it.
func TestClearDataDir_RefusesARunningHolder(t *testing.T) {
	f := &fakeOps{list: []container.Summary{
		summary(runningID, guardName, container.StateRunning, ours(), guardSource),
		summary(exitedID, "leftover", container.StateExited, nil, guardSource),
	}}
	notes, err := runGuard(t, f)
	if err == nil {
		t.Fatal("a running container holds the data dir; the pass must be refused")
	}
	for _, want := range []string{"refused", guardName, runningID[:12], "running"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal should contain %q; got %q", want, err)
		}
	}
	if strings.Contains(err.Error(), runningID) {
		t.Errorf("refusal should use the short id, not the full one: %q", err)
	}
	if len(f.removed) != 0 || len(notes) != 0 {
		t.Errorf("a refused pass must leave the node as it found it; removed %v, notes %v", f.removed, notes)
	}
	if f.stops != 0 {
		t.Error("the guard must never stop the holder itself")
	}
}

// An exited container bound to the dir — found by its mount alone, with no
// label — is removed, its name awaited, and the pass goes ahead.
func TestClearDataDir_RemovesAnExitedHolderAndAwaitsIt(t *testing.T) {
	f := &fakeOps{
		list:     []container.Summary{summary(exitedID, "hand-made", container.StateExited, nil, guardSource)},
		slowFree: 1,
	}
	withName(f, exitedID, "hand-made")
	notes, err := runGuard(t, f)
	if err != nil {
		t.Fatalf("an exited holder must not block the pass: %v", err)
	}
	if len(f.removed) != 1 || f.removed[0] != exitedID {
		t.Fatalf("want the exited holder removed by id; removed %v", f.removed)
	}
	if f.inspects < 2 {
		t.Errorf("the guard returned before the removed name came free (%d inspects)", f.inspects)
	}
	if len(notes) != 1 || !strings.HasPrefix(notes[0], "[kraken] ") || !strings.Contains(notes[0], "hand-made") {
		t.Errorf("want one [kraken] console line naming the removed container; got %v", notes)
	}
}

// The previous pass's install container is cleared whatever its state — it is
// about to be replaced under the same name — and it is not a reason to refuse.
func TestClearDataDir_ClearsThePreviousInstallContainer(t *testing.T) {
	f := &fakeOps{list: []container.Summary{summary(runningID, guardInst, container.StateRunning, ours(), guardSource)}}
	withName(f, runningID, guardInst)
	notes, err := runGuard(t, f)
	if err != nil {
		t.Fatalf("the old install container must be cleared, not refused: %v", err)
	}
	if len(f.removed) != 1 || f.removed[0] != runningID {
		t.Fatalf("want the old install container removed; removed %v", f.removed)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "previous install container") {
		t.Errorf("want a console line for the old install container; got %v", notes)
	}
}

// Containers that neither carry this server's label nor mount its dir — other
// servers, other workloads — are none of the guard's business.
func TestClearDataDir_IgnoresUnrelatedContainers(t *testing.T) {
	f := &fakeOps{list: []container.Summary{
		summary(runningID, "kraken_srv-2", container.StateRunning,
			map[string]string{labelServerID: "srv-2"}, `C:\kraken\server-data\srv-2`),
		summary(otherID, "postgres", container.StateRunning, nil, `C:\pg`),
		summary(exitedID, "kraken_srv-10", container.StateExited,
			map[string]string{labelServerID: "srv-10"}, `C:\kraken\server-data\srv-10`),
	}}
	notes, err := runGuard(t, f)
	if err != nil {
		t.Fatalf("unrelated containers must not block the pass: %v", err)
	}
	if len(f.removed) != 0 || len(notes) != 0 {
		t.Errorf("unrelated containers must not be touched; removed %v", f.removed)
	}
}

func TestClearDataDir_ListFailureFailsThePass(t *testing.T) {
	f := &fakeOps{listErr: errors.New("daemon unreachable")}
	if _, err := runGuard(t, f); err == nil || !strings.Contains(err.Error(), "daemon unreachable") {
		t.Fatalf("a pass that cannot check what holds the dir must not run; got %v", err)
	}
}

// readOnly marks every mount of s read-only.
func readOnly(s container.Summary) container.Summary {
	for i := range s.Mounts {
		s.Mounts[i].RW = false
	}
	return s
}

// withSocket adds a Docker-socket bind to s.
func withSocket(s container.Summary) container.Summary {
	s.Mounts = append(s.Mounts, container.MountPoint{Type: "bind", Source: "/var/run/docker.sock", Destination: "/var/run/docker.sock", RW: true})
	return s
}

// Both ways a container is tied to the data dir count — the label, and a bind
// of the dir, a dir inside it, or (writable) a dir above it — and the path
// compare tolerates the spellings a Windows daemon, a Windows Agent and Docker
// Desktop's Linux engine disagree on.
func TestFindDataDirHolders(t *testing.T) {
	cases := []struct {
		name  string
		c     container.Summary
		fold  bool
		match bool
	}{
		{"label only", summary(runningID, guardName, container.StateRunning, ours()), true, true},
		{"mount only", summary(runningID, "x", container.StateRunning, nil, guardSource), true, true},
		{"mount, drive letter case", summary(runningID, "x", container.StateRunning, nil, `c:\KRAKEN\server-data\srv-1`), true, true},
		{"mount, forward slashes and trailing slash", summary(runningID, "x", container.StateRunning, nil, `C:/kraken/server-data/srv-1/`), true, true},
		{"mount, case differs on a case-sensitive host", summary(runningID, "x", container.StateRunning, nil, `C:\KRAKEN\server-data\srv-1`), false, false},
		{"mount of a sibling dir", summary(runningID, "x", container.StateRunning, nil, `C:\kraken\server-data\srv-10`), true, false},
		{"mount of a sibling dir sharing a prefix", summary(runningID, "x", container.StateRunning, nil, `C:\kraken\server-data\srv-1-old`), true, false},
		// A container binding a child has files in the tree open: a holder.
		{"mount of a child dir", summary(runningID, "x", container.StateRunning, nil, `C:\kraken\server-data\srv-1\saves`), true, true},
		{"mount of a child file", summary(runningID, "x", container.StateRunning, nil, `C:\kraken\server-data\srv-1\server.cfg`), true, true},
		// So does one binding a parent, writable.
		{"writable mount of a parent", summary(runningID, "x", container.StateRunning, nil, `C:\kraken\server-data`), true, true},
		{"writable mount of the drive", summary(runningID, "x", container.StateRunning, nil, `C:\`), true, true},
		// A read-only parent (cAdvisor's /:/rootfs:ro) cannot write under SteamCMD.
		{"read-only mount of a parent", readOnly(summary(runningID, "cadvisor", container.StateRunning, nil, `C:\`)), true, false},
		// A read-only bind of the dir itself still counts: it holds the files.
		{"read-only mount of the dir", readOnly(summary(runningID, "x", container.StateRunning, nil, guardSource)), true, true},
		// A control-plane container binding the data root plus the socket — the
		// Agent itself when its own id could not be read — is not a game.
		{"parent plus docker socket", withSocket(summary(runningID, "kraken-agent", container.StateRunning, nil, `C:\kraken\server-data`)), true, false},
		{"docker socket does not excuse binding the dir itself", withSocket(summary(runningID, "x", container.StateRunning, nil, guardSource)), true, true},
		// Docker Desktop's Linux engine on a Windows host reports binds this way.
		{"docker desktop form", summary(runningID, "x", container.StateRunning, nil, `/run/desktop/mnt/host/c/kraken/server-data/srv-1`), true, true},
		{"docker desktop form, child", summary(runningID, "x", container.StateRunning, nil, `/run/desktop/mnt/host/c/kraken/server-data/srv-1/saves`), true, true},
		{"docker desktop form, other drive", summary(runningID, "x", container.StateRunning, nil, `/run/desktop/mnt/host/d/kraken/server-data/srv-1`), true, false},
		{"no mounts, other label", summary(runningID, "x", container.StateRunning, map[string]string{labelServerID: "srv-2"}), true, false},
		{"empty mount source", summary(runningID, "x", container.StateRunning, nil, ""), true, false},
	}
	for _, tc := range cases {
		got := findDataDirHolders([]container.Summary{tc.c}, guardServer, guardSource, tc.fold, "")
		if (len(got) == 1) != tc.match {
			t.Errorf("%s: match = %v, want %v", tc.name, len(got) == 1, tc.match)
		}
		if len(got) == 1 && strings.HasPrefix(got[0].Name, "/") {
			t.Errorf("%s: name %q should have Docker's leading slash trimmed", tc.name, got[0].Name)
		}
	}
	if got := findDataDirHolders([]container.Summary{summary(runningID, "x", container.StateRunning, nil, "")}, guardServer, "", true, ""); len(got) != 0 {
		t.Error("an empty bind source must not match an empty mount source")
	}
}

// The same rules on a Linux node, where paths are case-sensitive and nothing
// is folded: the compose Agent's data-root bind (with the socket) and a
// read-only `/` are skipped; a stray container binding the root writable is a
// holder.
func TestFindDataDirHolders_LinuxPaths(t *testing.T) {
	const root = "/var/lib/kraken/server-data"
	const dir = root + "/srv-1"
	cases := []struct {
		name  string
		c     container.Summary
		match bool
	}{
		{"the dir", summary(runningID, "x", container.StateRunning, nil, dir), true},
		{"a child", summary(runningID, "x", container.StateRunning, nil, dir+"/saves"), true},
		{"a sibling sharing a prefix", summary(runningID, "x", container.StateRunning, nil, dir+"0"), false},
		{"different case is a different path", summary(runningID, "x", container.StateRunning, nil, "/var/lib/Kraken/server-data/srv-1"), false},
		{"the Agent: data root plus the socket", withSocket(summary(runningID, "kraken-agent", container.StateRunning, nil, root)), false},
		{"cAdvisor: / read-only", readOnly(summary(runningID, "cadvisor", container.StateRunning, nil, "/")), false},
		{"a stray container binding the root writable", summary(runningID, "stray", container.StateRunning, nil, root), true},
		{"a stray container binding / writable", summary(runningID, "stray", container.StateRunning, nil, "/"), true},
	}
	for _, tc := range cases {
		got := findDataDirHolders([]container.Summary{tc.c}, guardServer, dir, false, "")
		if (len(got) == 1) != tc.match {
			t.Errorf("%s: match = %v, want %v", tc.name, len(got) == 1, tc.match)
		}
	}
	// And through the guard: the stray writable root bind refuses the pass.
	f := &fakeOps{list: []container.Summary{
		withSocket(summary(otherID, "kraken-agent", container.StateRunning, nil, root)),
		summary(runningID, "stray", container.StateRunning, nil, root),
	}}
	err := clearDataDir(context.Background(), f, guardServer, dir, guardInst, false, "", func(string) {})
	if err == nil || !strings.Contains(err.Error(), "stray") || strings.Contains(err.Error(), "kraken-agent") {
		t.Errorf("want a refusal naming the stray container only; got %v", err)
	}
}

// The Agent's own container binds the whole data root under
// docker-compose.full.yml; the guard must never refuse a pass over it.
func TestFindDataDirHolders_SkipsTheAgentsOwnContainer(t *testing.T) {
	agentC := summary(runningID, "kraken-agent", container.StateRunning, nil, `C:\kraken\server-data`)
	if got := findDataDirHolders([]container.Summary{agentC}, guardServer, guardSource, true, runningID); len(got) != 0 {
		t.Fatalf("the Agent's own container was treated as a holder: %v", got)
	}
	if got := findDataDirHolders([]container.Summary{agentC}, guardServer, guardSource, true, runningID[:12]); len(got) != 0 {
		t.Fatalf("a short self id should match too: %v", got)
	}
	if got := findDataDirHolders([]container.Summary{agentC}, guardServer, guardSource, true, otherID); len(got) != 1 {
		t.Fatal("another container binding the root writable is a holder")
	}
}

func TestParseSelfContainerID(t *testing.T) {
	id := strings.Repeat("d", 64)
	mountinfo := "1503 1480 0:79 / / rw,relatime - overlay overlay rw\n" +
		"1520 1503 8:1 /var/lib/docker/containers/" + id + "/resolv.conf /etc/resolv.conf rw,relatime - ext4 /dev/sda1 rw\n" +
		"1521 1503 8:1 /var/lib/docker/containers/" + id + "/hostname /etc/hostname rw,relatime - ext4 /dev/sda1 rw\n"
	if got := parseSelfContainerID(mountinfo); got != id {
		t.Errorf("got %q, want %q", got, id)
	}
	// /var/lib/docker on its own filesystem: the root field starts at /containers.
	if got := parseSelfContainerID("99 1 8:2 /containers/" + id + "/hosts /etc/hosts rw - ext4 /dev/sdb1 rw\n"); got != id {
		t.Errorf("separate docker fs: got %q", got)
	}
	if got := parseSelfContainerID("22 1 8:1 / / rw - ext4 /dev/sda1 rw\n"); got != "" {
		t.Errorf("a host process has no container id, got %q", got)
	}
}

// Only the states Docker defines as "no process" are cleared; every other
// state — including one this Agent has never heard of — blocks the pass.
func TestPlanDataDirHolders_States(t *testing.T) {
	cases := map[string]bool{ // state → refused
		container.StateCreated:    false,
		container.StateExited:     false,
		container.StateDead:       false,
		container.StateRunning:    true,
		container.StatePaused:     true,
		container.StateRestarting: true,
		container.StateRemoving:   true,
		"something-new":           true,
	}
	for state, refused := range cases {
		p := planDataDirHolders([]dataDirHolder{{ID: runningID, Name: guardName, State: state}}, guardInst)
		if (len(p.refuse) == 1) != refused || (len(p.remove) == 1) == refused {
			t.Errorf("state %q: refuse=%v remove=%v, want refused=%v", state, p.refuse, p.remove, refused)
		}
	}
}

// ---- stopAndConfirm ----

func TestStopAndConfirm_MissingContainerIsStopped(t *testing.T) {
	f := &fakeOps{stopErr: fmt.Errorf("no such container: %w", cerrdefs.ErrNotFound)}
	if err := stopAndConfirm(context.Background(), f, guardName, container.StopOptions{}, time.Second); err != nil {
		t.Fatalf("stopping a container that is not there must succeed: %v", err)
	}
	if f.waits != 0 {
		t.Error("nothing to confirm for a container that is not there")
	}
}

func TestStopAndConfirm_WaitsForTheDaemonToAgree(t *testing.T) {
	f := &fakeOps{}
	if err := stopAndConfirm(context.Background(), f, guardName, container.StopOptions{}, time.Second); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if f.stops != 1 || f.waits != 1 {
		t.Errorf("want one stop then one wait-for-not-running; got %d stops, %d waits", f.stops, f.waits)
	}
}

func TestStopAndConfirm_RemovedWhileWaitingIsStopped(t *testing.T) {
	f := &fakeOps{waitErr: fmt.Errorf("no such container: %w", cerrdefs.ErrNotFound)}
	if err := stopAndConfirm(context.Background(), f, guardName, container.StopOptions{}, time.Second); err != nil {
		t.Fatalf("a container that vanished during the wait is stopped: %v", err)
	}
}

func TestStopAndConfirm_StillRunningIsAnError(t *testing.T) {
	f := &fakeOps{waitBlock: true}
	start := time.Now()
	err := stopAndConfirm(context.Background(), f, guardName, container.StopOptions{}, 30*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "still running") {
		t.Fatalf("a stop the daemon never confirms must fail distinctly; got %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Error("the confirmation must be bounded")
	}
}

func TestStopAndConfirm_OtherStopErrorsSurface(t *testing.T) {
	f := &fakeOps{stopErr: errors.New("daemon hung up")}
	if err := stopAndConfirm(context.Background(), f, guardName, container.StopOptions{}, time.Second); err == nil {
		t.Fatal("a stop the daemon rejected must fail")
	}
}

// ---- removeAndAwait / resolveNameConflict ----

// Two removals overlapping get a 409 "removal … is already in progress" from
// the daemon. That is the slow removal the wait exists for, not a failure.
func TestRemoveAndAwait_RemovalAlreadyInProgressWaits(t *testing.T) {
	f := &fakeOps{
		removeErr: fmt.Errorf("removal of container %s is already in progress: %w", exitedID, cerrdefs.ErrConflict),
		slowFree:  1,
	}
	withName(f, exitedID, guardName)
	if err := removeAndAwait(context.Background(), f, exitedID, guardName); err != nil {
		t.Fatalf("an in-progress removal must be waited out, not failed: %v", err)
	}
	if _, err := f.ContainerInspect(context.Background(), guardName); !isNotFound(err) {
		t.Error("removeAndAwait returned before the name came free")
	}
}

func TestRemoveAndAwait_OtherRemoveErrorsFail(t *testing.T) {
	f := &fakeOps{removeErr: errors.New("daemon hung up")}
	withName(f, exitedID, guardName)
	if err := removeAndAwait(context.Background(), f, exitedID, guardName); err == nil {
		t.Fatal("a removal the daemon rejected must fail")
	}
}

func inspectState(id, serverID string, running bool) container.InspectResponse {
	return container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{ID: id, State: &container.State{Running: running}},
		Config:            &container.Config{Labels: map[string]string{labelServerID: serverID}},
	}
}

func createdState(id, serverID string) container.InspectResponse {
	info := inspectState(id, serverID, false)
	info.State.Status = container.StateCreated
	return info
}

// A create that loses the name to this server's own container while it is
// still `created` — the winner has not called ContainerStart yet — must leave
// it alone: removing it would make the winner's start-by-name fail.
func TestResolveNameConflict_AdoptsOurCreatedContainer(t *testing.T) {
	f := &fakeOps{names: map[string]container.InspectResponse{guardName: createdState(runningID, guardServer)}}
	adopted, err := resolveNameConflict(context.Background(), f, guardServer, guardName)
	if err != nil || !adopted {
		t.Fatalf("want adopted, got adopted=%v err=%v", adopted, err)
	}
	if len(f.removed) != 0 {
		t.Fatalf("the other start's created container was removed: %v", f.removed)
	}
}

func TestInspectRetryOnce(t *testing.T) {
	hiccup := errors.New("daemon: connection reset")
	t.Run("recovers from one hiccup", func(t *testing.T) {
		f := &fakeOps{inspectErrs: []error{hiccup}}
		withName(f, runningID, guardName)
		if _, err := inspectRetryOnce(context.Background(), f, guardName, 0); err != nil {
			t.Fatalf("want the retry to succeed, got %v", err)
		}
		if f.inspects != 2 {
			t.Errorf("want 2 inspects, got %d", f.inspects)
		}
	})
	t.Run("not found is an answer", func(t *testing.T) {
		f := &fakeOps{}
		if _, err := inspectRetryOnce(context.Background(), f, guardName, 0); !isNotFound(err) || f.inspects != 1 {
			t.Fatalf("want one inspect and not-found, got %d, %v", f.inspects, err)
		}
	})
	t.Run("gives up after the second", func(t *testing.T) {
		f := &fakeOps{inspectErrs: []error{hiccup, hiccup}}
		if _, err := inspectRetryOnce(context.Background(), f, guardName, 0); !errors.Is(err, hiccup) || f.inspects != 2 {
			t.Fatalf("want 2 inspects and the error, got %d, %v", f.inspects, err)
		}
	})
}

func TestDecideNameHolder(t *testing.T) {
	cases := []struct {
		name string
		info container.InspectResponse
		want nameHolderAction
	}{
		{"ours, running", inspectState(runningID, guardServer, true), holderAdopt},
		{"ours, exited", inspectState(exitedID, guardServer, false), holderRemove},
		{"foreign, running", inspectState(otherID, "srv-2", true), holderRefuse},
		{"foreign, exited", inspectState(otherID, "srv-2", false), holderRemove},
		{"no state", container.InspectResponse{ContainerJSONBase: &container.ContainerJSONBase{ID: otherID}}, holderRemove},
		// The other start, caught between its create and its ContainerStart.
		{"ours, created", createdState(runningID, guardServer), holderAdopt},
		{"foreign, created", createdState(otherID, "srv-2"), holderRemove},
	}
	for _, tc := range cases {
		if got := decideNameHolder(tc.info, guardServer); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A create that loses the name to this server's own running container — a
// watchdog fast-restart, a double-clicked Start — must adopt it, not kill it.
func TestResolveNameConflict_AdoptsOurRunningContainer(t *testing.T) {
	f := &fakeOps{names: map[string]container.InspectResponse{guardName: inspectState(runningID, guardServer, true)}}
	adopted, err := resolveNameConflict(context.Background(), f, guardServer, guardName)
	if err != nil || !adopted {
		t.Fatalf("want adopted, got adopted=%v err=%v", adopted, err)
	}
	if len(f.removed) != 0 {
		t.Fatalf("a healthy running container of this server's was removed: %v", f.removed)
	}
}

func TestResolveNameConflict_RefusesAForeignRunningContainer(t *testing.T) {
	f := &fakeOps{names: map[string]container.InspectResponse{guardName: inspectState(otherID, "srv-2", true)}}
	adopted, err := resolveNameConflict(context.Background(), f, guardServer, guardName)
	if err == nil || adopted {
		t.Fatalf("want a refusal, got adopted=%v err=%v", adopted, err)
	}
	if !strings.Contains(err.Error(), guardName) || !strings.Contains(err.Error(), otherID[:12]) {
		t.Errorf("the refusal should name the container; got %q", err)
	}
	if len(f.removed) != 0 {
		t.Fatalf("a running container the Agent cannot account for was removed: %v", f.removed)
	}
}

func TestResolveNameConflict_ClearsAStoppedOrphan(t *testing.T) {
	f := &fakeOps{names: map[string]container.InspectResponse{guardName: inspectState(exitedID, guardServer, false)}}
	adopted, err := resolveNameConflict(context.Background(), f, guardServer, guardName)
	if err != nil || adopted {
		t.Fatalf("want the orphan cleared, got adopted=%v err=%v", adopted, err)
	}
	if len(f.removed) != 1 || f.removed[0] != exitedID {
		t.Fatalf("want the orphan removed by id; removed %v", f.removed)
	}
}

// ---- FakeRuntime: the guard as the Panel sees it ----

// The property #351 exists for: whatever state a container holding the data
// dir is in, an install container is never run while that container is live,
// and a stopped one never blocks the pass.
func TestFakeInstall_NeverRunsWhileADataDirHolderIsLive(t *testing.T) {
	states := []string{
		container.StateCreated, container.StateRunning, container.StatePaused, container.StateRestarting,
		container.StateRemoving, container.StateExited, container.StateDead, "something-new",
	}
	for _, state := range states {
		t.Run(state, func(t *testing.T) {
			f := NewFakeRuntime("n", "windows", false, "test")
			f.HoldDataDir(guardServer, guardName, state)
			var failed string
			var lines []string
			err := f.Install(context.Background(), &agentpb.InstallServerRequest{ServerId: guardServer, InstallScript: "steamcmd"},
				func(ev *agentpb.InstallEvent) error {
					switch e := ev.Event.(type) {
					case *agentpb.InstallEvent_Failed:
						failed = e.Failed
					case *agentpb.InstallEvent_LogLine:
						lines = append(lines, e.LogLine)
					}
					return nil
				})
			if err != nil {
				t.Fatalf("install: %v", err)
			}
			live := !(dataDirHolder{State: state}).stopped()
			ran := len(f.InstallScripts(guardServer)) > 0
			if live && (ran || !strings.Contains(failed, "refused") || !strings.Contains(failed, guardName)) {
				t.Errorf("live holder: ran=%v failed=%q; want no pass and a refusal naming %s", ran, failed, guardName)
			}
			if !live && (!ran || failed != "" || len(lines) == 0 || !strings.Contains(lines[0], guardName)) {
				t.Errorf("stopped holder: ran=%v failed=%q lines=%v; want it removed and the pass run", ran, failed, lines)
			}
		})
	}
}
