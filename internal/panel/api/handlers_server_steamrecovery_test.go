package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/briggleman/kraken/internal/agent"
)

const (
	live0x602  = "Error! App '4019830' state is 0x602 after update job."
	orphanPath = "RSDragonwilds/Binaries/Win64/RSDragonwildsServer-Win64-Shipping.exe~RF1561a.TMP"
)

// TestPower_UpdatePassRecoversFromAFailedSteamCommit — #349, end to end. The
// update pass fails with 0x602 and leaves an orphaned `…~RF<hex>.TMP` whose
// target is missing (the 2026-09-15 wreckage). The Agent deletes the orphan and
// reruns the pass once — within the Panel's 30-minute deadline, which reaches
// it over gRPC — and the counts of install containers are exactly what the
// issue specifies.
func TestPower_UpdatePassRecoversFromAFailedSteamCommit(t *testing.T) {
	cases := []struct {
		name      string
		outcomes  []string
		orphan    bool
		passes    int
		state     string
		lastError []string // substrings; nil when the server should come up
	}{
		{
			name:     "0x602 then success runs two",
			outcomes: []string{live0x602}, orphan: true,
			passes: 2, state: "running",
		},
		{
			name:     "0x602 twice runs two and reports the second",
			outcomes: []string{live0x602, "Error! App '4019830' state is 0x606 after update job."}, orphan: true,
			passes: 2, state: "install_failed",
			lastError: []string{"state is 0x606 (update started, paused before commit, fully installed, update required)", "one retry"},
		},
		{
			name:     "No subscription runs one",
			outcomes: []string{"ERROR! Failed to install app '740' (No subscription)"}, orphan: true,
			passes: 1, state: "install_failed",
			lastError: []string{"No subscription"},
		},
		{
			name:     "0x602 with no orphans runs one",
			outcomes: []string{live0x602},
			passes:   1, state: "install_failed",
			lastError: []string{"(update started, paused before commit, update required)", "no SteamCMD staging files"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, st := newTestServerStore(t)
			token := login(t, h)
			addr, rt := startFakeAgentRuntime(t, "node-x", agent.WithFakeInstallOutcomes(tc.outcomes...))
			nodeID := registerNode(t, h, token, addr)
			specID := createSpecWithInstall(t, h, token, "update-rf", map[string]any{"script": "steamcmd +app_update 4019830 validate +quit"})
			sv := seedOfflineServer(t, st, "sv-rf", nodeID, specID, nil)
			if tc.orphan {
				if err := rt.WriteFile(context.Background(), sv.ID, orphanPath, []byte("staged")); err != nil {
					t.Fatal(err)
				}
			}

			rec := do(t, h, http.MethodPost, "/api/v1/servers/"+sv.ID+"/power", token,
				map[string]string{"action": "start"})
			if rec.Code != http.StatusAccepted {
				t.Fatalf("start: got %d, want 202; body: %s", rec.Code, rec.Body.String())
			}
			waitForState(t, h, token, sv.ID, tc.state)

			if got := len(rt.InstallScripts(sv.ID)); got != tc.passes {
				t.Errorf("install containers run: got %d, want %d", got, tc.passes)
			}
			if tc.orphan && tc.passes == 2 {
				if _, err := rt.StatFile(context.Background(), sv.ID, orphanPath); err == nil {
					t.Error("the orphaned staging file is still in the tree")
				}
			}
			_, lastErr := serverRecord(t, h, token, sv.ID)
			for _, want := range tc.lastError {
				if !strings.Contains(lastErr, want) {
					t.Errorf("last_error should contain %q; got %q", want, lastErr)
				}
			}
			if n := strings.Count(lastErr, "install failed: "); tc.lastError != nil && n != 1 {
				t.Errorf("want the \"install failed: \" prefix exactly once, got %d in %q", n, lastErr)
			}
		})
	}
}

// TestInstallFailedPrefixAppearsOnce — an Agent from before the fix sent
// SteamCMD failures already prefixed "install failed: ", and the Panel added
// its own: last_error read "install failed: install failed: …".
func TestInstallFailedPrefixAppearsOnce(t *testing.T) {
	h, st := newTestServerStore(t)
	token := login(t, h)
	addr, _ := startFakeAgentRuntime(t, "node-x",
		agent.WithFakeInstallFailure("install failed: ERROR! Failed to install app '740' (No subscription)"))
	nodeID := registerNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "old-agent", map[string]any{"script": "install.sh"})
	sv := seedOfflineServer(t, st, "sv-old", nodeID, specID, nil)

	rec := do(t, h, http.MethodPost, "/api/v1/servers/"+sv.ID+"/power", token,
		map[string]string{"action": "start"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("start: got %d, want 202; body: %s", rec.Code, rec.Body.String())
	}
	waitForState(t, h, token, sv.ID, "install_failed")
	_, lastErr := serverRecord(t, h, token, sv.ID)
	if lastErr != "install failed: ERROR! Failed to install app '740' (No subscription)" {
		t.Errorf("last_error = %q", lastErr)
	}
}
