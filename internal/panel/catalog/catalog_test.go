package catalog

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/briggleman/kraken/internal/shared/spec"
)

// TestLoadValidates ensures every bundled catalog spec parses and passes the same
// validation the live API enforces — so a malformed shipped spec fails CI, not a
// user's one-click import.
func TestLoadValidates(t *testing.T) {
	entries, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("catalog is empty; expected bundled starter specs")
	}
	for _, e := range entries {
		if e.ID == "" || e.Spec == nil {
			t.Fatalf("catalog entry missing id/spec: %+v", e)
		}
		if err := e.Spec.Validate(); err != nil {
			t.Errorf("bundled spec %q failed validation: %v", e.ID, err)
		}
	}
}

// TestBundledConfigsRenderValidJSON renders every bundled spec's template
// config files and, for *.json paths, requires the output to actually parse.
// Two passes per spec: field defaults as-is, and every password field set —
// templates with conditional blocks (enshrouded's userGroups) must emit valid
// JSON in both shapes, or a server boots against a config its game rejects.
func TestBundledConfigsRenderValidJSON(t *testing.T) {
	entries, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, e := range entries {
		ports := map[string]int{}
		for _, p := range e.Spec.Ports {
			ports[p.Name] = p.Default
		}
		vars := map[string]string{}
		for _, v := range e.Spec.Variables {
			vars[v.Key] = v.Default
		}
		withPasswords := map[string]string{}
		for _, g := range e.Spec.Settings.Groups {
			for _, f := range g.Fields {
				if f.Type == spec.FieldPassword {
					withPasswords[f.Key] = "s3cret"
				}
			}
		}
		for name, overrides := range map[string]map[string]string{
			"defaults":      nil,
			"passwords-set": withPasswords,
		} {
			settings := e.Spec.ResolveSettings(overrides)
			for _, cf := range e.Spec.ConfigFiles {
				if cf.Format != spec.FormatTemplate {
					continue
				}
				out, err := spec.RenderConfig(cf, spec.RenderContext{Settings: settings, Vars: vars, Ports: ports})
				if err != nil {
					t.Errorf("%s (%s): render %s: %v", e.ID, name, cf.Path, err)
					continue
				}
				if strings.HasSuffix(cf.Path, ".json") && !json.Valid([]byte(out)) {
					t.Errorf("%s (%s): %s renders invalid JSON:\n%s", e.ID, name, cf.Path, out)
				}
			}
		}
	}
}

func TestGet(t *testing.T) {
	if _, ok := Get("palworld"); !ok {
		t.Error("expected 'palworld' in the bundled catalog")
	}
	if _, ok := Get("does-not-exist"); ok {
		t.Error("Get returned ok for an unknown id")
	}
}

// TestDragonwildsOwnerIdIsRequired pins the incident this field exists for. A
// fresh Dragonwilds server boots on the spec's own defaults, where OwnerId is
// blank, and the game crashes on a blank OwnerId. The spec must mark it required
// so the Panel refuses that start instead of launching it into a crash loop.
func TestDragonwildsOwnerIdIsRequired(t *testing.T) {
	entries, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var dw *spec.Spec
	for _, e := range entries {
		if e.Spec != nil && e.Spec.Slug == "dragonwilds" {
			dw = e.Spec
		}
	}
	if dw == nil {
		t.Fatal("bundled dragonwilds spec not found")
	}
	missing := dw.MissingRequiredSettings(dw.ResolveSettings(nil))
	if len(missing) != 1 || missing[0].Key != "OwnerId" {
		t.Fatalf("a fresh dragonwilds server must be missing exactly OwnerId, got %+v", missing)
	}
	if missing[0].Label == "" {
		t.Fatal("OwnerId needs a label: it is what the refusal message names")
	}
}

// Every bundled Steam spec is covered by the build check (#392) on every
// platform it declares, with no update_check block of its own: the derivation
// from the install script finds app_update and the platform's app id. The two
// that are not Steam installs say so — Factorio explicitly, the Windows demo
// by having no app_update to find.
func TestBundledSpecsResolveTheBuildCheck(t *testing.T) {
	entries, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	notSteam := map[string]bool{"factorio": true, "windemo": true}
	for _, e := range entries {
		for _, kind := range e.Spec.PlatformKinds() {
			uc := e.Spec.UpdateCheckFor(kind)
			switch {
			case notSteam[e.ID] && uc.Steam():
				t.Errorf("%s/%s: want no build check, got %+v", e.ID, kind, uc)
			case !notSteam[e.ID] && (!uc.Steam() || uc.Branch != "public"):
				t.Errorf("%s/%s: want a steam check on the public branch, got %+v", e.ID, kind, uc)
			}
		}
	}
	if f, ok := Get("factorio"); !ok || f.Spec.Install.UpdateCheck == nil || f.Spec.Install.UpdateCheck.Method != spec.UpdateCheckNone {
		t.Error("factorio must opt out of the build check explicitly (install.update_check.method: none)")
	}
}

// appUpdateArgsRE captures what a SteamCMD `+app_update` is given, up to the
// next `+command`.
var appUpdateArgsRE = regexp.MustCompile(`app_update([^+]*)`)

// Every bundled Steam spec leaves validate to the pass (#392): each app_update
// in each platform's install script carries {{VALIDATE}}, and no bare
// `validate` is left, so the update-on-start pass downloads a new build's
// changed chunks instead of re-hashing the whole tree. The BepInEx overlays are
// not install passes and are not checked.
func TestBundledSteamSpecsLeaveValidateToThePass(t *testing.T) {
	entries, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	placeholder := "{{" + spec.ValidateVar + "}}"
	notSteam := map[string]bool{"factorio": true, "windemo": true}
	for _, e := range entries {
		if notSteam[e.ID] {
			continue
		}
		for _, kind := range e.Spec.PlatformKinds() {
			script := e.Spec.InstallScriptFor(kind)
			calls := appUpdateArgsRE.FindAllStringSubmatch(script, -1)
			if len(calls) == 0 {
				t.Errorf("%s/%s: no app_update in the install script", e.ID, kind)
			}
			for _, c := range calls {
				if !strings.Contains(c[1], placeholder) {
					t.Errorf("%s/%s: app_update%s has no %s", e.ID, kind, c[1], placeholder)
				}
			}
			if rest := strings.ReplaceAll(script, placeholder, ""); strings.Contains(strings.ToLower(rest), "validate") {
				t.Errorf("%s/%s: a bare validate is left in the install script: %q", e.ID, kind, script)
			}
		}
	}
}
