package api_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/briggleman/kraken/internal/panel/config"
)

const aasaPath = "/.well-known/apple-app-site-association"

// Without app IDs the association path is a real 404 — not the SPA shell with
// a 200, which iOS would try (and fail) to parse as the association file.
func TestAppleAppSiteAssociationOffByDefault(t *testing.T) {
	h := newTestServer(t)
	rec := do(t, h, http.MethodGet, aasaPath, "", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404; body %q", rec.Code, rec.Body.String())
	}
}

// With app IDs it is unauthenticated JSON naming them under webcredentials,
// which is what Password AutoFill reads.
func TestAppleAppSiteAssociationServesWebCredentials(t *testing.T) {
	ids := []string{"HGMD87Q9KP.com.briggleman.kraken-companion", "ABCDE12345.io.example.other"}
	h := newTestServerWith(t, func(c *config.Config) { c.AppleAppIDs = ids })
	rec := do(t, h, http.MethodGet, aasaPath, "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200; body %q", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type %q, want application/json", ct)
	}
	var got struct {
		WebCredentials struct {
			Apps []string `json:"apps"`
		} `json:"webcredentials"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not JSON: %v (%q)", err, rec.Body.String())
	}
	if len(got.WebCredentials.Apps) != len(ids) {
		t.Fatalf("apps %v, want %v", got.WebCredentials.Apps, ids)
	}
	for i, id := range ids {
		if got.WebCredentials.Apps[i] != id {
			t.Errorf("apps[%d] = %q, want %q", i, got.WebCredentials.Apps[i], id)
		}
	}
}
