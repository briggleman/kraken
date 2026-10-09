package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// The fake's drill triggers (#348 phase 5): console commands that make the fake
// do what a real node does on its own, so every push alert can be produced by
// hand on the fake-live stack.

func startedFake(t *testing.T, query string) (*FakeRuntime, context.Context) {
	t.Helper()
	f := NewFakeRuntime("n1", "linux", false, "test")
	ctx := context.Background()
	spec := &agentpb.ServerSpec{ServerId: "s1"}
	if query != "" {
		spec.PlayerQuery = &agentpb.PlayerQuery{Method: query, MaxPlayers: 32}
	}
	if err := f.Create(ctx, spec); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Power(ctx, "s1", agentpb.PowerAction_POWER_ACTION_START); err != nil {
		t.Fatal(err)
	}
	return f, ctx
}

func rosterNames(t *testing.T, f *FakeRuntime, ctx context.Context) string {
	t.Helper()
	st, err := f.Status(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	ls := st.GetLastStats()
	if ls == nil || !ls.GetPlayersKnown() {
		return "unknown"
	}
	var names []string
	for _, p := range ls.GetOnlinePlayers() {
		names = append(names, p.GetName())
	}
	if int(ls.GetPlayers()) != len(names) {
		t.Fatalf("players %d but %d names", ls.GetPlayers(), len(names))
	}
	return strings.Join(names, ",")
}

func TestFakeDrillJoinAndLeave(t *testing.T) {
	f, ctx := startedFake(t, "log")
	if got := rosterNames(t, f, ctx); got != "Kestrel,MossVeil" {
		t.Fatalf("seeded roster = %q", got)
	}
	_ = f.SendCommand(ctx, "s1", "join Wren")
	_ = f.SendCommand(ctx, "s1", "join Wren") // a second join of the same name is one player
	if got := rosterNames(t, f, ctx); got != "Kestrel,MossVeil,Wren" {
		t.Fatalf("after join = %q", got)
	}
	_ = f.SendCommand(ctx, "s1", "leave Kestrel")
	if got := rosterNames(t, f, ctx); got != "MossVeil,Wren" {
		t.Fatalf("after leave = %q", got)
	}
	// A stopped server reports no roster, as the Docker runtime samples none.
	if _, err := f.Power(ctx, "s1", agentpb.PowerAction_POWER_ACTION_STOP); err != nil {
		t.Fatal(err)
	}
	if got := rosterNames(t, f, ctx); got != "unknown" {
		t.Fatalf("stopped server roster = %q", got)
	}
}

// A spec with no "log" query has no roster to report, whatever is typed.
func TestFakeDrillJoinNeedsALogQuery(t *testing.T) {
	f, ctx := startedFake(t, "")
	_ = f.SendCommand(ctx, "s1", "join Wren")
	if got := rosterNames(t, f, ctx); got != "unknown" {
		t.Fatalf("roster without a log query = %q", got)
	}
}

func TestFakeDrillBackupFail(t *testing.T) {
	f, ctx := startedFake(t, "")
	_ = f.SendCommand(ctx, "s1", "backupfail no space left on device")
	b, err := f.CreateBackup(ctx, "s1", "", "nightly", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if b.GetState() != agentpb.BackupState_BACKUP_STATE_FAILED || b.GetError() != "no space left on device" {
		t.Fatalf("backup under backupfail = %v %q", b.GetState(), b.GetError())
	}
	// Only that server's backups fail.
	if b, _ := f.CreateBackup(ctx, "s2", "", "nightly", nil, nil); b.GetState() == agentpb.BackupState_BACKUP_STATE_FAILED {
		t.Fatal("another server's backup failed too")
	}
	_ = f.SendCommand(ctx, "s1", "backupfail off")
	if b, _ := f.CreateBackup(ctx, "s1", "", "nightly", nil, nil); b.GetState() == agentpb.BackupState_BACKUP_STATE_FAILED {
		t.Fatal("backupfail off did not clear it")
	}
}

func TestFakeDrillRuntimeDownAndUp(t *testing.T) {
	f, ctx := startedFake(t, "")
	read := func() *agentpb.NodeInfo {
		t.Helper()
		info, err := f.NodeInfo(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return info
	}
	if info := read(); info.GetRuntimeStatus() != agentpb.RuntimeStatus_RUNTIME_STATUS_OK {
		t.Fatalf("healthy runtime = %v", info.GetRuntimeStatus())
	}
	_ = f.SendCommand(ctx, "s1", "runtime down")
	if info := read(); info.GetRuntimeStatus() != agentpb.RuntimeStatus_RUNTIME_STATUS_UNAVAILABLE || info.GetRuntimeError() == "" {
		t.Fatalf("runtime down = %v %q", info.GetRuntimeStatus(), info.GetRuntimeError())
	}
	_ = f.SendCommand(ctx, "s1", "runtime up")
	if info := read(); info.GetRuntimeStatus() != agentpb.RuntimeStatus_RUNTIME_STATUS_OK || info.GetRuntimeError() != "" {
		t.Fatalf("runtime up = %v %q", info.GetRuntimeStatus(), info.GetRuntimeError())
	}
}
