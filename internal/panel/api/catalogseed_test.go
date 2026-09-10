package api_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/briggleman/kraken/internal/panel"
	"github.com/briggleman/kraken/internal/panel/api"
	"github.com/briggleman/kraken/internal/panel/catalog"
	"github.com/briggleman/kraken/internal/panel/config"
	"github.com/briggleman/kraken/internal/panel/store/memory"
	"github.com/briggleman/kraken/internal/shared/spec"
)

// newSeedServer builds a Server (not just its handler — SeedCatalog is a boot
// hook, not a route) over a fresh memory store.
func newSeedServer(t *testing.T) (*api.Server, *memory.Store) {
	t.Helper()
	st := memory.New()
	cfg := &config.Config{
		Env:                    "test",
		SessionTTL:             time.Hour,
		BootstrapAdminUser:     testAdmin,
		BootstrapAdminPassword: testPass,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := panel.Seed(context.Background(), st, cfg, logger); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return api.New(cfg, st, logger), st
}

func TestSeedCatalogFirstBoot(t *testing.T) {
	srv, st := newSeedServer(t)
	ctx := context.Background()

	entries, err := catalog.Load()
	if err != nil {
		t.Fatalf("catalog load: %v", err)
	}

	srv.SeedCatalog(ctx)

	specs, err := st.ListSpecs(ctx)
	if err != nil {
		t.Fatalf("list specs: %v", err)
	}
	if len(specs) != len(entries) {
		t.Fatalf("seeded %d specs, want the whole catalog (%d)", len(specs), len(entries))
	}
	settings, err := st.GetSettings(ctx)
	if err != nil || !settings.CatalogSeeded {
		t.Fatalf("catalog_seeded not latched (settings=%+v err=%v)", settings, err)
	}

	// Latched: a second boot imports nothing more.
	srv.SeedCatalog(ctx)
	again, _ := st.ListSpecs(ctx)
	if len(again) != len(specs) {
		t.Fatalf("second seed changed the spec count: %d → %d", len(specs), len(again))
	}
}

func TestSeedCatalogNeverSeedsAnExistingDeployment(t *testing.T) {
	srv, st := newSeedServer(t)
	ctx := context.Background()

	// An upgraded deployment: specs exist, the latch (a new field) does not.
	existing := &spec.Spec{ID: "pre", Slug: "palworld", Name: "Palworld", Version: 3,
		Platforms: []spec.Platform{{Kind: spec.LinuxNative, Image: "ghcr.io/x/y:z"}},
		Startup:   spec.Startup{Command: "run"},
	}
	if err := st.CreateSpec(ctx, existing); err != nil {
		t.Fatalf("create existing spec: %v", err)
	}

	srv.SeedCatalog(ctx)

	specs, _ := st.ListSpecs(ctx)
	if len(specs) != 1 {
		t.Fatalf("seed injected bundled specs into an existing deployment: %d specs", len(specs))
	}
	settings, err := st.GetSettings(ctx)
	if err != nil || !settings.CatalogSeeded {
		t.Fatalf("latch not set on the skip path (settings=%+v err=%v)", settings, err)
	}
}
