package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"

	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// fakeContainers is a daemon that knows a few containers by name. Removal is
// by ID and lands at once; removeErr makes every removal fail instead.
type fakeContainers struct {
	mu        sync.Mutex
	byName    map[string]string // name → id
	removed   []string          // names, in the order they went
	removeErr error
}

func (f *fakeContainers) ContainerInspect(_ context.Context, ref string) (container.InspectResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id, ok := f.byName[ref]; ok {
		return container.InspectResponse{ContainerJSONBase: &container.ContainerJSONBase{ID: id, Name: "/" + ref}}, nil
	}
	return container.InspectResponse{}, fmt.Errorf("no such container: %s: %w", ref, cerrdefs.ErrNotFound)
}

func (f *fakeContainers) ContainerRemove(_ context.Context, id string, _ container.RemoveOptions) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.removeErr != nil {
		return f.removeErr
	}
	for name, cid := range f.byName {
		if cid == id {
			delete(f.byName, name)
			f.removed = append(f.removed, name)
			return nil
		}
	}
	return fmt.Errorf("no such container: %s: %w", id, cerrdefs.ErrNotFound)
}

// newRemoveRuntime is a DockerRuntime wired for Remove alone: a fake daemon, a
// temp data root holding one server's world, and that server's persisted spec.
func newRemoveRuntime(t *testing.T, serverID string, f *fakeContainers) *DockerRuntime {
	t.Helper()
	d := &DockerRuntime{
		containers: f,
		dataDir:    t.TempDir(),
		specDir:    t.TempDir(),
		osType:     "linux",
		specs:      map[string]*agentpb.ServerSpec{},
		monitors:   map[string]*monitor{},
		backupJobs: map[string]*agentpb.BackupInfo{},
	}
	if err := os.MkdirAll(filepath.Join(d.localDir(serverID), "world"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d.localDir(serverID), "world", "level.sav"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	d.putSpec(&agentpb.ServerSpec{ServerId: serverID})
	if _, err := os.Stat(d.specFile(serverID)); err != nil {
		t.Fatalf("setup: spec not persisted: %v", err)
	}
	return d
}

// Both of a server's containers go — the runtime one and the install one an
// interrupted install can leave behind — and the name is free when Remove
// returns, so a server created again under the same id cannot collide with it.
func TestRemoveClearsBothContainersAndForgetsTheServer(t *testing.T) {
	const id = "srv-1"
	f := &fakeContainers{byName: map[string]string{
		containerName(id):        "c-run",
		installContainerName(id): "c-install",
		containerName("srv-2"):   "c-other",
	}}
	d := newRemoveRuntime(t, id, f)

	if err := d.Remove(context.Background(), id, true); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if strings.Join(f.removed, ",") != containerName(id)+","+installContainerName(id) {
		t.Errorf("removed %v, want the runtime container then the install one", f.removed)
	}
	if _, ok := f.byName[containerName("srv-2")]; !ok {
		t.Error("another server's container was removed")
	}
	if _, err := os.Stat(d.localDir(id)); !os.IsNotExist(err) {
		t.Errorf("data dir still there (stat err %v), want it deleted", err)
	}
	if _, err := os.Stat(d.specFile(id)); !os.IsNotExist(err) {
		t.Errorf("persisted spec still there (stat err %v): a restarted Agent would treat it as its own", err)
	}
	if _, ok := d.getSpec(id); ok {
		t.Error("spec still in memory")
	}
}

// Nothing to remove is not a failure: it is what a retried removal finds when
// the first attempt got further than it could report. Without this a pending
// removal would fail forever on a node that had already done the work.
func TestRemoveWithNoContainersIsSuccess(t *testing.T) {
	const id = "srv-1"
	d := newRemoveRuntime(t, id, &fakeContainers{byName: map[string]string{}})
	if err := d.Remove(context.Background(), id, false); err != nil {
		t.Fatalf("Remove with nothing on the daemon = %v, want nil", err)
	}
	// delete_data=false keeps the world, which is the orphan-retire promise.
	if _, err := os.Stat(filepath.Join(d.localDir(id), "world", "level.sav")); err != nil {
		t.Errorf("world touched without delete_data: %v", err)
	}
	if _, err := os.Stat(d.specFile(id)); !os.IsNotExist(err) {
		t.Errorf("persisted spec still there (stat err %v)", err)
	}
}

// A data dir the filesystem will not delete is reported — the incident was a
// Remove that swallowed every error and told the Panel it had worked. The
// message carries the logical /data path, never the node's host path.
func TestRemoveReturnsTheDataDeletionError(t *testing.T) {
	const id = "srv-1"
	d := newRemoveRuntime(t, id, &fakeContainers{byName: map[string]string{containerName(id): "c-run"}})
	host := d.localDir(id)
	d.removeAll = func(p string) error {
		return &os.PathError{Op: "unlinkat", Path: filepath.Join(p, "world", "level.sav"), Err: syscall.EACCES}
	}

	err := d.Remove(context.Background(), id, true)
	if err == nil {
		t.Fatal("Remove = nil, want the deletion failure")
	}
	if !errors.Is(err, syscall.EACCES) {
		t.Errorf("error %q does not carry its cause", err)
	}
	if !strings.Contains(err.Error(), "/data/world/level.sav") {
		t.Errorf("error %q does not name the logical path", err)
	}
	if strings.Contains(err.Error(), host) {
		t.Errorf("error %q leaks the host path %s", err, host)
	}
	// The containers were gone before the data was tried, so the server is
	// forgotten either way; a retry redoes only the part that failed.
	if _, serr := os.Stat(d.specFile(id)); !os.IsNotExist(serr) {
		t.Errorf("persisted spec still there (stat err %v)", serr)
	}
}

// A container that will not go stops the removal before anything else is
// touched: the server is still there, so its data and spec stay for the retry.
func TestRemoveStopsAtAContainerThatWillNotGo(t *testing.T) {
	const id = "srv-1"
	boom := errors.New("daemon is restarting")
	d := newRemoveRuntime(t, id, &fakeContainers{
		byName:    map[string]string{containerName(id): "c-run"},
		removeErr: boom,
	})

	err := d.Remove(context.Background(), id, true)
	if !errors.Is(err, boom) {
		t.Fatalf("Remove = %v, want the daemon's error", err)
	}
	if _, serr := os.Stat(filepath.Join(d.localDir(id), "world", "level.sav")); serr != nil {
		t.Errorf("data deleted under a container that is still there: %v", serr)
	}
	if _, serr := os.Stat(d.specFile(id)); serr != nil {
		t.Errorf("spec forgotten while its container still exists: %v", serr)
	}
}

func TestDataRemoveErrorNamesTheLogicalPath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "srv-1")
	for _, tc := range []struct {
		path string
		want string
	}{
		{root, "/data"},
		{filepath.Join(root, "a", "b.txt"), "/data/a/b.txt"},
		{filepath.Join(t.TempDir(), "elsewhere"), "/data"}, // never a path outside the root
	} {
		got := dataRemoveError(root, &os.PathError{Op: "remove", Path: tc.path, Err: syscall.EPERM}).Error()
		if !strings.HasPrefix(got, "remove "+tc.want+":") {
			t.Errorf("dataRemoveError(%s) = %q, want it to name %s", tc.path, got, tc.want)
		}
	}
	plain := errors.New("plain")
	if got := dataRemoveError(root, plain); got != plain {
		t.Errorf("a non-path error should pass through, got %v", got)
	}
}
