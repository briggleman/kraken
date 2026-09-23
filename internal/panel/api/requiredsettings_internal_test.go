package api

import (
	"testing"

	"github.com/briggleman/kraken/internal/shared/spec"
)

func TestRequiredSettingsMessage(t *testing.T) {
	f := func(key, label string) spec.SettingField { return spec.SettingField{Key: key, Label: label} }
	cases := []struct {
		name    string
		missing []spec.SettingField
		want    string
	}{
		{"one", []spec.SettingField{f("OwnerId", "Owner Player ID")},
			"Owner Player ID is required before this server can start — set it on the Settings tab"},
		{"two", []spec.SettingField{f("OwnerId", "Owner Player ID"), f("Seed", "World seed")},
			"Owner Player ID and World seed are required before this server can start — set them on the Settings tab"},
		{"three", []spec.SettingField{f("a", "A"), f("b", "B"), f("c", "C")},
			"A, B and C are required before this server can start — set them on the Settings tab"},
		// No label: the key is still something the operator can find.
		{"unlabelled", []spec.SettingField{f("OwnerId", "")},
			"OwnerId is required before this server can start — set it on the Settings tab"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := requiredSettingsMessage(c.missing); got != c.want {
				t.Fatalf("got  %q\nwant %q", got, c.want)
			}
		})
	}
}
