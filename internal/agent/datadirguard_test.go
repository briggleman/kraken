package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"

	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// fakeOps is the containerOps seam: enough of a Docker daemon to drive the
// install guard, the stop confirmation and the name helpers without one.
type fakeOps struct {
	list    []container.Summary
	listErr error

	// names answers ContainerInspect by name. A removal deletes the name after
	// slowFree more inspects, which is how a slow Windows removal looks.
	names    map[string]container.InspectResponse
	slowFree int
	inspects int

	removeErr error
	removed   []string // ids, in order

	stopErr   error
	stops     int
	waitErr   error // sent on the error channel when set
	waitBlock bool  // neither channel ever fires (only ctx ends the wait)
	waits     int

	pending map[string]int // name → inspects left before it frees
}

func (f *fakeOps) ContainerList(_ context.Context, _ container.ListOptions) ([]container.Summary, error) {
	return f.list, f.listErr
}

func (f *fakeOps) ContainerInspect(_ context.Context, name string) (container.InspectResponse, error) {
	f.inspects++
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
		s.Mounts = append(s.Mounts, container.MountPoint{Type: "bind", Source: src, Destination: `C:\data`})
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
	err := clearDataDir(context.Background(), f, guardServer, guardSource, guardInst, true, func(s string) { notes = append(notes, s) })
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

// Both ways a container is tied to the data dir count, and the path compare
// tolerates the spellings a Windows daemon and a Windows Agent disagree on.
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
		{"mount of a child dir", summary(runningID, "x", container.StateRunning, nil, `C:\kraken\server-data\srv-1\saves`), true, false},
		{"no mounts, other label", summary(runningID, "x", container.StateRunning, map[string]string{labelServerID: "srv-2"}), true, false},
		{"empty mount source", summary(runningID, "x", container.StateRunning, nil, ""), true, false},
	}
	for _, tc := range cases {
		got := findDataDirHolders([]container.Summary{tc.c}, guardServer, guardSource, tc.fold)
		if (len(got) == 1) != tc.match {
			t.Errorf("%s: match = %v, want %v", tc.name, len(got) == 1, tc.match)
		}
		if len(got) == 1 && got[0].Name != "x" && got[0].Name != guardName {
			t.Errorf("%s: name %q should have Docker's leading slash trimmed", tc.name, got[0].Name)
		}
	}
	if got := findDataDirHolders([]container.Summary{summary(runningID, "x", container.StateRunning, nil, "")}, guardServer, "", true); len(got) != 0 {
		t.Error("an empty bind source must not match an empty mount source")
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
