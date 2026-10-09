package api

import (
	"strings"
	"testing"

	"github.com/briggleman/kraken/internal/panel/store"
	"github.com/briggleman/kraken/internal/shared/spec"
)

// TestInstallScriptFor covers what each install pass actually sends: the
// per-platform script, rendered with the server's CURRENT variables, and the
// BepInEx overlay appended only when asked for (create/reinstall) — never on
// the pre-start update pass (#307).
func TestInstallScriptFor(t *testing.T) {
	sp := &spec.Spec{
		Platforms: []spec.Platform{
			{Kind: spec.LinuxNative, Image: "linux-img"},
			{Kind: spec.WindowsNative, Image: "win-img", InstallScript: `steamcmd.exe +app_update {{APP_ID}} validate +quit`},
		},
		Install: spec.Install{
			Script:        "steamcmd +login anonymous +app_update {{APP_ID}} validate +quit",
			BepInExScript: "curl -fsSL $BEPINEX_URL -o /tmp/b.zip && unzip /tmp/b.zip -d /data",
		},
	}
	linux := &store.Server{Kind: spec.LinuxNative, Vars: map[string]string{"APP_ID": "896660"}}
	windows := &store.Server{Kind: spec.WindowsNative, Vars: map[string]string{"APP_ID": "2394010"}}

	// Vanilla pass: the spec-level script, variables substituted.
	got := installScriptFor(linux, sp, passUpdate, false)
	if !strings.Contains(got, "app_update 896660 validate") {
		t.Errorf("vanilla linux pass: %q", got)
	}
	if strings.Contains(got, "unzip") {
		t.Errorf("vanilla pass must not carry the BepInEx overlay: %q", got)
	}

	// Modded create/reinstall: overlay appended after the vanilla script.
	got = installScriptFor(linux, sp, passCreate, true)
	if !strings.Contains(got, "app_update 896660") || !strings.Contains(got, "unzip") {
		t.Errorf("modded linux pass should be vanilla + overlay: %q", got)
	}
	if strings.Index(got, "app_update") > strings.Index(got, "unzip") {
		t.Errorf("the vanilla install must run before the overlay: %q", got)
	}
	if !strings.Contains(got, "+quit\ncurl") {
		t.Errorf("POSIX chains on a newline: %q", got)
	}

	// Windows: the platform override wins, and cmd chains with " & ".
	got = installScriptFor(windows, sp, passReinstall, true)
	if !strings.HasPrefix(got, "steamcmd.exe ") {
		t.Errorf("windows pass should use the platform override: %q", got)
	}
	if !strings.Contains(got, "+quit & curl") {
		t.Errorf("cmd chains with ' & ': %q", got)
	}

	// No overlay declared: asking for one changes nothing.
	plain := &spec.Spec{
		Platforms: []spec.Platform{{Kind: spec.LinuxNative, Image: "img"}},
		Install:   spec.Install{Script: "install.sh"},
	}
	if got := installScriptFor(linux, plain, passCreate, true); got != "install.sh" {
		t.Errorf("spec with no bepinex_script: %q", got)
	}
}

// TestInstallScriptFor_ValidatePlaceholder covers what {{VALIDATE}} renders as
// for each pass (#392): validate for a create (and a revive, which provisions
// as one) and a reinstall, nothing for the update-on-start pass. A VALIDATE
// key in the server's vars, which nothing should ever write, cannot shadow it.
func TestInstallScriptFor_ValidatePlaceholder(t *testing.T) {
	sp := &spec.Spec{
		Platforms: []spec.Platform{{Kind: spec.LinuxNative, Image: "img"}},
		Install: spec.Install{
			Script: "steamcmd +login anonymous +app_update {{APP_ID}} {{VALIDATE}} +quit",
		},
	}
	sv := &store.Server{Kind: spec.LinuxNative, Vars: map[string]string{"APP_ID": "896660"}}
	cases := []struct {
		pass passKind
		want string
	}{
		{passCreate, "steamcmd +login anonymous +app_update 896660 validate +quit"},
		{passReinstall, "steamcmd +login anonymous +app_update 896660 validate +quit"},
		{passUpdate, "steamcmd +login anonymous +app_update 896660  +quit"},
	}
	for _, tc := range cases {
		if got := installScriptFor(sv, sp, tc.pass, false); got != tc.want {
			t.Errorf("pass %d: got %q, want %q", tc.pass, got, tc.want)
		}
	}

	shadowed := &store.Server{Kind: spec.LinuxNative, Vars: map[string]string{"APP_ID": "896660", "VALIDATE": "-beta x"}}
	if got := installScriptFor(shadowed, sp, passUpdate, false); strings.Contains(got, "beta") {
		t.Errorf("a VALIDATE var must not shadow the pseudo-variable: %q", got)
	}
	if _, ok := sv.Vars["VALIDATE"]; ok {
		t.Errorf("rendering must never write VALIDATE into the server's vars: %v", sv.Vars)
	}
}
