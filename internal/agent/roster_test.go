package agent

import (
	"testing"
	"time"

	"github.com/briggleman/kraken/internal/shared/agentpb"
)

// The Dragonwilds lines, verbatim from a live server (2026-09-14). The join and
// leave lines carry the same Account/Character/Guid triple, so the roster keys
// on the account id and shows the character name.
const (
	dwJoin  = `LogDominionPlayerController: RequestGameExit : Server saving World and Player state for Account\[(?P<id>[^\]]+)\] Character Name\[(?P<name>[^\]]*)\]`
	dwLeave = `LogDominionPlayerController: ClientRequestDisconnect : DisconnectMe : .*Account\[(?P<id>[^\]]+)\] Character Name\[(?P<name>[^\]]*)\]`

	dwJoinLine  = `[2026.09.14-14.02.11:512][123]LogDominionPlayerController: RequestGameExit : Server saving World and Player state for Account[XP:00023a5e93534de8a2868b4f4bf96f35] Character Name[GHETTO.CHiLD] Guid[DCG:4969D2EC4FBA4C0F7094F7A075757A4F] Type[0]`
	dwLeaveLine = `[2026.09.14-14.40.02:001][987]LogDominionPlayerController: ClientRequestDisconnect : DisconnectMe : PlayerStateSave result[true] - state saved for Account[XP:00023a5e93534de8a2868b4f4bf96f35] Character Name[GHETTO.CHiLD] Guid[DCG:4969D2EC4FBA4C0F7094F7A075757A4F] Type[0]`
)

func names(ps []*agentpb.OnlinePlayer) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.Name)
	}
	return out
}

func TestLogRoster_DragonwildsJoinLeave(t *testing.T) {
	r, err := newLogRoster(&agentpb.PlayerQuery{Method: "log", JoinRegex: dwJoin, LeaveRegex: dwLeave})
	if err != nil {
		t.Fatal(err)
	}
	r.reset()
	t0 := time.Date(2026, 9, 14, 14, 2, 11, 0, time.UTC)

	r.observe("LogNet: some unrelated line", t0)
	r.observe(dwJoinLine, t0)
	ps, alive := r.snapshot()
	if !alive || len(ps) != 1 || ps[0].Name != "GHETTO.CHiLD" || ps[0].JoinedUnixMs != t0.UnixMilli() {
		t.Fatalf("after join: alive=%v players=%v", alive, ps)
	}
	// The game re-prints the save line while the player is aboard; that is
	// not a second player, and it keeps the original arrival time.
	r.observe(dwJoinLine, t0.Add(10*time.Minute))
	if ps, _ = r.snapshot(); len(ps) != 1 || ps[0].JoinedUnixMs != t0.UnixMilli() {
		t.Fatalf("after repeated join: %v", ps)
	}
	// A second account, renamed later — same id, one entry, new name.
	second := `LogDominionPlayerController: RequestGameExit : Server saving World and Player state for Account[XP:0000aaaa] Character Name[Bramble] Guid[DCG:1] Type[0]`
	r.observe(second, t0.Add(time.Minute))
	r.observe(`LogDominionPlayerController: RequestGameExit : Server saving World and Player state for Account[XP:0000aaaa] Character Name[Bramble the Second] Guid[DCG:1] Type[0]`, t0.Add(2*time.Minute))
	ps, _ = r.snapshot()
	if got := names(ps); len(got) != 2 || got[0] != "GHETTO.CHiLD" || got[1] != "Bramble the Second" {
		t.Fatalf("after second account: %v", got)
	}

	r.observe(dwLeaveLine, t0.Add(38*time.Minute))
	if ps, _ = r.snapshot(); len(ps) != 1 || ps[0].Name != "Bramble the Second" {
		t.Fatalf("after leave: %v", names(ps))
	}
	// A leave for someone never seen is not an error and changes nothing.
	r.observe(`LogDominionPlayerController: ClientRequestDisconnect : DisconnectMe : PlayerStateSave result[true] - state saved for Account[XP:nobody] Character Name[Ghost] Guid[DCG:2] Type[0]`, t0)
	if ps, _ = r.snapshot(); len(ps) != 1 {
		t.Fatalf("after stranger leave: %v", names(ps))
	}

	// The container exits: nobody is aboard, and the count is unknown until a
	// follower reads the next run.
	r.down()
	if ps, alive = r.snapshot(); alive || len(ps) != 0 {
		t.Fatalf("after down: alive=%v players=%v", alive, ps)
	}
}

func TestLogRoster_NameOnlyKey(t *testing.T) {
	r, err := newLogRoster(&agentpb.PlayerQuery{
		Method: "log", JoinRegex: `Player (?P<name>\S+) joined`, LeaveRegex: `Player (?P<name>\S+) left`,
	})
	if err != nil {
		t.Fatal(err)
	}
	r.reset()
	now := time.Now()
	r.observe("Player kestrel joined", now)
	r.observe("Player moss joined", now.Add(time.Second))
	r.observe("Player kestrel left", now.Add(2*time.Second))
	ps, _ := r.snapshot()
	if got := names(ps); len(got) != 1 || got[0] != "moss" {
		t.Fatalf("got %v", got)
	}
}

func TestSplitLogTimestamp(t *testing.T) {
	at, line := splitLogTimestamp("2026-09-14T14:02:11.512345678Z LogNet: hello")
	if line != "LogNet: hello" || at.Year() != 2026 || at.Minute() != 2 {
		t.Fatalf("got %v %q", at, line)
	}
	before := time.Now()
	at, line = splitLogTimestamp("LogNet: no stamp")
	if line != "LogNet: no stamp" || at.Before(before) {
		t.Fatalf("got %v %q", at, line)
	}
}
