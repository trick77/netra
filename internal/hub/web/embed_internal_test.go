package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// All behavioural assertions here run against handlerFor and a synthetic
// fs.FS rather than the real embedded dist/ tree. dist/ is tracked only via
// an empty .gitkeep (see the package doc on distFS) so that `go:embed all:dist`
// keeps compiling on a fresh checkout, `make ui` never leaves the working
// tree dirty, and no build artifact is ever committed. That means the real
// embedded FS may legitimately contain nothing at all -- there is no
// fingerprinted asset, and often no real index.html either, to request
// through Handler() itself. See TestHandlerDoesNotPanic below for the one
// assertion that is still made against the real embed.
func testFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":           {Data: []byte("<html>placeholder</html>")},
		"assets/app-abc123.js": {Data: []byte("console.log('hi')")},
	}
}

// A deep link is a client-side route, not a file. The handler must answer it
// with index.html rather than 404, or a reload of /hosts/3/graphs breaks.
func TestHandlerForServesIndexForUnknownPath(t *testing.T) {
	rec := httptest.NewRecorder()
	handlerFor(testFS()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/hosts/3/graphs", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct == "" {
		t.Fatal("no content type")
	}
}

// Fingerprinted assets are immutable and safe to cache forever; a regression
// here means browsers needlessly re-fetch a bundle that never changes.
func TestAssetPathIsCachedImmutably(t *testing.T) {
	rec := httptest.NewRecorder()
	handlerFor(testFS()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/assets/app-abc123.js", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Fatalf("assets Cache-Control = %q, want public, max-age=31536000, immutable", got)
	}
}

// index.html must never be cached, or a deploy never reaches a browser that
// already has the previous build.
func TestIndexPathIsNotCached(t *testing.T) {
	rec := httptest.NewRecorder()
	handlerFor(testFS()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("index Cache-Control = %q, want no-cache", got)
	}
}

// Same assertion as TestIndexPathIsNotCached, driven through the unknown-path
// fallback rather than "/" directly, so both entry points into the no-cache
// branch are covered.
func TestUnknownPathFallbackIsNotCached(t *testing.T) {
	rec := httptest.NewRecorder()
	handlerFor(testFS()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/hosts/3/graphs", nil))

	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("fallback Cache-Control = %q, want no-cache", got)
	}
}

// The only assertion made against the real embedded FS: dist/ may
// legitimately hold nothing but .gitkeep on a fresh checkout (see the package
// doc on distFS), so Handler() must construct successfully and never panic
// even then. Every behavioural case is covered above against a populated
// fstest.MapFS instead.
func TestHandlerDoesNotPanic(t *testing.T) {
	h := Handler()
	if h == nil {
		t.Fatal("Handler() = nil")
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
}

// A binary built without `make ui` must say so rather than serve
// http.FileServer's directory listing of an all-but-empty dist/, which is
// what the fallback produced before: a 200, past every healthcheck, with a
// file index where the UI belongs.
func TestUnbuiltDistIsAnExplicitError(t *testing.T) {
	rec := httptest.NewRecorder()
	handlerFor(fstest.MapFS{".gitkeep": {Data: nil}}).
		ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "make ui") {
		t.Fatalf("body = %q, want it to name `make ui`", rec.Body.String())
	}
}
