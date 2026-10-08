package api_test

import (
	"net/http"
	"strings"
	"testing"
)

// A start that re-runs the install pass writes a system line at every phase
// boundary with how long the phase took (#392), on a stream of its own so the
// console can set Kraken's account of the attempt apart from what the
// installer printed. This is the record an operator reads to see where an
// eight-minute reboot went.
func TestPower_UpdatePassWritesSystemStepLines(t *testing.T) {
	h, st := newTestServerStore(t)
	token := login(t, h)
	addr, _ := startFakeAgentRuntime(t, "node-x")
	nodeID := registerNode(t, h, token, addr)
	specID := createSpecWithInstall(t, h, token, "update-steps", map[string]any{
		"script": "steamcmd +login anonymous +app_update {{APP_ID}} validate +quit",
	})
	sv := seedOfflineServer(t, st, "sv-steps", nodeID, specID, nil)

	rec := do(t, h, http.MethodPost, "/api/v1/servers/"+sv.ID+"/power", token,
		map[string]string{"action": "start"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("start: got %d, want 202; body: %s", rec.Code, rec.Body.String())
	}
	waitForState(t, h, token, sv.ID, "running")

	body := getInstallLog(t, h, token, sv.ID)
	if !body.Done {
		t.Fatalf("the install log should be closed once the server is running; got %+v", body)
	}

	// The Panel's step lines, in the order the phases run.
	steps := []string{
		"[panel] updating sv-steps",
		"[panel] stopping sv-steps before the update",
		"[panel] stop took ",
		"[panel] update pass took ",
		"[panel] update complete — applying config and starting sv-steps",
		"[panel] start took ",
	}
	next := 0
	for _, l := range body.Lines {
		if next < len(steps) && strings.HasPrefix(l.Text, steps[next]) {
			if l.Stream != "system" {
				t.Errorf("step line %q is on stream %q, want system", l.Text, l.Stream)
			}
			next++
		}
	}
	if next != len(steps) {
		t.Errorf("step lines stopped at %d of %d (%q); lines: %+v", next, len(steps), steps[next], body.Lines)
	}

	// Nothing Kraken wrote may read as installer output, and the installer's
	// own output must still be there on its stream.
	installer := 0
	for _, l := range body.Lines {
		switch l.Stream {
		case "install":
			installer++
			if strings.HasPrefix(l.Text, "[panel] ") || strings.HasPrefix(l.Text, "[kraken] ") {
				t.Errorf("Kraken's own line is on the install stream: %q", l.Text)
			}
		case "system":
			if !strings.HasPrefix(l.Text, "[panel] ") && !strings.HasPrefix(l.Text, "[kraken] ") {
				t.Errorf("system line without a [panel]/[kraken] prefix: %q", l.Text)
			}
		case "error":
			t.Errorf("a clean update wrote an error line: %q", l.Text)
		default:
			t.Errorf("unknown stream %q on %q", l.Stream, l.Text)
		}
	}
	if installer == 0 {
		t.Errorf("no installer output on the install stream; lines: %+v", body.Lines)
	}
}
