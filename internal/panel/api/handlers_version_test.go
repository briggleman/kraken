package api_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/briggleman/kraken/internal/shared/version"
)

// The UI's footer stamp and the per-node version-skew flag both read this, so it
// has to report the Panel's real build rather than a placeholder.
func TestVersionEndpoint(t *testing.T) {
	h, _ := newTestServerStore(t)
	token := login(t, h)

	rec := do(t, h, http.MethodGet, "/api/v1/version", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Version string `json:"version"`
		Commit  string `json:"commit"`
		Date    string `json:"date"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Version != version.Version {
		t.Errorf("version = %q, want the build's %q", out.Version, version.Version)
	}
	if out.Commit == "" || out.Date == "" {
		t.Errorf("commit/date must be present so a bug report can name a build: commit=%q date=%q", out.Commit, out.Date)
	}
}

// The build is deliberately behind auth: an unauthenticated caller shouldn't be
// able to fingerprint which Panel version is running.
func TestVersionEndpointRequiresAuth(t *testing.T) {
	h, _ := newTestServerStore(t)
	if rec := do(t, h, http.MethodGet, "/api/v1/version", "", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("status %d, want 401 without a session", rec.Code)
	}
}
