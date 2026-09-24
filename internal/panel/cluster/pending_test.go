package cluster

import (
	"testing"
	"time"
)

// A server is owed to its node at most once: a second delete of the same id
// (a retry after a 500, say) folds into the first rather than queueing a
// duplicate that would be replayed twice — or release its allocation twice.
func TestPendingRemovalsDedupeByServer(t *testing.T) {
	first := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	n := &Node{}
	n.AddPendingRemoval(PendingRemoval{ServerID: "a", DeleteData: false, RequestedAt: first, Attempts: 1, LastError: "node unreachable", MemoryMB: 1024, Ports: []int{28000}})
	n.AddPendingRemoval(PendingRemoval{ServerID: "b", DeleteData: true, RequestedAt: first, Attempts: 1})
	n.AddPendingRemoval(PendingRemoval{ServerID: "a", DeleteData: true, RequestedAt: first.Add(time.Hour), Attempts: 1, LastError: "docker down", MemoryMB: 1024, Ports: []int{28000}})

	if len(n.PendingRemovals) != 2 {
		t.Fatalf("pending = %+v, want one entry per server", n.PendingRemovals)
	}
	a := n.PendingRemovals[0]
	if a.ServerID != "a" || !a.DeleteData || a.Attempts != 2 || a.LastError != "docker down" || !a.RequestedAt.Equal(first) {
		t.Errorf("folded entry = %+v, want the latest intent and reason, both attempts, the first request time", a)
	}
	if a.MemoryMB != 1024 || len(a.Ports) != 1 {
		t.Errorf("folded entry allocation = %d MB %v, want the one allocation it holds", a.MemoryMB, a.Ports)
	}
}

// The allocation a pending removal carries is released when the removal is
// finished, exactly once, and not before.
func TestFinishPendingRemovalReleasesOnce(t *testing.T) {
	n := &Node{TotalMemoryMB: 8192, Ports: NewPortPool(PortRange{Start: 28000, End: 28010})}
	ports, err := n.Reserve(2048, []PortRequest{{Name: "game", Preferred: 28000}})
	if err != nil {
		t.Fatal(err)
	}
	n.AddPendingRemoval(PendingRemoval{ServerID: "a", MemoryMB: 2048, Ports: []int{ports["game"]}})
	if n.AllocatedMemoryMB != 2048 || n.Ports.IsFree(28000) {
		t.Fatal("recording a pending removal released its allocation")
	}

	if !n.FinishPendingRemoval("a") {
		t.Fatal("FinishPendingRemoval(a) = false, want true")
	}
	if n.AllocatedMemoryMB != 0 || !n.Ports.IsFree(28000) {
		t.Fatalf("after finish: %d MB allocated, 28000 free=%v — want both released", n.AllocatedMemoryMB, n.Ports.IsFree(28000))
	}
	// A port handed to someone else in the meantime must not be freed again.
	if _, err := n.Reserve(512, []PortRequest{{Name: "game", Preferred: 28000}}); err != nil {
		t.Fatal(err)
	}
	if n.FinishPendingRemoval("a") {
		t.Fatal("finishing it twice reported a second removal")
	}
	if n.Ports.IsFree(28000) || n.AllocatedMemoryMB != 512 {
		t.Fatal("a second finish released someone else's allocation")
	}
	if n.PendingRemovals != nil {
		t.Errorf("an emptied list = %#v, want nil so the JSON field is omitted", n.PendingRemovals)
	}
}

// A failed retry is counted and pushes the next one back: 20s, doubling, an
// hour at most. The caller is told whether the reason changed.
func TestRecordRemovalFailureBacksOff(t *testing.T) {
	for attempts, want := range map[int]time.Duration{
		1: 20 * time.Second, 2: 40 * time.Second, 3: 80 * time.Second,
		8: 2560 * time.Second, 9: time.Hour, 50: time.Hour,
	} {
		if got := RemovalBackoff(attempts); got != want {
			t.Errorf("RemovalBackoff(%d) = %v, want %v", attempts, got, want)
		}
	}

	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	n := &Node{}
	n.AddPendingRemoval(PendingRemoval{ServerID: "a", Attempts: 1, LastError: "node unreachable", NextAttempt: now.Add(20 * time.Second)})
	p, _ := n.PendingRemovalFor("a")
	if p.Due(now) || !p.Due(now.Add(20*time.Second)) {
		t.Fatal("Due does not honour NextAttempt")
	}
	if !n.RecordRemovalFailure("a", "docker down", now) {
		t.Error("a new reason was not reported as a change")
	}
	if n.RecordRemovalFailure("a", "docker down", now) {
		t.Error("the same reason was reported as a change")
	}
	p, _ = n.PendingRemovalFor("a")
	if p.Attempts != 3 || !p.NextAttempt.Equal(now.Add(80*time.Second)) {
		t.Errorf("after two more failures: %+v, want 3 attempts and the next one 80s out", p)
	}
}
