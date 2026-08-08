package httpapi_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/trick77/netra/internal/hub/httpapi"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func TestRequireAdminRejectsAMissingCredential(t *testing.T) {
	h := httpapi.RequireAdmin("s3cret", false, okHandler())

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/hosts", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestRequireAdminRejectsAWrongBearer(t *testing.T) {
	h := httpapi.RequireAdmin("s3cret", false, okHandler())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/hosts", nil)
	req.Header.Set("Authorization", "Bearer wrong")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestRequireAdminAcceptsTheRightBearer(t *testing.T) {
	h := httpapi.RequireAdmin("s3cret", false, okHandler())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/hosts", nil)
	req.Header.Set("Authorization", "Bearer s3cret")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestRequireAdminAcceptsAValidSessionCookie(t *testing.T) {
	h := httpapi.RequireAdmin("s3cret", false, okHandler())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/hosts", nil)
	req.AddCookie(httpapi.NewSessionCookieForTest("s3cret", time.Now()))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 with a valid session", rec.Code)
	}
}

func TestRequireAdminRejectsASessionCookieFromAnotherToken(t *testing.T) {
	h := httpapi.RequireAdmin("new-token", false, okHandler())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/hosts", nil)
	req.AddCookie(httpapi.NewSessionCookieForTest("old-token", time.Now()))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

// An API client wants a status it can act on. A browser handed a bare 401 on
// GET / would show a dead end with no way to authenticate.
func TestRequireAdminRedirectsAPageRequestToLogin(t *testing.T) {
	h := httpapi.RequireAdmin("s3cret", true, okHandler())

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/login" {
		t.Errorf("Location = %q, want %q", got, "/login")
	}
}

// A rejected response must not be cached: the next request may carry a valid
// credential, and a cached 401 or redirect would survive the login.
func TestRequireAdminMarksRejectionsNoStore(t *testing.T) {
	h := httpapi.RequireAdmin("s3cret", false, okHandler())

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/hosts", nil))

	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want %q", got, "no-store")
	}
}
