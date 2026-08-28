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
	"slices"
	"strings"
)

// distFS holds the built SPA. dist/ is generated and .gitignore excludes its
// contents, keeping only a tracked, empty dist/.gitkeep. That file exists
// solely so `//go:embed all:dist` -- resolved at compile time -- has
// something to embed on a fresh checkout; without it `go build ./...` and
// `go test ./...` fail outright with "pattern all:dist: no matching files
// found". A real index.html is deliberately NOT tracked here (an earlier
// version of this file committed one as a placeholder, but `make ui`
// overwrites it on every build, leaving the working tree permanently dirty).
// The consequence is that distFS may contain nothing at all on a checkout
// that hasn't run `make ui`; see TestHandlerDoesNotPanic in
// embed_internal_test.go for the one thing asserted about that case.
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
	return handlerFor(sub)
}

// handlerFor implements the routing logic against an arbitrary fs.FS, so
// tests can exercise the assets/ immutable-caching branch with a synthetic
// filesystem instead of a real Vite build. Handler is the thin wrapper that
// supplies the embedded dist/ tree.
func handlerFor(sub fs.FS) http.Handler {
	files := http.FileServer(http.FS(sub))

	// An unbuilt dist/ is a build mistake, not a request error, and it used to
	// present as neither: with only .gitkeep embedded, the fallback below hands
	// "/" to http.FileServer, which answers 200 and a directory listing of the
	// one empty file. The image starts, /api/health passes, the compose
	// healthcheck goes green, and the operator gets a file index where the UI
	// should be. Saying so plainly, once, is worth more than any 200 here.
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w,
				"netra: this binary was built without the web UI; run `make ui` before building, or use a released image",
				http.StatusServiceUnavailable)
		})
	}

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

// PublicAssetPaths are the files a browser asks for before it can possibly
// have a session: the tab icon on the login page, the touch and launcher
// icons, and the web manifest. They are served OUTSIDE RequireAdmin, for the
// reason /login-cover.webp already is -- the catch-all answers a signed-out
// browser's asset request with a redirect to the login page, so the login
// page's own icon would 303 and the tab would sit blank on the one screen
// every user meets first.
//
// The manifest has a second reason of its own: a <link rel="manifest"> fetch
// is credential-less by spec unless the tag opts in, so it never carries the
// session cookie and would 303 even for a signed-in user. The alternative is
// crossorigin="use-credentials" in every consuming document, which fixes the
// signed-in case and still leaves the login page without an icon.
//
// Nothing here is secret -- they are the same bytes any visitor to the login
// page can already fetch -- and the list is fixed rather than a prefix, so it
// cannot be widened by dropping a file into ui/public.
//
// Paths are FS-relative (no leading slash), the form the handler compares
// against; NewRouter adds the slash when it mounts them.
var PublicAssetPaths = []string{
	"icon.svg",
	"icon-192.png",
	"icon-512.png",
	"icon-maskable-512.png",
	"apple-touch-icon.png",
	"site.webmanifest",
}

// PublicAssets serves exactly PublicAssetPaths out of the embedded dist/ tree
// and 404s everything else.
//
// The allowlist is enforced here rather than left to the routes NewRouter
// registers, so this handler cannot serve the SPA shell to an anonymous caller
// even if it is later mounted somewhere broader. A missing file is a 404 too,
// which is what a binary built without `make ui` gets: an unbuilt dist/ must
// not fall through to index.html on an unauthenticated route.
func PublicAssets() http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic("web: dist not embedded: " + err.Error())
	}
	return publicAssetsFor(sub)
}

// publicAssetsFor is PublicAssets against an arbitrary fs.FS, so tests can
// exercise it without a real Vite build -- the same split Handler/handlerFor
// uses, and for the same reason.
func publicAssetsFor(sub fs.FS) http.Handler {
	files := http.FileServer(http.FS(sub))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if !slices.Contains(PublicAssetPaths, p) {
			http.NotFound(w, r)
			return
		}
		f, err := sub.Open(p)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		_ = f.Close()

		// A day, matching /login-cover.webp: none of these names is
		// fingerprinted, so replacing an icon leaves the old one in a browser
		// that has it until the entry expires. That is the right trade for a
		// tab icon, and the alternative is refetching all six on every load.
		w.Header().Set("Cache-Control", "public, max-age=86400")
		files.ServeHTTP(w, r)
	})
}
