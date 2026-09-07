package catalog

import (
	"encoding/json"
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
