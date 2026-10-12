package httpapi_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/trick77/netra/internal/hub/httpapi"
)

// noRedirect keeps the 3xx so its Location and Set-Cookie can be asserted,
// rather than following it into a page that proves nothing about the redirect.
func noRedirect(t *testing.T) *http.Client {
	t.Helper()
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
}

func TestOIDCRoutesAre404WhenSignInIsNotConfigured(t *testing.T) {
	// A hub without a provider should look like it has no such endpoint, not
	// like it has a broken one -- a 500 here would read as an outage.
	srv := httptest.NewServer(httpapi.NewLoginHandler("token", nil))
	t.Cleanup(srv.Close)

	for _, path := range []string{"/auth/login", "/auth/callback"} {
		t.Run(path, func(t *testing.T) {
			resp, err := noRedirect(t).Get(srv.URL + path)
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusNotFound {
				t.Errorf("status = %d, want 404", resp.StatusCode)
			}
		})
	}
}

func TestLoginPageOffersOnlyTheTokenFormWhenSignInIsOff(t *testing.T) {
	srv := httptest.NewServer(httpapi.NewLoginHandler("token", nil))
	t.Cleanup(srv.Close)

	resp, err := noRedirect(t).Get(srv.URL + "/login")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	body := string(raw)
	if strings.Contains(body, "/auth/login") {
		t.Error("login page offers sign-in with no provider configured")
	}
	if !strings.Contains(body, `name="token"`) {
		t.Error("login page must always offer the admin token form")
	}
}

func TestCallbackRejectsMissingState(t *testing.T) {
	// Reaching the callback without the cookie minted at /auth/login means the
	// flow did not start here. It must not be completed.
	srv := httptest.NewServer(httpapi.NewLoginHandler("token", nil))
	t.Cleanup(srv.Close)

	resp, err := noRedirect(t).Get(srv.URL + "/auth/callback?code=x&state=y")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	// Without a provider the route is absent entirely, which is the strongest
	// possible rejection.
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
	for _, c := range resp.Cookies() {
		if c.Name == "netra_session" && c.Value != "" {
			t.Fatal("callback minted a session without completing a flow")
		}
	}
}
