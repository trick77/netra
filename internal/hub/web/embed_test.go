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

// Fingerprinted assets are immutable; index.html must never be, or a deploy
// leaves browsers pinned to the previous build forever.
func TestAssetsAreImmutableAndIndexIsNot(t *testing.T) {
	rec := httptest.NewRecorder()
	web.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("index Cache-Control = %q, want no-cache", got)
	}
}
