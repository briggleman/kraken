package alerts

import (
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/briggleman/kraken/internal/panel/push"
)

func TestExplainExit(t *testing.T) {
	cases := []struct {
		code int64
		want string
	}{
		{3221225781, "exit 0xC0000135, a DLL the game needs is missing"},
		{3221225785, "exit 0xC0000139, a DLL is the wrong build (an export the game wants is missing)"},
		{3221225477, "exit 0xC0000005, an access violation (the game crashed on a bad memory access)"},
		{3221225595, "exit 0xC000007B, bad image format (a 32/64-bit mismatch between the game and a DLL)"},
		// The same code sign-extended, as a 32-bit signed field would carry it.
		{-1073741515, "exit 0xC0000135, a DLL the game needs is missing"},
		{3221226505, "exit 0xC0000409"},
		{0, "exit 0, the process ended on its own without an error code"},
		{137, "exit 137, killed by signal 9"},
		{143, "exit 143, killed by signal 15"},
		{1, "exit 1"},
		{255, "exit 255"},
		{256, "exit 0x00000100"},
	}
	for _, c := range cases {
		if got := ExplainExit(c.code); got != c.want {
			t.Errorf("ExplainExit(%d) = %q, want %q", c.code, got, c.want)
		}
	}
}

// The drill-in and the alert explain the same codes. The web's table is the
// one an operator sees first, so a code added there and not here (or the other
// way round) is a crash the phone describes less well than the console.
func TestExitHintsMatchTheWebTable(t *testing.T) {
	src, err := os.ReadFile("../../../web/src/lib/fmt.ts")
	if err != nil {
		t.Fatalf("read the web's exit table: %v", err)
	}
	block := regexp.MustCompile(`(?s)const EXIT_HINTS[^{]*\{(.*?)\n\};`).FindSubmatch(src)
	if block == nil {
		t.Fatal("web/src/lib/fmt.ts has no EXIT_HINTS table")
	}
	var web []string
	for _, m := range regexp.MustCompile(`"0x([0-9A-F]{8})"\s*:`).FindAllSubmatch(block[1], -1) {
		web = append(web, string(m[1]))
	}
	var here []string
	for code := range exitHints {
		here = append(here, strings.ToUpper(strconv.FormatUint(uint64(code), 16)))
	}
	sort.Strings(web)
	sort.Strings(here)
	if strings.Join(web, ",") != strings.Join(here, ",") {
		t.Fatalf("exit hints differ: web %v, alerts %v", web, here)
	}
}

func TestSentences(t *testing.T) {
	at := time.Unix(1791555300, 0)
	dw := ServerRef{ID: "s1", Name: "dragonwilds-01", NodeID: "n1"}
	pw := ServerRef{ID: "s2", Name: "palworld-01", NodeID: "n1"}
	vh := ServerRef{ID: "s3", Name: "valheim-01", NodeID: "n2"}
	cases := []struct {
		e     Event
		class string
		event string
		body  string
	}{
		{ServerCrashed(dw, 3221225781, true, at), push.ClassAttend, push.EventServerCrashed,
			"dragonwilds-01 stopped unexpectedly — exit 0xC0000135, a DLL the game needs is missing"},
		{ServerCrashed(dw, 0, false, at), push.ClassAttend, push.EventServerCrashed,
			"dragonwilds-01 stopped unexpectedly"},
		{WatchdogRestart(pw, at), push.ClassHealed, push.EventWatchdogRestart,
			"the watchdog restarted palworld-01"},
		{CrashLoop(pw, 3, at), push.ClassAttend, push.EventCrashLoop,
			"palworld-01 is crash-looping: the watchdog has restarted it 3 times in the last hour"},
		{NodeOffline("n1", "abyss-win", at), push.ClassAttend, push.EventNodeOffline,
			"node abyss-win went offline — the panel lost its connection to the agent"},
		{NodePartial("n2", "abyss-lnx", at), push.ClassAttend, push.EventNodePartial,
			"node abyss-lnx can't reach Docker"},
		{BackupFailed(vh, BackupScheduled, "disk full", at), push.ClassAttend, push.EventBackupFailed,
			"the scheduled backup of valheim-01 failed: disk full"},
		{BackupFailed(vh, BackupManual, " ", at), push.ClassAttend, push.EventBackupFailed,
			"the backup of valheim-01 failed: the node gave no reason"},
		{BackupFailed(vh, BackupFinal, "tar: write error", at), push.ClassAttend, push.EventBackupFailed,
			"the final backup of valheim-01 failed, so its retire was abandoned: tar: write error"},
		{PlayersJoined(pw, []string{"Kestrel"}, 4, at), push.ClassAlive, push.EventPlayerJoined,
			"Kestrel joined palworld-01 · 4 online"},
		{PlayersJoined(pw, []string{"Kestrel", "Wren"}, 5, at), push.ClassAlive, push.EventPlayerJoined,
			"Kestrel and Wren joined palworld-01 · 5 online"},
		{PlayersJoined(pw, []string{"Kestrel", "Wren", "Ash"}, 6, at), push.ClassAlive, push.EventPlayerJoined,
			"Kestrel, Wren and Ash joined palworld-01 · 6 online"},
		{PlayersJoined(pw, []string{"A", "B", "C", "D"}, 7, at), push.ClassAlive, push.EventPlayerJoined,
			"A, B, C and 1 other joined palworld-01 · 7 online"},
		{PlayersJoined(pw, []string{"A", "B", "C", "D", "E"}, 8, at), push.ClassAlive, push.EventPlayerJoined,
			"A, B, C and 2 others joined palworld-01 · 8 online"},
		{PlayersJoinedCount(pw, 1, 4, at), push.ClassAlive, push.EventPlayerJoined, "a player joined · 4 online"},
		{PlayersJoinedCount(pw, 2, 5, at), push.ClassAlive, push.EventPlayerJoined, "2 players joined · 5 online"},
		{Test("kraken.example.com", at), push.ClassAttend, push.EventTest, "test alert from kraken.example.com"},
	}
	for _, c := range cases {
		if c.e.Class != c.class || c.e.Name != c.event || c.e.Body != c.body {
			t.Errorf("%s: class %q event %q body %q; want %q %q %q", c.event, c.e.Class, c.e.Name, c.e.Body, c.class, c.event, c.body)
		}
		if strings.Contains(c.e.Body, "!") {
			t.Errorf("%s: the house voice does not exclaim: %q", c.event, c.e.Body)
		}
	}
}

// The payload carries what the app needs to file and open the alert: a
// server's events thread per server and name its node, a node's thread per
// node.
func TestPayloadThreadsAndIDs(t *testing.T) {
	at := time.UnixMilli(1791555300123)
	p := WatchdogRestart(ServerRef{ID: "s1", Name: "palworld-01", NodeID: "n1"}, at).Payload()
	if p.V != push.PayloadVersion || p.ServerID != "s1" || p.NodeID != "n1" || p.Title != "palworld-01" ||
		p.Thread != "server:s1" || p.TSms != 1791555300123 || p.Class != push.ClassHealed {
		t.Fatalf("server payload = %+v", p)
	}
	p = NodeOffline("n2", "abyss-win", at).Payload()
	if p.ServerID != "" || p.NodeID != "n2" || p.Title != "abyss-win" || p.Thread != "node:n2" {
		t.Fatalf("node payload = %+v", p)
	}
	if p = Test("h", at).Payload(); p.ServerID != "" || p.NodeID != "" || p.Thread != "test" {
		t.Fatalf("test payload = %+v", p)
	}
}
