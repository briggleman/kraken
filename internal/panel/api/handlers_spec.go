package api

import (
	"errors"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"sigs.k8s.io/yaml"

	"github.com/briggleman/kraken/internal/panel/store"
	"github.com/briggleman/kraken/internal/shared/spec"
)

// decodeSpec reads a spec from the request body, accepting either JSON or YAML
// (JSON is valid YAML; sigs.k8s.io/yaml honors the same json tags).
func decodeSpec(r *http.Request, sp *spec.Spec) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return err
	}
	return yaml.Unmarshal(body, sp)
}

func (s *Server) handleListSpecs(w http.ResponseWriter, r *http.Request) {
	specs, err := s.store.ListSpecs(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list specs")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"specs": specs})
}

func (s *Server) handleGetSpec(w http.ResponseWriter, r *http.Request) {
	sp, err := s.store.GetSpec(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "spec not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not get spec")
		return
	}
	writeJSON(w, http.StatusOK, sp)
}

// persistNewSpec assigns server-owned identity + version, validates, and stores a
// new spec. It returns the HTTP status to surface and an error message on failure
// (validation → 400, duplicate slug → 409, otherwise 500). Shared by direct spec
// creation and one-click catalog import so both follow the same rules.
func (s *Server) persistNewSpec(r *http.Request, sp *spec.Spec) (int, error) {
	sp.ID = uuid.NewString()
	sp.Version = 1
	if err := sp.Validate(); err != nil {
		return http.StatusBadRequest, err
	}
	if err := s.store.CreateSpec(r.Context(), sp); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return http.StatusConflict, errors.New("a spec with that slug already exists")
		}
		return http.StatusInternalServerError, errors.New("could not create spec")
	}
	return http.StatusCreated, nil
}

func (s *Server) handleCreateSpec(w http.ResponseWriter, r *http.Request) {
	var sp spec.Spec
	if err := decodeSpec(r, &sp); err != nil {
		writeError(w, http.StatusBadRequest, "invalid spec body (expected JSON or YAML): "+err.Error())
		return
	}
	if status, err := s.persistNewSpec(r, &sp); err != nil {
		writeError(w, status, err.Error())
		return
	}
	s.logger.Info("spec created", "slug", sp.Slug, "id", sp.ID)
	writeJSON(w, http.StatusCreated, &sp)
}

func (s *Server) handleUpdateSpec(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	existing, err := s.store.GetSpec(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "spec not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not load spec")
		return
	}

	var sp spec.Spec
	if err := decodeSpec(r, &sp); err != nil {
		writeError(w, http.StatusBadRequest, "invalid spec body (expected JSON or YAML): "+err.Error())
		return
	}
	sp.ID = existing.ID
	sp.Version = existing.Version + 1
	if err := sp.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.UpdateSpec(r.Context(), &sp); err != nil {
		writeError(w, http.StatusInternalServerError, "could not update spec")
		return
	}
	s.logger.Info("spec updated", "slug", sp.Slug, "id", sp.ID, "version", sp.Version)
	writeJSON(w, http.StatusOK, &sp)
}

func (s *Server) handleDeleteSpec(w http.ResponseWriter, r *http.Request) {
	err := s.store.DeleteSpec(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "spec not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not delete spec")
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}
