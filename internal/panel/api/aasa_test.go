package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/briggleman/kraken/internal/panel/config"
)

const aasaPath = "/.well-known/apple-app-site-association"

// getAASA fetches the association file the way Apple's CDN does: anonymously,
// with whatever Accept header it likes. A browser-style Accept is the hard case,
// because that is what would pull the UI fallback's index.html.
func getAASA(t *testing.T, h http.Handler, accept string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, aasaPath, nil)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// With app IDs configured the file is served as JSON, with no session, no
// redirect, and exactly the webcredentials shape Apple parses.
func TestAppleAppSiteAssociationServesConfiguredApps(t *testing.T) {
	h := newTestServerWith(t, func(c *config.Config) {
		c.AppleAppIDs = []string{"HGMD87Q9KP.com.briggleman.kraken-companion", "ABCDE12345.com.example.other"}
	})
	for _, accept := range []string{"", "*/*", "text/html,application/xhtml+xml"} {
		rec := getAASA(t, h, accept)
		if rec.Code != http.StatusOK {
			t.Fatalf("Accept %q: status %d, want 200 (body %q)", accept, rec.Code, rec.Body.String())
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("Accept %q: Content-Type %q, want application/json", accept, ct)
		}
		if loc := rec.Header().Get("Location"); loc != "" {
			t.Errorf("Accept %q: redirected to %q — Apple does not follow redirects", accept, loc)
		}
		want := `{"webcredentials":{"apps":["HGMD87Q9KP.com.briggleman.kraken-companion","ABCDE12345.com.example.other"]}}`
		if got := strings.TrimSpace(rec.Body.String()); got != want {
			t.Errorf("Accept %q: body\n  got  %s\n  want %s", accept, got, want)
		}
	}
}

// Unconfigured, the path is a 404 — and never the web UI's index page, which
// the SPA fallback would otherwise hand a browser-style request with a 200.
func TestAppleAppSiteAssociationIsNotFoundWhenUnconfigured(t *testing.T) {
	h := newTestServer(t)
	for _, accept := range []string{"", "text/html"} {
		rec := getAASA(t, h, accept)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("Accept %q: status %d, want 404", accept, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "<html") {
			t.Errorf("Accept %q: served the UI shell: %q", accept, rec.Body.String())
		}
	}
}
