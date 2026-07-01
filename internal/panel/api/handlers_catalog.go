package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/briggleman/kraken/internal/panel/catalog"
)

type catalogItem struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Slug            string   `json:"slug"`
	Description     string   `json:"description,omitempty"`
	IconURL         string   `json:"icon_url,omitempty"`
	BannerURL       string   `json:"banner_url,omitempty"`
	Platforms       []string `json:"platforms"`
	AlreadyImported bool     `json:"already_imported"`
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
			AlreadyImported: gerr == nil,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"catalog": items})
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
	if status, err := s.persistNewSpec(r, &sp); err != nil {
		writeError(w, status, err.Error())
		return
	}
	s.logger.Info("catalog spec imported", "slug", sp.Slug, "id", sp.ID)
	writeJSON(w, http.StatusCreated, &sp)
}
