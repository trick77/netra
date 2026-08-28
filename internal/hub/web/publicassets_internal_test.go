package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

// Same construction as embed_internal_test.go's testFS, and for the same
// reason: the real embedded dist/ holds nothing but .gitkeep on a checkout
// that has not run `make ui`, so every behavioural assertion runs against a
// synthetic tree. index.html is present precisely so the tests below can prove
// it is NOT what an anonymous caller gets.
func publicTestFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":       {Data: []byte("<html>placeholder</html>")},
		"icon.svg":         {Data: []byte("<svg/>")},
		"site.webmanifest": {Data: []byte(`{"name":"netra"}`)},
	}
}

// The whole point of the route: a signed-out browser on the login page must
// get the icon itself, not the redirect the catch-all would answer with.
func TestPublicAssetsServesAnAllowlistedFile(t *testing.T) {
	rec := httptest.NewRecorder()
	publicAssetsFor(publicTestFS()).
		ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/icon.svg", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "<svg/>" {
		t.Fatalf("body = %q, want the icon", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=86400" {
		t.Fatalf("Cache-Control = %q, want public, max-age=86400", got)
	}
}

// The manifest is the second reason this route exists -- its fetch is
// credential-less by spec -- so it is asserted separately from the icon rather
// than assumed to follow it.
func TestPublicAssetsServesTheManifest(t *testing.T) {
	rec := httptest.NewRecorder()
	publicAssetsFor(publicTestFS()).
		ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/site.webmanifest", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != `{"name":"netra"}` {
		t.Fatalf("body = %q, want the manifest", got)
	}
}

// This handler is mounted outside RequireAdmin, so a path it does not own must
// 404 rather than fall through to the SPA shell. index.html exists in the test
// tree, which is what makes this a real assertion: the allowlist is the only
// thing standing between an anonymous caller and it.
func TestPublicAssetsRefusesAnythingElse(t *testing.T) {
	for _, path := range []string{"/index.html", "/", "/assets/app-abc123.js", "/hosts/3/graphs"} {
		rec := httptest.NewRecorder()
		publicAssetsFor(publicTestFS()).
			ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))

		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", path, rec.Code)
		}
	}
}

// An allowlisted name that is not in dist/ -- a binary built without `make ui`,
// or a raster nobody regenerated -- must 404 too. http.FileServer would answer
// a missing file with its own 404, but only after the allowlist has let the
// request through, and the explicit check is what keeps that from depending on
// FileServer's behaviour.
func TestPublicAssetsMissingFileIs404(t *testing.T) {
	rec := httptest.NewRecorder()
	publicAssetsFor(fstest.MapFS{".gitkeep": {Data: nil}}).
		ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/icon.svg", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// The counterpart to TestHandlerDoesNotPanic: PublicAssets constructs against
// the real embed, which may hold nothing at all.
func TestPublicAssetsDoesNotPanic(t *testing.T) {
	h := PublicAssets()
	if h == nil {
		t.Fatal("PublicAssets() = nil")
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/icon.svg", nil))
}
