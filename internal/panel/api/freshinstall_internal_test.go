package api

import (
	"testing"
	"time"

	"github.com/briggleman/kraken/internal/panel/store"
)

func TestFreshlyProvisioned(t *testing.T) {
	now := time.Date(2026, 9, 23, 19, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *time.Time { t := now.Add(-d); return &t }

	cases := []struct {
		name string
		at   *time.Time
		want bool
	}{
		// A server provisioned before the field existed updates as it always did.
		{"never stamped", nil, false},
		{"just now", at(0), true},
		{"the form's auto-start, seconds later", at(5 * time.Second), true},
		{"operator filled in settings first", at(20 * time.Minute), true},
		{"just inside the window", at(freshInstallWindow - time.Second), true},
		// The boundary is exclusive: at exactly the window the tree is no longer
		// "just installed".
		{"exactly the window", at(freshInstallWindow), false},
		{"left for days", at(72 * time.Hour), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sv := &store.Server{ProvisionedAt: c.at}
			if got := freshlyProvisioned(sv, now); got != c.want {
				t.Fatalf("freshlyProvisioned: got %v, want %v", got, c.want)
			}
		})
	}
}
