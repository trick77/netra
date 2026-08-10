package web_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/trick77/netra/internal/hub/web"
)

// A deep link is a client-side route, not a file. The handler must answer it
// with index.html rather than 404, or a reload of /hosts/3/graphs breaks.
func TestHandlerServesIndexForUnknownPath(t *testing.T) {
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/hosts/3/graphs", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct == "" {
		t.Fatal("no content type")
	}
}

// index.html must never be cached, or a deploy never reaches a browser that
// already has the previous build.
//
// The assets/ immutable-caching half of this behaviour is covered separately
// in embed_internal_test.go, against a synthetic fs.FS: the real embedded
// dist/ tree only ever contains the committed placeholder index.html in this
// package's test environment, so there is no fingerprinted asset here to
// request.
func TestIndexIsNeverCached(t *testing.T) {
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("index Cache-Control = %q, want no-cache", got)
	}
}
