// Package web serves the phase-2 single-page UI out of the hub binary.
//
// The SPA is built by `make ui` into dist/ and embedded here, the same way
// migrations and the phase-1 templates are: one binary, no asset directory to
// deploy, no separate web server.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

// distFS holds the built SPA. dist/ is generated, and .gitignore excludes it,
// so a checkout without `make ui` will fail to compile here rather than
// silently serving nothing.
//
//go:embed all:dist
var distFS embed.FS

// Handler serves the SPA.
//
// Every path that is not a real file falls back to index.html, because the
// router owns those paths client-side. Without the fallback, reloading
// /hosts/3/graphs 404s -- and deep links are load-bearing: the diagnosis
// drawer links into specific tabs.
func Handler() http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic("web: dist not embedded: " + err.Error())
	}
	files := http.FileServer(http.FS(sub))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p != "" {
			if f, err := sub.Open(p); err == nil {
				_ = f.Close()
				// Vite fingerprints everything under assets/, so those are safe
				// to pin forever. Anything else keeps the default.
				if strings.HasPrefix(p, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				files.ServeHTTP(w, r)
				return
			}
		}
		// index.html must be revalidated every load or a deploy never reaches
		// a browser that already has it.
		w.Header().Set("Cache-Control", "no-cache")
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/"
		files.ServeHTTP(w, r2)
	})
}
