package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/image"
)

// shortBudget shrinks the start-path wait so these tests run in milliseconds
// rather than the eight seconds a real start is willing to wait.
func shortBudget(t *testing.T) {
	t.Helper()
	prev := startPullBudget
	startPullBudget = 50 * time.Millisecond
	t.Cleanup(func() { startPullBudget = prev })
}

// waitFor polls until cond holds or the deadline passes — the background pull
// finishes on its own goroutine, so there is nothing to join.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

const oldID = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func freshImage() *image.InspectResponse {
	return &image.InspectResponse{
		ID:          "sha256:" + strings.Repeat("b", 64),
		RepoDigests: []string{testRef + "@sha256:" + strings.Repeat("c", 64)},
	}
}

func TestStartRefreshUsesTheNewImageWhenThePullIsQuick(t *testing.T) {
	// The common case: an unchanged moving tag is a manifest check, so the start
	// proceeds on the refreshed image exactly as a synchronous pull would.
	shortBudget(t)
	f := &fakeImages{
		local:  map[string]image.InspectResponse{testRef: {ID: oldID}},
		pulled: freshImage(),
	}
	d := newPullRuntime(t, pullAlways, f)

	if err := d.refreshImageForStart(context.Background(), testRef, "srv-1"); err != nil {
		t.Fatalf("refreshImageForStart: %v", err)
	}
	if f.pullCount() != 1 {
		t.Fatalf("expected one pull, got %d", f.pullCount())
	}
	// The container is recreated after this returns, so what matters is that the
	// new image is the local one by now.
	got, err := f.ImageInspect(context.Background(), testRef)
	if err != nil {
		t.Fatalf("ImageInspect: %v", err)
	}
	if got.ID == oldID {
		t.Error("the start did not end up on the refreshed image")
	}
}

func TestStartRefreshDetachesAPullThatOverrunsTheBudget(t *testing.T) {
	// A tag that actually moved: the Panel bounds the Power RPC, so the start
	// must not wait for the transfer. It proceeds on the local image and the
	// download continues in the background.
	shortBudget(t)
	gate := make(chan struct{})
	f := &fakeImages{
		local:  map[string]image.InspectResponse{testRef: {ID: oldID}},
		pulled: freshImage(),
		gate:   gate,
	}
	d := newPullRuntime(t, pullAlways, f)

	start := time.Now()
	if err := d.refreshImageForStart(context.Background(), testRef, "srv-1"); err != nil {
		t.Fatalf("a start with a local image must not fail because the pull is slow: %v", err)
	}
	if waited := time.Since(start); waited > time.Second {
		t.Errorf("the start waited %v; it should give up on the budget", waited)
	}
	if f.pullCount() != 1 {
		t.Fatalf("expected one pull, got %d", f.pullCount())
	}

	// The pull was left running, not cancelled: it completes once unblocked, and
	// the node then holds the new image for the next start.
	close(gate)
	waitFor(t, "the background pull to land the new image", func() bool {
		got, err := f.ImageInspect(context.Background(), testRef)
		return err == nil && got.ID != oldID
	})
}

func TestStartRefreshJoinsAnInFlightPull(t *testing.T) {
	// A second START while a multi-GB transfer is running must wait on the same
	// pull, not launch a competing one.
	shortBudget(t)
	gate := make(chan struct{})
	f := &fakeImages{
		local:  map[string]image.InspectResponse{testRef: {ID: oldID}},
		pulled: freshImage(),
		gate:   gate,
	}
	d := newPullRuntime(t, pullAlways, f)

	if err := d.refreshImageForStart(context.Background(), testRef, "srv-1"); err != nil {
		t.Fatalf("first start: %v", err)
	}
	if err := d.refreshImageForStart(context.Background(), testRef, "srv-2"); err != nil {
		t.Fatalf("second start: %v", err)
	}
	if f.pullCount() != 1 {
		t.Fatalf("the second start started its own pull: %d pulls", f.pullCount())
	}

	close(gate)
	// Wait for the pull to be deregistered, not merely for the new image to be
	// visible: the fake lands the image inside doPull, before backgroundPull
	// drops the in-flight record, so a start in between would still join it.
	waitFor(t, "the shared pull to finish", func() bool {
		d.pullMu.Lock()
		_, inFlight := d.pulls[testRef]
		d.pullMu.Unlock()
		return !inFlight
	})

	// Once it has finished, a later start joins nothing and pulls afresh.
	if err := d.refreshImageForStart(context.Background(), testRef, "srv-1"); err != nil {
		t.Fatalf("third start: %v", err)
	}
	if f.pullCount() != 2 {
		t.Errorf("a start after the pull completed should pull again, got %d", f.pullCount())
	}
}

