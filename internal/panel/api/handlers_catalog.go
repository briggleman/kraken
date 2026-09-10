package api

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/briggleman/kraken/internal/panel/catalog"
)

type catalogItem struct {
	ID              string         `json:"id"`
	Name            string         `json:"name"`
	Slug            string         `json:"slug"`
	Description     string         `json:"description,omitempty"`
	IconURL         string         `json:"icon_url,omitempty"`
	BannerURL       string         `json:"banner_url,omitempty"`
	Platforms       []string       `json:"platforms"`
	SteamAppIDs     map[string]int `json:"steam_app_ids,omitempty"`
	AlreadyImported bool           `json:"already_imported"`
}

// handleListCatalog returns the built-in starter game catalog, flagging which
// entries have already been imported (by slug) so the UI can disable re-import.
func (s *Server) handleListCatalog(w http.ResponseWriter, r *http.Request) {
	entries, err := catalog.Load()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load catalog")
		return
	}
	items := make([]catalogItem, 0, len(entries))
	for _, e := range entries {
		_, gerr := s.store.GetSpecBySlug(r.Context(), e.Spec.Slug)
		kinds := make([]string, 0, len(e.Spec.Platforms))
		for _, p := range e.Spec.Platforms {
			kinds = append(kinds, string(p.Kind))
		}
		items = append(items, catalogItem{
			ID:              e.ID,
			Name:            e.Spec.Name,
			Slug:            e.Spec.Slug,
			Description:     e.Spec.Description,
			IconURL:         e.Spec.IconURL,
			BannerURL:       e.Spec.BannerURL,
			Platforms:       kinds,
			SteamAppIDs:     e.Spec.SteamAppIDs,
			AlreadyImported: gerr == nil,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"catalog": items})
}

// SeedCatalog imports the whole bundled catalog on a FIRST boot (#203): the
// setup wizard's per-spec import only covers operators who open the wizard, so
// headless and API-first installs started with an empty spec table. It runs
// once ever, latched by settings.catalog_seeded — and it seeds only into an
// EMPTY spec table: on an upgraded deployment's first post-flag boot the latch
// is set without importing anything, because injecting new bundled entries into
// a curated fleet would be a surprise, not a convenience.
func (s *Server) SeedCatalog(ctx context.Context) {
	st := s.panelSettings(ctx)
	if st.CatalogSeeded {
		return
	}
	specs, err := s.store.ListSpecs(ctx)
	if err != nil {
		// Transient store trouble: leave the latch unset so a later boot retries.
		s.logger.Warn("catalog seed skipped — could not list specs", "err", err)
		return
	}
	seeded := 0
	if len(specs) == 0 {
		entries, lerr := catalog.Load()
		if lerr != nil {
			s.logger.Warn("catalog seed skipped — could not load bundled catalog", "err", lerr)
			return
		}
		for _, e := range entries {
			sp := *e.Spec // copy: persistNewSpec assigns a fresh ID/version
			if _, perr := s.persistNewSpec(ctx, &sp); perr != nil {
				s.logger.Warn("catalog seed: spec failed to import", "slug", e.Spec.Slug, "err", perr)
				continue
			}
			seeded++
		}
		s.logger.Info("catalog seeded", "specs", seeded)
	} else {
		s.logger.Info("catalog seed skipped — specs already exist", "existing", len(specs))
	}
	st.CatalogSeeded = true
	if serr := s.store.SaveSettings(ctx, st); serr != nil {
		s.logger.Warn("could not persist catalog_seeded — the seed will re-evaluate next boot", "err", serr)
	}
}

// handleImportCatalogSpec imports a bundled catalog spec into the live catalog,
// reusing the same validation + versioning path as direct spec creation.
func (s *Server) handleImportCatalogSpec(w http.ResponseWriter, r *http.Request) {
	entry, ok := catalog.Get(chi.URLParam(r, "id"))
	if !ok {
		writeError(w, http.StatusNotFound, "catalog item not found")
		return
	}
	sp := *entry.Spec // copy: persistNewSpec assigns a fresh ID/version
	if status, err := s.persistNewSpec(r.Context(), &sp); err != nil {
		writeError(w, status, err.Error())
		return
	}
	s.logger.Info("catalog spec imported", "slug", sp.Slug, "id", sp.ID)
	writeJSON(w, http.StatusCreated, &sp)
}
