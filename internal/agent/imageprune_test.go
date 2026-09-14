package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
)

// ImagesPrune completes the imageAPI seam (see images_test.go for the rest).
func (f *fakeImages) ImagesPrune(_ context.Context, pruneFilter filters.Args) (image.PruneReport, error) {
	f.pruneFilters = append(f.pruneFilters, pruneFilter)
	if f.pruneErr != nil {
		return image.PruneReport{}, f.pruneErr
	}
	return f.pruneReport, nil
}

// newPruneRuntime builds a runtime with just the fields the pruner touches.
// runtimeOK is true because pruneIfDue skips a node whose daemon is down.
func newPruneRuntime(t *testing.T, f *fakeImages, enabled bool) (*DockerRuntime, string) {
	t.Helper()
	stateDir := t.TempDir()
	d := &DockerRuntime{images: f, imagePrune: enabled, pruneClock: newPruneClock(stateDir)}
	d.runtimeOK = true
	return d, stateDir
}

func TestPruneIfDuePassesDanglingFilter(t *testing.T) {
	f := &fakeImages{pruneReport: image.PruneReport{
		ImagesDeleted:  []image.DeleteResponse{{Deleted: "sha256:aaa"}, {Deleted: "sha256:bbb"}},
		SpaceReclaimed: 4 << 30,
	}}
	d, stateDir := newPruneRuntime(t, f, true)

	d.pruneIfDue(context.Background())

	if len(f.pruneFilters) != 1 {
		t.Fatalf("expected exactly one prune, got %d", len(f.pruneFilters))
	}
	// Dangling only: an image any container still references — running or merely
	// stopped — is not in this set, and the daemon would refuse it anyway.
	got := f.pruneFilters[0].Get("dangling")
	if len(got) != 1 || got[0] != "true" {
		t.Errorf(`prune filter should be dangling=true, got %v`, got)
	}
	if _, err := os.Stat(filepath.Join(stateDir, imagePruneStateName)); err != nil {
		t.Errorf("a completed prune must persist its timestamp: %v", err)
	}
}

func TestPruneIfDueDisabled(t *testing.T) {
	f := &fakeImages{}
	d, _ := newPruneRuntime(t, f, false)
	d.pruneIfDue(context.Background())
	if len(f.pruneFilters) != 0 {
		t.Errorf("KRAKEN_IMAGE_PRUNE=off must not prune, got %d calls", len(f.pruneFilters))
	}
}

func TestPruneIfDueNotYetDue(t *testing.T) {
	f := &fakeImages{}
	d, _ := newPruneRuntime(t, f, true)
	d.pruneClock.stamp(time.Now().Add(-6 * 24 * time.Hour))

	d.pruneIfDue(context.Background())

	if len(f.pruneFilters) != 0 {
		t.Errorf("a prune six days old is not due yet, got %d calls", len(f.pruneFilters))
	}
}

func TestPruneIfDueAfterAWeek(t *testing.T) {
	f := &fakeImages{}
	d, _ := newPruneRuntime(t, f, true)
	stale := time.Now().Add(-8 * 24 * time.Hour)
	d.pruneClock.stamp(stale)

	d.pruneIfDue(context.Background())

	if len(f.pruneFilters) != 1 {
		t.Fatalf("a prune eight days old is due, got %d calls", len(f.pruneFilters))
	}
	last, ok := d.pruneClock.last()
	if !ok {
		t.Fatal("the clock should still be readable after a prune")
	}
	if !last.After(stale) {
		t.Errorf("the clock was not advanced: %v is not after %v", last, stale)
	}
}

func TestPruneClockUnparseableTimestampMeansNeverRun(t *testing.T) {
	f := &fakeImages{}
	d, stateDir := newPruneRuntime(t, f, true)
	if err := os.WriteFile(filepath.Join(stateDir, imagePruneStateName), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := d.pruneClock.last(); ok {
		t.Fatal("a corrupt clock file must read as never-pruned")
	}

	d.pruneIfDue(context.Background())

	if len(f.pruneFilters) != 1 {
		t.Errorf("a corrupt clock should prune (and rewrite the file), got %d calls", len(f.pruneFilters))
	}
	if _, ok := d.pruneClock.last(); !ok {
		t.Error("the prune should have replaced the corrupt file with a usable one")
	}
}

func TestPruneFailureLeavesTheClockAlone(t *testing.T) {
	f := &fakeImages{pruneErr: errors.New("daemon is busy")}
	d, _ := newPruneRuntime(t, f, true)

	d.pruneIfDue(context.Background())

	if len(f.pruneFilters) != 1 {
		t.Fatalf("expected one attempt, got %d", len(f.pruneFilters))
	}
	if _, ok := d.pruneClock.last(); ok {
		t.Error("a failed prune must not stamp the clock — the next hourly check should retry")
	}
}

func TestPruneSkipsWhenTheDaemonIsUnreachable(t *testing.T) {
	f := &fakeImages{}
	d, _ := newPruneRuntime(t, f, true)
	d.runtimeOK = false

	d.pruneIfDue(context.Background())

	if len(f.pruneFilters) != 0 {
		t.Errorf("a prune against an unreachable daemon can only fail; it should be skipped, got %d calls", len(f.pruneFilters))
	}
}
