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

// The build check (#392) round-trips through its own writer, and the row's
// ordinary writer cannot undo it: a stale copy of the server written back with
// UpdateServer keeps the check that landed after the copy was read.
func TestServerBuildRoundTripAndUpdateServerLeavesIt(t *testing.T) {
	st := memory.New()
	ctx := context.Background()
	if err := st.CreateServer(ctx, &store.Server{ID: "sv", Name: "a", State: store.StateOffline}); err != nil {
		t.Fatal(err)
	}
	stale, _ := st.GetServer(ctx, "sv")

	at := time.Unix(1759921187, 0).UTC()
	checked := time.Now().UTC()
	want := store.ServerBuild{InstalledBuild: "100", AvailableBuild: "101", AvailableBuildAt: &at, CheckedAt: &checked, CheckError: "x"}
	if err := st.UpdateServerBuild(ctx, "sv", want); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetServer(ctx, "sv")
	if got.Build.InstalledBuild != "100" || got.Build.AvailableBuild != "101" || !got.Build.AvailableBuildAt.Equal(at) ||
		!got.Build.CheckedAt.Equal(checked) || got.Build.CheckError != "x" {
		t.Fatalf("build did not round-trip: %+v", got.Build)
	}
	// A reader's copy is its own.
	*got.Build.CheckedAt = time.Time{}
	if again, _ := st.GetServer(ctx, "sv"); again.Build.CheckedAt.IsZero() {
		t.Fatal("editing a read server's build changed the stored one")
	}

	stale.Name = "renamed"
	stale.Build = store.ServerBuild{InstalledBuild: "stale"}
	if err := st.UpdateServer(ctx, stale); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetServer(ctx, "sv")
	if got.Name != "renamed" || got.Build.InstalledBuild != "100" {
		t.Fatalf("UpdateServer: name %q build %+v; want the rename and the check kept", got.Name, got.Build)
	}

	if err := st.UpdateServerBuild(ctx, "missing", want); err != store.ErrNotFound {
		t.Fatalf("UpdateServerBuild on a missing server = %v, want ErrNotFound", err)
	}
}

// Push-alert devices (#348) round-trip, a refresh keeps the device's own
// choices while a takeover by another user starts afresh, a dead token is
// marked only while it is still the device's token, and deleting a user takes
// their devices with them — the cascade Postgres gets from its foreign key.
func TestDeviceRoundTrip(t *testing.T) {
	st := memory.New()
	ctx := context.Background()
	t0 := time.Now().UTC()
	for _, u := range []string{"ann", "bob"} {
		if err := st.CreateUser(ctx, &store.User{ID: u, Username: u, RoleID: "owner", CreatedAt: t0}); err != nil {
			t.Fatal(err)
		}
	}
	reg := func(id, user, token string, at time.Time) *store.Device {
		t.Helper()
		d, err := st.UpsertDevice(ctx, &store.Device{
			ID: id, UserID: user, Platform: store.DevicePlatformIOS, APNsToken: token,
			APNsEnvironment: store.APNsProduction, PublicKey: make([]byte, 32), Name: "phone",
			Rules: store.DefaultDeviceRules(), CreatedAt: at, LastSeenAt: at,
		})
		if err != nil {
			t.Fatalf("UpsertDevice: %v", err)
		}
		return d
	}

	reg("d1", "ann", "aa", t0)
	reg("d2", "ann", "cc", t0.Add(time.Second))
	muted := store.DeviceRules{Attend: true, AliveMutedServers: []string{"s1"}}
	if err := st.UpdateDeviceRules(ctx, "d1", muted); err != nil {
		t.Fatal(err)
	}
	sent := t0.Add(time.Minute)
	if err := st.TouchDeviceSent(ctx, "d1", sent); err != nil {
		t.Fatal(err)
	}
	if ok, _ := st.MarkDeviceTokenInvalid(ctx, "d1", "stale", t0); ok {
		t.Fatal("a token that is no longer the device's was marked dead")
	}
	if ok, _ := st.MarkDeviceTokenInvalid(ctx, "d1", "aa", t0); !ok {
		t.Fatal("the device's own token was not marked dead")
	}

	// A refresh by the owner: new token, rules and history kept, mark cleared.
	t1 := t0.Add(time.Hour)
	d := reg("d1", "ann", "bb", t1)
	if d.APNsToken != "bb" || d.TokenInvalidAt != nil || !d.LastSeenAt.Equal(t1) || !d.CreatedAt.Equal(t0) ||
		d.Rules.Alive || len(d.Rules.AliveMutedServers) != 1 || d.LastSentAt == nil || !d.LastSentAt.Equal(sent) {
		t.Fatalf("refresh = %+v", d)
	}
	// A reader's copy is its own.
	d.Rules.AliveMutedServers[0] = "x"
	d.PublicKey[0] = 9
	if again, _ := st.GetDevice(ctx, "d1"); again.Rules.AliveMutedServers[0] != "s1" || again.PublicKey[0] != 0 {
		t.Fatal("editing a read device changed the stored one")
	}

	if ds, _ := st.ListDevicesByUser(ctx, "ann"); len(ds) != 2 || ds[0].ID != "d1" || ds[1].ID != "d2" {
		t.Fatalf("ann's devices = %+v, want d1 then d2", ds)
	}

	// Another user's registration of the same install takes it over afresh.
	d = reg("d1", "bob", "dd", t1)
	if d.UserID != "bob" || !d.Rules.Alive || len(d.Rules.AliveMutedServers) != 0 || d.LastSentAt != nil || !d.CreatedAt.Equal(t1) {
		t.Fatalf("takeover = %+v", d)
	}
	if all, _ := st.ListDevices(ctx); len(all) != 2 {
		t.Fatalf("all devices = %d, want 2", len(all))
	}

	if err := st.DeleteUser(ctx, "ann"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetDevice(ctx, "d2"); err != store.ErrNotFound {
		t.Fatalf("a deleted user's device: %v, want ErrNotFound", err)
	}
	if n, _ := st.DeleteDevicesByUser(ctx, "bob"); n != 1 {
		t.Fatalf("DeleteDevicesByUser = %d, want 1", n)
	}
	if err := st.DeleteDevice(ctx, "d1"); err != store.ErrNotFound {
		t.Fatalf("DeleteDevice of a gone device = %v", err)
	}
	if err := st.UpdateDeviceRules(ctx, "d1", muted); err != store.ErrNotFound {
		t.Fatalf("UpdateDeviceRules of a gone device = %v", err)
	}
	if err := st.TouchDeviceSent(ctx, "d1", sent); err != store.ErrNotFound {
		t.Fatalf("TouchDeviceSent of a gone device = %v", err)
	}
}

// The install id is minted once and is the same on every read.
func TestPanelIDIsStable(t *testing.T) {
	st := memory.New()
	a, err := st.PanelID(context.Background())
	if err != nil || a == "" {
		t.Fatalf("PanelID = %q, %v", a, err)
	}
	if b, _ := st.PanelID(context.Background()); b != a {
		t.Fatalf("PanelID changed: %q then %q", a, b)
	}
}
