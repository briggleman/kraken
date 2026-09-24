package api

import (
	"testing"

	"github.com/briggleman/kraken/internal/panel/store"
)

// A retire lets go of the server just after it writes `retired`. A revive or a
// permanent delete landing in between is told the truth — the retire is
// finishing, retry — rather than that the server is busy with a retire that,
// as far as the row says, is over (#360 review).
func TestHoldRetiredRow_ARetireThatIsFinishing(t *testing.T) {
	s := &Server{restores: newRestoreJobs()}
	if held := s.restores.holdOp("sv", opRetire); held != "" {
		t.Fatalf("setup: %q", held)
	}
	for _, op := range []string{opRevive, opDelete} {
		r := holdRetiredRow(s, &store.Server{ID: "sv", State: store.StateRetired}, op)
		if r == nil || r.code != codeServerBusy || r.msg != "the retire is finishing — retry in a moment" {
			t.Fatalf("%s while the retire finishes: %+v", op, r)
		}
	}
	if r := holdRetiredRow(s, &store.Server{ID: "sv", State: store.StateRetiring}, opRevive); r == nil || r.msg != "this server is being retired" {
		t.Fatalf("revive mid-retire: %+v", r)
	}
	s.restores.releaseOp("sv")
	if r := holdRetiredRow(s, &store.Server{ID: "sv", State: store.StateRetired}, opDelete); r != nil {
		t.Fatalf("once let go: %+v", r)
	}
	if r := holdRetiredRow(s, &store.Server{ID: "sv", State: store.StateRetired}, opRevive); r == nil || r.msg != "this server is already being deleted" {
		t.Fatalf("revive while a delete holds it: %+v", r)
	}
}
