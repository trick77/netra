package httpapi_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/trick77/netra/internal/hub/httpapi"
)

// Signing, expiry and tamper checks are kit/auth's and tested there; these pin
// what netra configures on top: the name, the key's source and the cookie's
// attributes.

func TestSessionCookieRoundTrips(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(httpapi.NewSessionCookieForTest("s3cret", ""))

	if _, ok := httpapi.SessionUserForTest("s3cret", req); !ok {
		t.Error("a freshly minted cookie did not validate")
	}
}

// The signing key is derived from the admin token, so rotating that token is
// also the way to log every browser out. There is no session store to clear.
func TestSessionCookieFromAnotherAdminTokenIsRejected(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(httpapi.NewSessionCookieForTest("old-token", ""))

	if _, ok := httpapi.SessionUserForTest("new-token", req); ok {
		t.Error("a session survived an admin-token change — rotation must log everyone out")
	}
}

func TestSessionCookieAttributes(t *testing.T) {
	c := httpapi.NewSessionCookieForTest("s3cret", "")

	if c.Name != "netra_session" {
		t.Errorf("name = %q, want netra_session", c.Name)
	}
	if !c.HttpOnly || !c.Secure {
		t.Error("cookie must be HttpOnly and Secure")
	}
	// Lax is what stops a cross-site form post from reaching the
	// state-changing UI routes with a live session attached. Not Strict:
	// browsers withhold a Strict cookie on the provider-initiated navigation
	// out of the OIDC callback, so every first sign-in would land back on the
	// login form.
	if c.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite = %v, want Lax", c.SameSite)
	}
	// A month, nothing renews it.
	if left := time.Until(c.Expires); left < 29*24*time.Hour || left > 31*24*time.Hour {
		t.Errorf("expires in %s, want about 30 days", left)
	}
}

func TestTokenSessionCarriesNoUser(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(httpapi.NewSessionCookieForTest("token", ""))

	user, ok := httpapi.SessionUserForTest("token", req)
	if !ok || user != "" {
		t.Errorf("user, ok = %q, %v; want empty and valid for a token session", user, ok)
	}
}

func TestSignedInSessionCarriesTheUser(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(httpapi.NewSessionCookieForTest("token", "jan"))

	user, ok := httpapi.SessionUserForTest("token", req)
	if !ok || user != "jan" {
		t.Errorf("user, ok = %q, %v; want jan", user, ok)
	}
}

func TestValidSessionRejectsAMalformedCookie(t *testing.T) {
	good := httpapi.NewSessionCookieForTest("s3cret", "").Value
	for _, value := range []string{
		"",
		"no-separator",
		"notbase64.abcdef",
		strings.Replace(good, ".", ".x", 1),
	} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.AddCookie(&http.Cookie{Name: "netra_session", Value: value})

		if _, ok := httpapi.SessionUserForTest("s3cret", req); ok {
			t.Errorf("cookie value %q validated", value)
		}
	}
}
