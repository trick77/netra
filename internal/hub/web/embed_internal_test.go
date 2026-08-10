package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

// handlerFor is exercised directly (rather than through Handler) against a
// synthetic fs.FS, because the real embedded dist/ tree in this package's
// test environment only ever contains the committed placeholder index.html —
// there is no fingerprinted asset to request through Handler(). A fake
// fs.MapFS with both an index.html and an assets/ file lets both branches of
// the routing logic run.
func testFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":           {Data: []byte("<html>placeholder</html>")},
		"assets/app-abc123.js": {Data: []byte("console.log('hi')")},
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
// already has the previous build. Covered again here, alongside the assets
// case, so both halves of the caching behaviour sit next to each other and
// exercise the exact same handler construction.
func TestIndexPathIsNotCached(t *testing.T) {
	rec := httptest.NewRecorder()
	handlerFor(testFS()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("index Cache-Control = %q, want no-cache", got)
	}
}
