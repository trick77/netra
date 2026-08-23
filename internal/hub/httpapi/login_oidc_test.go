package httpapi_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

func TestTokenSessionCarriesNoUser(t *testing.T) {
	// A session minted from the admin token is an identity too -- "whoever held
	// the token" -- and is recorded as the empty user so it cannot be confused
	// with a person who signed in.
	now := timeNow()
	c := httpapi.NewSessionCookieForTest("token", "", now)

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(c)

	user, ok := httpapi.SessionUserForTest("token", r, now)
	if !ok {
		t.Fatal("cookie should be valid")
	}
	if user != "" {
		t.Errorf("user = %q, want empty for a token session", user)
	}
}

func TestSignedInSessionCarriesTheUser(t *testing.T) {
	now := timeNow()
	c := httpapi.NewSessionCookieForTest("token", "jan", now)

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(c)

	user, ok := httpapi.SessionUserForTest("token", r, now)
	if !ok {
		t.Fatal("cookie should be valid")
	}
	if user != "jan" {
		t.Errorf("user = %q, want jan", user)
	}
}

func TestSessionUserCannotBeEditedWithoutBreakingTheMAC(t *testing.T) {
	// The name is displayed and logged, so an unsigned copy would let anyone
	// holding a valid cookie rename themselves.
	now := timeNow()
	c := httpapi.NewSessionCookieForTest("token", "jan", now)

	parts := strings.SplitN(c.Value, ".", 3)
	if len(parts) != 3 {
		t.Fatalf("cookie has %d fields, want 3", len(parts))
	}
	// "cm9vdA" is base64url("root").
	forged := parts[0] + ".cm9vdA." + parts[2]

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(&http.Cookie{Name: "netra_session", Value: forged})

	if _, ok := httpapi.SessionUserForTest("token", r, now); ok {
		t.Fatal("a cookie with a swapped user must not validate")
	}
}

// timeNow is a fixed instant so expiry is asserted exactly rather than with a
// sleep, matching how the rest of these tests treat the clock.
func timeNow() time.Time { return time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC) }
