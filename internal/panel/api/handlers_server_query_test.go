package api

import (
	"testing"

	"github.com/briggleman/kraken/internal/panel/store"
	"github.com/briggleman/kraken/internal/shared/spec"
)

// The log player-query method resolves its cap from the server's own setting
// when the spec names one (Enshrouded's slotCount), and falls back to the
// spec's constant when that setting is blank or not a number.
func TestToAgentSpec_LogQueryCap(t *testing.T) {
	sp := &spec.Spec{
		Slug:      "enshrouded",
		Platforms: []spec.Platform{{Kind: spec.WindowsNative, Image: "img"}},
		Query: &spec.PlayerQuery{
			Method: "log", JoinRegex: `in (?P<name>\S+)`, LeaveRegex: `out (?P<name>\S+)`,
			MaxPlayersSetting: "slotCount", MaxPlayers: 16,
		},
	}
	for _, tc := range []struct {
		name     string
		settings map[string]string
		want     int32
	}{
		{"setting wins", map[string]string{"slotCount": "8"}, 8},
		{"blank setting falls back", map[string]string{"slotCount": ""}, 16},
		{"non-numeric falls back", map[string]string{"slotCount": "lots"}, 16},
		{"no settings at all", nil, 16},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sv := &store.Server{ID: "s1", Kind: spec.WindowsNative, Settings: tc.settings}
			q := toAgentSpec(sv, sp).GetPlayerQuery()
			if q.GetMethod() != "log" || q.GetJoinRegex() == "" || q.GetLeaveRegex() == "" {
				t.Fatalf("query not forwarded: %v", q)
			}
			if q.GetMaxPlayers() != tc.want {
				t.Fatalf("max_players = %d, want %d", q.GetMaxPlayers(), tc.want)
			}
		})
	}
}
