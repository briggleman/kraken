package catalog

import "testing"

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

func TestGet(t *testing.T) {
	if _, ok := Get("palworld"); !ok {
		t.Error("expected 'palworld' in the bundled catalog")
	}
	if _, ok := Get("does-not-exist"); ok {
		t.Error("Get returned ok for an unknown id")
	}
}
