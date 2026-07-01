// Package catalog provides a small built-in library of ready-to-import game
// specs, embedded in the Panel binary so a fresh install can deploy a game with
// one click during onboarding. The bundled YAML lives under bundled/ (go:embed is
// package-relative). Each file must be a valid spec.Spec (enforced by tests).
package catalog

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"

	"sigs.k8s.io/yaml"

	"github.com/briggleman/kraken/internal/shared/spec"
)

//go:embed bundled/*.yaml
var bundledFS embed.FS

// Entry is a catalog item: a parsed spec plus its derived catalog id (the slug).
type Entry struct {
	ID   string
	Spec *spec.Spec
}

// Load parses every bundled catalog spec, sorted by name. It returns an error if
// any embedded spec is malformed or missing a slug.
func Load() ([]Entry, error) {
	files, err := fs.ReadDir(bundledFS, "bundled")
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(files))
	for _, f := range files {
		if f.IsDir() {
			continue
		}
		data, err := bundledFS.ReadFile("bundled/" + f.Name())
		if err != nil {
			return nil, err
		}
		var sp spec.Spec
		if err := yaml.Unmarshal(data, &sp); err != nil {
			return nil, fmt.Errorf("catalog: parse %s: %w", f.Name(), err)
		}
		if sp.Slug == "" {
			return nil, fmt.Errorf("catalog: %s has no slug", f.Name())
		}
		out = append(out, Entry{ID: sp.Slug, Spec: &sp})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Spec.Name < out[j].Spec.Name })
	return out, nil
}

// Get returns the catalog entry with the given id (slug), or false if absent.
func Get(id string) (Entry, bool) {
	all, err := Load()
	if err != nil {
		return Entry{}, false
	}
	for _, e := range all {
		if e.ID == id {
			return e, true
		}
	}
	return Entry{}, false
}
