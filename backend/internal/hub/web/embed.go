// Package web serves the single-page UI out of the hub binary.
//
// The SPA is built by `make fe-build` into dist/ and embedded here, the same way
// migrations and the login templates are: one binary, no asset directory to
// deploy, no separate web server. Serving is kit/spa's.
package web

import (
	"embed"
	"io/fs"
	"net/http"

	"github.com/trick77/ship/kit/spa"
)

// distFS holds the built SPA. dist/ is generated and .gitignore excludes its
// contents, keeping only a tracked, empty dist/.gitkeep. That file exists
// solely so `//go:embed all:dist` -- resolved at compile time -- has
// something to embed on a fresh checkout; without it `go build ./...` and
// `go test ./...` fail outright with "pattern all:dist: no matching files
// found". A real index.html is deliberately NOT tracked here: `make fe-build`
// would overwrite it on every build, leaving the working tree permanently
// dirty.
//
//go:embed all:dist
var distFS embed.FS

// unbuilt is what a binary built without the UI answers app routes with (503),
// instead of a directory listing that every healthcheck reads as fine.
var unbuilt = []byte("<!doctype html><title>netra</title><p>netra: this binary was built without the web UI; " +
	"run <code>make fe-build</code> before building, or use a released image.</p>\n")

// Handler serves the SPA. Every path that is not a real file gets the shell,
// because the router owns those paths client-side: a reload of
// /hosts/3/graphs must not 404, and the diagnosis drawer deep-links into tabs.
func Handler() http.Handler { return handlerFor(dist()) }

func handlerFor(sub fs.FS) http.Handler {
	return spa.Handler(sub, spa.Options{Placeholder: unbuilt})
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
// session cookie and would 303 even for a signed-in user.
//
// Nothing here is secret, and the list is fixed rather than a prefix, so it
// cannot be widened by dropping a file into ui/public. FS-relative (no leading
// slash); NewRouter adds the slash when it mounts them.
var PublicAssetPaths = []string{
	"icon.svg",
	"icon-192.png",
	"icon-512.png",
	"icon-maskable-512.png",
	"apple-touch-icon.png",
	"site.webmanifest",
}

// PublicAssets serves exactly PublicAssetPaths out of the embedded dist/ tree
// and 404s everything else, a missing file included.
func PublicAssets() http.Handler { return publicAssetsFor(dist()) }

func publicAssetsFor(sub fs.FS) http.Handler { return spa.Files(sub, PublicAssetPaths...) }

func dist() fs.FS {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic("web: dist not embedded: " + err.Error())
	}
	return sub
}
