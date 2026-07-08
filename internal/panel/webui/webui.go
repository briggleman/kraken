// Package webui serves the compiled React UI from the Panel binary. The Vite
// build writes into dist/ (see web/vite.config.ts) and this package embeds
// that tree so a single `panel` binary ships the UI — no separate static host.
//
// Two files in dist/ are checked in:
//   - .gitignore, which excludes real Vite output (index.html, assets/, …) so
//     builds never dirty the working tree
//   - index.stub.html, served as a "UI not built" fallback so a bare
//     `go build ./cmd/panel` (no `npm run build` first) still returns
//     something helpful at /
//
// Everything else in dist/ is generated per-build and stamped with content
// hashes — never committed.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var distFS embed.FS

// stubIndex is served when no real index.html is present in the embed (i.e.
// nobody ran `npm run build` before `go build`).
const stubIndex = "index.stub.html"

// FS returns the embedded UI file tree rooted at dist/.
func FS() fs.FS {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		// fs.Sub only fails on invalid paths; "dist" is a compile-time constant.
		panic(err)
	}
	return sub
}

// Handler returns an http.Handler that serves the embedded UI with SPA
// fallback: any request that doesn't resolve to a real embedded file is
// served index.html so the React router can render the route client-side.
// Requests carrying an Accept header that clearly doesn't want HTML (JSON
// API probes, image fetches) get a 404 instead — the fallback is only for
// browser navigations. When there's no real index.html (bare backend build),
// the committed index.stub.html is served instead.
//
// The index page is written directly rather than delegated to http.FileServer
// because FileServer 301-redirects "/index.html" → "/", which would loop
// against our SPA rewrite.
func Handler() http.Handler {
	root := FS()
	fileServer := http.FileServer(http.FS(root))
	indexName := "index.html"
	if _, err := fs.Stat(root, indexName); err != nil {
		indexName = stubIndex
	}
	indexBytes, err := fs.ReadFile(root, indexName)
	if err != nil {
		// Neither real nor stub is embedded — should be impossible given the
		// committed stub, but degrade gracefully rather than panic on a request.
		indexBytes = []byte("<!doctype html><title>Kraken</title><p>web UI unavailable</p>")
	}
	serveIndex := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(indexBytes)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clean := path.Clean(strings.TrimPrefix(r.URL.Path, "/"))
		if clean == "." || clean == "" || clean == "index.html" {
			serveIndex(w, r)
			return
		}
		if _, err := fs.Stat(root, clean); err == nil {
			fileServer.ServeHTTP(w, r)
			return
		}
		if wantsJSON(r) {
			http.NotFound(w, r)
			return
		}
		serveIndex(w, r)
	})
}

func wantsJSON(r *http.Request) bool {
	accept := r.Header.Get("Accept")
	if accept == "" {
		return false
	}
	return strings.Contains(accept, "application/json") && !strings.Contains(accept, "text/html")
}
