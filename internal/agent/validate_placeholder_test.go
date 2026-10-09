package agent

import (
	"strings"
	"testing"

	"github.com/briggleman/kraken/internal/shared/agentpb"
	"github.com/briggleman/kraken/internal/shared/spec"
)

// The bundled Steam specs write {{VALIDATE}} where `validate` used to be
// (#392), and the Panel renders it empty on the update-on-start pass. These
// tests feed the Agent's script and log parsers what such a pass looks like —
// a double space where the word was — and hold them to the outcome they give
// the validating render.

// windowsTwoPass is the bundled windows-native two-step, as the specs write it.
const windowsTwoPass = `steamcmd.exe +force_install_dir C:\data +login anonymous +app_update {{APP_ID}} {{VALIDATE}} +quit` +
	` & steamcmd.exe +force_install_dir C:\data +login anonymous +app_update {{APP_ID}} {{VALIDATE}} +quit`

// rendered is windowsTwoPass as the Panel sends it for a pass that does, or
// does not, validate.
func rendered(validate bool) string {
	return spec.RenderInstall(windowsTwoPass, map[string]string{"APP_ID": "4019830"}, validate)
}

// The Windows self-update guard splits the cmd chain on `&` and waits after
// every steamcmd segment. An empty render changes the words inside a segment,
// never the chain, so the guarded script is the validating one minus the word.
func TestGuardWindowsSteamInstall_EmptyValidate(t *testing.T) {
	empty := rendered(false)
	if !strings.Contains(empty, "+app_update 4019830  +quit") {
		t.Fatalf("expected the double space an empty render leaves: %q", empty)
	}
	gotEmpty, appliedEmpty := guardWindowsSteamInstall(empty)
	gotFull, appliedFull := guardWindowsSteamInstall(rendered(true))
	if !appliedEmpty || !appliedFull {
		t.Fatalf("guard applied: empty=%v validate=%v, want both", appliedEmpty, appliedFull)
	}
	if want := strings.ReplaceAll(gotFull, " validate ", "  "); gotEmpty != want {
		t.Errorf("empty render guarded differently from the validating one\n got: %q\nwant: %q", gotEmpty, want)
	}
	// Prime, one wait after the prime, one after each of the two passes.
	if n := strings.Count(gotEmpty, steamcmdWaitCmd); n != 3 {
		t.Errorf("waits: got %d, want 3 (prime + two passes): %q", n, gotEmpty)
	}
	if !strings.HasPrefix(gotEmpty, steamcmdPrime+" & ") {
		t.Errorf("guarded script must start with the prime: %q", gotEmpty)
	}
	// And it is still recognised as guarded on the way back round.
	if again, applied := guardWindowsSteamInstall(gotEmpty); applied || again != gotEmpty {
		t.Errorf("an already-guarded empty render was guarded again: %q", again)
	}
}

// noRetryReason reads the request's env, not its script, so an empty render
// cannot change whether a failed pass may be retried — with a Steam Guard code
// or without one.
func TestNoRetryReason_EmptyValidate(t *testing.T) {
	for _, env := range []map[string]string{
		{"APP_ID": "4019830"},
		{"APP_ID": "4019830", steamGuardEnv: "X7K2Q"},
	} {
		full := noRetryReason(&agentpb.InstallServerRequest{InstallScript: rendered(true), Env: env})
		empty := noRetryReason(&agentpb.InstallServerRequest{InstallScript: rendered(false), Env: env})
		if full != empty {
			t.Errorf("env %v: empty render gave %q, validating render %q", env, empty, full)
		}
	}
}

// Without validate, a pass over a current build ends "already up to date"
// rather than "fully installed". It must clear the fresh-home "Missing
// configuration" of the first half of the two-step exactly as the validating
// success line does, or a good tree lands install_failed.
func TestSteamInstallOutcome_EmptyValidate(t *testing.T) {
	const missing = "ERROR! Failed to install app '4019830' (Missing configuration)"
	cases := []struct {
		name  string
		lines []string
		want  string
	}{
		{"two-step, current build", []string{missing, "Success! App '4019830' already up to date."}, ""},
		{"two-step, new build", []string{missing, " Update state (0x61) downloading, progress: 42.00 (420 / 1000)", "Success! App '4019830' fully installed."}, ""},
		{"first half downloads, second is current", []string{"Success! App '4019830' fully installed.", "Success! App '4019830' already up to date."}, ""},
		{"a later failure still wins", []string{"Success! App '4019830' already up to date.", live0x602}, live0x602},
	}
	for _, tc := range cases {
		pending := ""
		for _, l := range tc.lines {
			pending = steamInstallOutcome(pending, l)
		}
		if pending != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, pending, tc.want)
		}
	}
}
