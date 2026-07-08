package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHandlerServesEmbeddedIndex confirms the embed picks up index.html and
// serves it at the root — guards against a missing embed target on fresh
// checkouts.
func TestHandlerServesEmbeddedIndex(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept", "text/html")
	Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("root: got %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "<html") {
		t.Fatalf("root: body missing <html: %q", rr.Body.String())
	}
}

// TestHandlerSPAFallback confirms a client-router path (no matching file)
// still returns 200 + index.html so the React router can render it.
func TestHandlerSPAFallback(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/servers/some-id", nil)
	req.Header.Set("Accept", "text/html")
	Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("client route: got %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "<html") {
		t.Fatalf("client route: body missing <html")
	}
}

// TestHandlerJSONProbeReturns404 confirms a JSON-only Accept header gets a
// real 404 rather than the SPA shell — so a misrouted API call fails loudly.
func TestHandlerJSONProbeReturns404(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/does-not-exist", nil)
	req.Header.Set("Accept", "application/json")
	Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("json probe: got %d, want 404", rr.Code)
	}
}
