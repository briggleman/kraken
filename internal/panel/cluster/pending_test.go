package cluster

import (
	"testing"
	"time"
)

// A server is owed to its node at most once: a second delete of the same id
// (a retry after a 500, say) folds into the first rather than queueing a
// duplicate that would be replayed twice.
func TestPendingRemovalsDedupeByServer(t *testing.T) {
	first := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	n := &Node{}
	n.AddPendingRemoval(PendingRemoval{ServerID: "a", DeleteData: false, RequestedAt: first, Attempts: 1, LastError: "node unreachable"})
	n.AddPendingRemoval(PendingRemoval{ServerID: "b", DeleteData: true, RequestedAt: first, Attempts: 1})
	n.AddPendingRemoval(PendingRemoval{ServerID: "a", DeleteData: true, RequestedAt: first.Add(time.Hour), Attempts: 1, LastError: "docker down"})

	if len(n.PendingRemovals) != 2 {
		t.Fatalf("pending = %+v, want one entry per server", n.PendingRemovals)
	}
	a := n.PendingRemovals[0]
	if a.ServerID != "a" || !a.DeleteData || a.Attempts != 2 || a.LastError != "docker down" || !a.RequestedAt.Equal(first) {
		t.Errorf("folded entry = %+v, want the latest intent and reason, both attempts, the first request time", a)
	}

	if !n.DropPendingRemoval("a") {
		t.Fatal("DropPendingRemoval(a) = false, want true")
	}
	if n.DropPendingRemoval("a") {
		t.Fatal("dropping it twice reported a second removal")
	}
	if len(n.PendingRemovals) != 1 || n.PendingRemovals[0].ServerID != "b" {
		t.Fatalf("after drop: %+v, want only b", n.PendingRemovals)
	}
	n.DropPendingRemoval("b")
	if n.PendingRemovals != nil {
		t.Errorf("an emptied list = %#v, want nil so the JSON field is omitted", n.PendingRemovals)
	}
}
