package share

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestDescription(t *testing.T) {
	tests := []struct {
		name string
		card Card
		want string
	}{
		{"running with cap", Card{Game: "Valheim", State: StateRunning, PlayersKnown: true, Players: 3, MaxPlayers: 10},
			"Valheim · running · 3 of 10 players"},
		{"running without cap", Card{Game: "Windrose", State: StateRunning, PlayersKnown: true, Players: 2},
			"Windrose · running · 2 players"},
		{"running, count unknown", Card{Game: "Factorio", State: StateRunning}, "Factorio · running"},
		{"stopped hides a stale count", Card{Game: "Valheim", State: StateStopped, PlayersKnown: true, Players: 4},
			"Valheim · stopped"},
		{"unreachable", Card{Game: "Palworld", State: StateUnreachable},
			"Palworld · the panel can't reach this server right now"},
		{"join values on a second line", Card{Game: "Windrose", State: StateStarting, Join: []JoinValue{
			{Kind: JoinCode, Value: "Kq7Rw2Xp"}, {Kind: JoinPassword, Value: "tidewater"}}},
			"Windrose · starting\ninvite code Kq7Rw2Xp · password tidewater"},
	}
	for _, tc := range tests {
		if got := tc.card.Description(); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestPlayersTextAndCopyAll(t *testing.T) {
	c := Card{PlayersKnown: true, Players: 6, MaxPlayers: 32, Join: []JoinValue{
		{Kind: JoinAddress, Value: "203.0.113.24:8211"}, {Kind: JoinPassword, Value: "pw"}}}
	if got := c.PlayersText(); got != "6 / 32" {
		t.Errorf("PlayersText = %q", got)
	}
	if got := c.CopyAll(); got != "Address: 203.0.113.24:8211\nPassword: pw" {
		t.Errorf("CopyAll = %q", got)
	}
	if (&Card{}).PlayersText() != "" {
		t.Error("an unknown count renders as nothing")
	}
}

// Everything on the card is operator- or game-supplied text; the template
// must escape it in element bodies and in attribute values alike.
func TestRenderEscapes(t *testing.T) {
	var buf bytes.Buffer
	c := &Card{
		ServerName: `<script>alert(1)</script>`, Game: `"><img src=x onerror=alert(1)>`,
		State: StateRunning, AsOf: time.Unix(0, 0),
		Join:  []JoinValue{{Kind: JoinPassword, Value: `"onmouseover="x`}, {Kind: JoinAddress, Value: "a:1"}},
		Notes: "<b>bold</b>",
	}
	if err := Render(&buf, c); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, bad := range []string{"<script>alert", `"><img`, `"onmouseover="`, "<b>bold</b>"} {
		if strings.Contains(out, bad) {
			t.Errorf("unescaped %q in the page", bad)
		}
	}
	if !strings.Contains(out, `<link rel="stylesheet" href="/s/static/share.css">`) {
		t.Error("the stylesheet link is missing")
	}
	if strings.Contains(out, "<style") || strings.Contains(out, " style=") || strings.Contains(out, "<script>") {
		t.Error("the page has inline style or script, which the Panel's CSP blocks")
	}
}

func TestRenderNotFoundHasNoCard(t *testing.T) {
	var buf bytes.Buffer
	if err := RenderNotFound(&buf); err != nil {
		t.Fatal(err)
	}
	if out := buf.String(); strings.Contains(out, "og:") || !strings.Contains(out, "Link not available") {
		t.Errorf("not-found page: %s", out)
	}
}
