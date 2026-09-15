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

// maxJSONBody caps every JSON request body the API will read. Sized off the
// largest legitimate one: the in-browser editor saves a file through
// POST /files/write, and it will load a file up to maxEditBytes (1 MiB), which
// JSON string-escaping can inflate several times over. 4 MiB clears that with
// room to spare while still bounding what one request can make the Panel hold.
// Bulk paths do not come through here — a spec upload reads its own limited
// body, and file uploads are multipart.
const maxJSONBody = 4 << 20

// decodeJSON decodes the request body into v, rejecting unknown fields and
// reporting a 400-friendly error on failure. The body is capped at
// maxJSONBody: without it any authenticated caller could pin arbitrary Panel
// memory with one request, since json.Decoder reads until the body ends.
func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxJSONBody))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}