func TestStartRefreshFailsFastWithNoLocalImage(t *testing.T) {
	// Nothing to start from and the download is not done: fail with one clear
	// sentence rather than hang the RPC until the Panel's deadline.
	shortBudget(t)
	gate := make(chan struct{})
	defer close(gate)
	f := &fakeImages{pulled: freshImage(), gate: gate}
	d := newPullRuntime(t, pullAlways, f)

	err := d.refreshImageForStart(context.Background(), testRef, "srv-1")
	if err == nil {
		t.Fatal("expected an error when the node has no copy of the image yet")
	}
	if !strings.Contains(err.Error(), "not on this node yet") || !strings.Contains(err.Error(), "background") {
		t.Errorf("the error should tell the operator what to do, got %v", err)
	}
}

func TestStartRefreshPullSurvivesTheRPCContext(t *testing.T) {
	// The Power RPC's context dying (the Panel's 15s START deadline, or the
	// operator navigating away) must not cancel a transfer already under way.
	shortBudget(t)
	gate := make(chan struct{})
	f := &fakeImages{
		local:  map[string]image.InspectResponse{testRef: {ID: oldID}},
		pulled: freshImage(),
		gate:   gate,
	}
	d := newPullRuntime(t, pullAlways, f)

	ctx, cancel := context.WithCancel(context.Background())
	if err := d.refreshImageForStart(ctx, testRef, "srv-1"); err != nil {
		t.Fatalf("refreshImageForStart: %v", err)
	}
	cancel() // the RPC is over; the pull is not

	close(gate)
	waitFor(t, "the detached pull to finish", func() bool {
		got, err := f.ImageInspect(context.Background(), testRef)
		return err == nil && got.ID != oldID
	})

	f.mu.Lock()
	ctxErr := f.pullCtxErr
	f.mu.Unlock()
	if ctxErr != nil {
		t.Errorf("the detached pull saw a cancelled context (%v) — it is not detached", ctxErr)
	}
}

func TestStartRefreshPolicyNeverDoesNotPull(t *testing.T) {
	f := &fakeImages{local: map[string]image.InspectResponse{testRef: {ID: oldID}}}
	d := newPullRuntime(t, pullNever, f)
	if err := d.refreshImageForStart(context.Background(), testRef, "srv-1"); err != nil {
		t.Fatalf("refreshImageForStart: %v", err)
	}
	if f.pullCount() != 0 {
		t.Errorf(`"never" must not contact the registry on start either, got %d`, f.pullCount())
	}
}

func TestStartRefreshStopsWaitingWhenTheRPCEnds(t *testing.T) {
	// The Panel's deadline passing must end the wait at once. Before, the wait
	// ignored ctx and held the start for the whole pull budget, past a deadline
	// nobody was listening to any more.
	prev := startPullBudget
	startPullBudget = 10 * time.Second
	t.Cleanup(func() { startPullBudget = prev })
	gate := make(chan struct{})
	defer close(gate)
	f := &fakeImages{
		local:  map[string]image.InspectResponse{testRef: {ID: oldID}},
		pulled: freshImage(),
		gate:   gate,
	}
	d := newPullRuntime(t, pullAlways, f)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := d.refreshImageForStart(ctx, testRef, "srv-1")
	if waited := time.Since(start); waited > 2*time.Second {
		t.Fatalf("the refresh waited %v after its context ended", waited)
	}
	if err == nil || !strings.Contains(err.Error(), context.DeadlineExceeded.Error()) {
		t.Errorf("want the context's error, got %v", err)
	}
}
