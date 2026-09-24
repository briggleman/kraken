package memory_test

import (
	"context"
	"testing"
	"time"

	"github.com/briggleman/kraken/internal/panel/store"
	"github.com/briggleman/kraken/internal/panel/store/memory"
)

// A deleted server takes its schedules with it, and nobody else's: a schedule
// left behind fires forever and fails with "load server" (#354).
func TestDeleteServerTakesItsSchedules(t *testing.T) {
	st := memory.New()
	ctx := context.Background()
	now := time.Now()

	for _, id := range []string{"gone", "kept"} {
		if err := st.CreateServer(ctx, &store.Server{ID: id, Name: id, State: store.StateOffline, CreatedAt: now}); err != nil {
			t.Fatalf("CreateServer: %v", err)
		}
	}
	for i, sid := range []string{"gone", "gone", "kept"} {
		task := &store.ScheduledTask{ID: "sched-" + string(rune('a'+i)), ServerID: sid, Action: store.ScheduleRestart, Cron: "0 4 * * *", Enabled: true, CreatedAt: now}
		if err := st.CreateSchedule(ctx, task); err != nil {
			t.Fatalf("CreateSchedule: %v", err)
		}
	}

	if err := st.DeleteServer(ctx, "gone"); err != nil {
		t.Fatalf("DeleteServer: %v", err)
	}
	if left, _ := st.ListSchedulesByServer(ctx, "gone"); len(left) != 0 {
		t.Fatalf("schedules of the deleted server = %d, want none", len(left))
	}
	if left, _ := st.ListSchedulesByServer(ctx, "kept"); len(left) != 1 {
		t.Fatalf("schedules of another server = %d, want its one", len(left))
	}
	if err := st.DeleteServer(ctx, "gone"); err != store.ErrNotFound {
		t.Fatalf("second DeleteServer = %v, want ErrNotFound", err)
	}
}

// A caller that edits the retire block, the retired stamp or the remembered
// ports of a server it read must not be editing the stored row: the retire job
// writes its phase while a request marshals the same server (#360).
func TestGetServerCopiesTheRetireFields(t *testing.T) {
	st := memory.New()
	ctx := context.Background()
	at := time.Now()
	if err := st.CreateServer(ctx, &store.Server{
		ID: "sv", State: store.StateRetired, RetiredAt: &at,
		Retire:       &store.ServerRetire{Phase: store.RetirePhaseStopping},
		RetiredPorts: map[string]int{"game": 27015},
	}); err != nil {
		t.Fatal(err)
	}
	a, _ := st.GetServer(ctx, "sv")
	a.Retire.Phase = store.RetirePhaseRemoving
	a.RetiredPorts["game"] = 1
	*a.RetiredAt = time.Time{}
	b, _ := st.GetServer(ctx, "sv")
	if b.Retire.Phase != store.RetirePhaseStopping || b.RetiredPorts["game"] != 27015 || b.RetiredAt.IsZero() {
		t.Fatalf("editing a read server changed the stored one: %+v", b)
	}
}
