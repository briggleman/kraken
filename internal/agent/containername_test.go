package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
)

// The delay is what makes these fast: the policy is exercised with a 0 delay so
// the attempt budget, not the clock, decides how long a test runs.
const testDelay = 0

func TestAwaitNameFree_ReturnsAsSoonAsTheNameIsFree(t *testing.T) {
	calls := 0
	err := awaitNameFree(context.Background(), func(context.Context) (bool, error) {
		calls++
		return false, nil
	}, 16, testDelay)
	if err != nil {
		t.Fatalf("want nil, got %v", err)
	}
	// The common case is a removal that already landed. One probe, no sleep.
	if calls != 1 {
		t.Fatalf("want a single probe for an already-free name, got %d", calls)
	}
}

func TestAwaitNameFree_WaitsOutASlowRemoval(t *testing.T) {
	calls := 0
	err := awaitNameFree(context.Background(), func(context.Context) (bool, error) {
		calls++
		return calls < 4, nil // free on the fourth look
	}, 16, testDelay)
	if err != nil {
		t.Fatalf("want nil, got %v", err)
	}
	if calls != 4 {
		t.Fatalf("want 4 probes, got %d", calls)
	}
}

func TestAwaitNameFree_GivesUpAsAStuckName(t *testing.T) {
	calls := 0
	err := awaitNameFree(context.Background(), func(context.Context) (bool, error) {
		calls++
		return true, nil // never frees
	}, 5, testDelay)
	if !errors.Is(err, errNameStillTaken) {
		t.Fatalf("want errNameStillTaken, got %v", err)
	}
	// Exactly the budget — not one probe more, and not an infinite loop.
	if calls != 5 {
		t.Fatalf("want 5 probes, got %d", calls)
	}
}

func TestAwaitNameFree_SurfacesAProbeError(t *testing.T) {
	boom := errors.New("daemon went away")
	err := awaitNameFree(context.Background(), func(context.Context) (bool, error) {
		return false, boom
	}, 16, testDelay)
	// A daemon that cannot answer must not read as "the name is free" — that is
	// how the create conflict happened in the first place.
	if !errors.Is(err, boom) {
		t.Fatalf("want the probe's error, got %v", err)
	}
}

func TestAwaitNameFree_HonoursContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	err := awaitNameFree(ctx, func(context.Context) (bool, error) {
		calls++
		cancel() // cancelled while the name is still held
		return true, nil
	}, 16, time.Second)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("want to stop at the first probe, got %d", calls)
	}
}

func TestIsNameConflict(t *testing.T) {
	// The live failure: "Conflict. The container name "/kraken_<id>" is already
	// in use by container "<hash>"." Docker returns 409 for it.
	conflict := cerrdefs.ErrConflict.WithMessage(
		`Conflict. The container name "/kraken_f4030778" is already in use by container "af3813a2".`)
	if !isNameConflict(conflict) {
		t.Fatal("a daemon conflict must be recognised as a name conflict")
	}
	if isNameConflict(nil) {
		t.Fatal("nil is not a conflict")
	}
	if isNameConflict(errors.New("some other failure")) {
		t.Fatal("an unrelated error must not be treated as a name conflict — retrying would hide it")
	}
	if isNameConflict(cerrdefs.ErrNotFound.WithMessage("no such container")) {
		t.Fatal("not-found is not a conflict")
	}
}

func TestIsNotFound(t *testing.T) {
	if !isNotFound(cerrdefs.ErrNotFound.WithMessage("no such container")) {
		t.Fatal("a daemon not-found must be recognised")
	}
	if isNotFound(nil) {
		t.Fatal("nil is not a not-found")
	}
	// A removal that fails for a real reason must NOT be swallowed as "already
	// gone" — that is exactly what left the name taken.
	if isNotFound(errors.New("permission denied")) {
		t.Fatal("an unrelated error must not read as not-found")
	}
}
