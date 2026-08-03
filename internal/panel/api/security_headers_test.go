package api_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/briggleman/kraken/internal/panel/api"
	"github.com/briggleman/kraken/internal/panel/config"
	"github.com/briggleman/kraken/internal/panel/store/memory"
)

// headersFor builds a Panel with the given config and returns the response
// headers for one request. The security headers are set by middleware on every
// response, so an unauthenticated 401 carries them just as an authorized 200
// does — no seeding or login needed.
func headersFor(t *testing.T, cfg *config.Config) http.Header {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := api.New(cfg, memory.New(), logger).Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/servers", nil))
	return rec.Result().Header
}

func TestSecurityHeaders_AlwaysSet(t *testing.T) {
	h := headersFor(t, &config.Config{Env: "test"})
	for name, want := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
	} {
		if got := h.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if h.Get("Permissions-Policy") == "" {
		t.Error("Permissions-Policy not set")
	}
}

// A zero-value Config (constructed in code rather than parsed from the
// environment) must still get an enforcing policy. Defaulting the empty string
// to "off" would silently drop the header for every embedder.
func TestCSP_EnforcedByDefault(t *testing.T) {
	h := headersFor(t, &config.Config{Env: "test"})
	if h.Get("Content-Security-Policy-Report-Only") != "" {
		t.Error("report-only header set when mode was unspecified")
	}
	csp := h.Get("Content-Security-Policy")
	if csp == "" {
		t.Fatal("Content-Security-Policy not set")
	}
	for _, want := range []string{
		"default-src 'self'",
		"script-src 'self'",
		"object-src 'none'",
		"base-uri 'none'",
		"frame-ancestors 'none'",
		"form-action 'self'",
		// The brand faces are self-hosted, so no font CDN belongs in either.
		"style-src 'self'",
		"font-src 'self'",
		// Game Specs carry operator-supplied artwork URLs on arbitrary CDNs.
		"img-src 'self' data: https:",
		"connect-src 'self'",
	} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP missing %q\n  got: %s", want, csp)
		}
	}
	// Regression guard: the fonts were on Google's CDN until they were vendored
	// into web/public/fonts. If either host comes back, the LAN-first promise and
	// this policy have both quietly regressed.
	for _, host := range []string{"fonts.googleapis.com", "fonts.gstatic.com"} {
		if strings.Contains(csp, host) {
			t.Errorf("CSP allows %s — the brand faces are self-hosted: %s", host, csp)
		}
	}
	// The bundle has no inline script and no eval, so neither escape hatch
	// belongs in the policy. These are the assertions that fail loudly if
	// someone loosens the header to make a stubborn feature work.
	for _, forbidden := range []string{"'unsafe-eval'", "'unsafe-inline'"} {
		if strings.Contains(csp, forbidden) {
			t.Errorf("CSP contains %s — the bundle does not need it: %s", forbidden, csp)
		}
	}
	// Plain-http LAN installs are first-class; upgrading their subresources breaks them.
	if strings.Contains(csp, "upgrade-insecure-requests") {
		t.Errorf("CSP must not upgrade insecure requests (breaks http LAN installs): %s", csp)
	}
}

func TestCSP_ReportOnlyMode(t *testing.T) {
	h := headersFor(t, &config.Config{Env: "test", CSPMode: config.CSPReportOnly})
	if got := h.Get("Content-Security-Policy-Report-Only"); got == "" {
		t.Error("Content-Security-Policy-Report-Only not set in report-only mode")
	}
	if got := h.Get("Content-Security-Policy"); got != "" {
		t.Errorf("enforcing header also set in report-only mode: %q", got)
	}
}

func TestCSP_OffMode(t *testing.T) {
	h := headersFor(t, &config.Config{Env: "test", CSPMode: config.CSPOff})
	if got := h.Get("Content-Security-Policy"); got != "" {
		t.Errorf("CSP set while off: %q", got)
	}
	if got := h.Get("Content-Security-Policy-Report-Only"); got != "" {
		t.Errorf("report-only CSP set while off: %q", got)
	}
	// Turning the CSP off must not take the other headers with it.
	if h.Get("X-Frame-Options") != "DENY" {
		t.Error("X-Frame-Options dropped when CSP is off")
	}
}

// A Panel behind a CDN that injects a script (Cloudflare Web Analytics, for one)
// allows that host per-deployment rather than in the default everyone inherits.
func TestCSP_ExtraSourcesAppended(t *testing.T) {
	h := headersFor(t, &config.Config{
		Env:           "test",
		CSPScriptSrc:  []string{"https://static.cloudflareinsights.com"},
		CSPConnectSrc: []string{"https://example-collector.test"},
	})
	csp := h.Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'self' https://static.cloudflareinsights.com") {
		t.Errorf("extra script-src not appended: %s", csp)
	}
	if !strings.Contains(csp, "connect-src 'self' https://example-collector.test") {
		t.Errorf("extra connect-src not appended: %s", csp)
	}
}

func TestCSPMode_UnknownValueFallsBackToEnforce(t *testing.T) {
	t.Setenv("KRAKEN_CSP", "yes-please")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.CSPMode != config.CSPEnforce {
		t.Errorf("CSPMode = %q, want %q — a typo must not disable the header", cfg.CSPMode, config.CSPEnforce)
	}
}
