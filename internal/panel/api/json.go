package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// errorBody is the standard JSON error envelope returned by the API.
type errorBody struct {
	Error string `json:"error"`
}

// writeJSON encodes v as JSON with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("api: encode response", "err", err)
	}
}

// writeError writes a JSON error envelope with the given status code.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorBody{Error: msg})
}

// decodeJSON decodes the request body into v, rejecting unknown fields and
// reporting a 400-friendly error on failure.
func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}
