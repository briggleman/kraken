package api

import (
	_ "embed"
	"net/http"
)

//go:embed openapi.yaml
var openAPISpec []byte

// handleOpenAPISpec serves the embedded OpenAPI document. It is mounted behind
// authentication; the in-app API reference fetches and renders it with the
// Panel's own branding (no public Swagger UI).
func (s *Server) handleOpenAPISpec(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/yaml")
	_, _ = w.Write(openAPISpec)
}
