package httpapi_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/trick77/netra/internal/hub/httpapi"
)

func TestSessionCookieRoundTrips(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	c := httpapi.NewSessionCookieForTest("s3cret", "", now)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(c)

	if !httpapi.ValidSessionForTest("s3cret", req, now.Add(time.Minute)) {
		t.Error("a freshly minted cookie did not validate")
	}
}

func TestSessionCookieExpires(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	c := httpapi.NewSessionCookieForTest("s3cret", "", now)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(c)

	if httpapi.ValidSessionForTest("s3cret", req, now.Add(13*time.Hour)) {
		t.Error("an expired cookie validated")
	}
}

// The MAC must cover the expiry, not just exist alongside it. A signature over
// a constant would pass the round-trip test above and still let anyone mint a
// permanent session by editing the plaintext half of the cookie.
func TestSessionCookieRejectsATamperedExpiry(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	c := httpapi.NewSessionCookieForTest("s3cret", "", now)

	_, mac, ok := strings.Cut(c.Value, ".")
	if !ok {
		t.Fatalf("cookie value %q has no separator", c.Value)
	}
	c.Value = fmt.Sprintf("%d.%s", now.Add(100*time.Hour).Unix(), mac)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(c)

	if httpapi.ValidSessionForTest("s3cret", req, now) {
		t.Error("a cookie with a rewritten expiry validated — the MAC does not cover it")
	}
}

// The signing key is derived from the admin token, so rotating that token is
// also the way to log every browser out. There is no session store to clear.
func TestSessionCookieFromAnotherAdminTokenIsRejected(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	c := httpapi.NewSessionCookieForTest("old-token", "", now)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(c)

	if httpapi.ValidSessionForTest("new-token", req, now.Add(time.Minute)) {
		t.Error("a session survived an admin-token change — rotation must log everyone out")
	}
}

func TestSessionCookieIsHttpOnlyAndSameSiteLax(t *testing.T) {
	c := httpapi.NewSessionCookieForTest("s3cret", "", time.Unix(1_700_000_000, 0))

	if !c.HttpOnly {
		t.Error("cookie is not HttpOnly — script-readable session")
	}
	// Lax is what stops a cross-site form post from reaching the
	// state-changing UI routes with a live session attached, which is why
	// this stage ships no separate CSRF token. Not Strict: browsers withhold
	// a Strict cookie on the provider-initiated navigation out of the OIDC
	// callback, so every first sign-in would land back on the login form.
	if c.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite = %v, want Lax", c.SameSite)
	}
}

func TestValidSessionWithNoCookie(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	if httpapi.ValidSessionForTest("s3cret", req, time.Unix(1_700_000_000, 0)) {
		t.Error("a request with no cookie validated")
	}
}

func TestValidSessionRejectsAMalformedCookie(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)

	for _, value := range []string{
		"",
		"no-separator",
		"notanumber.abcdef",
		".abcdef",
	} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.AddCookie(&http.Cookie{Name: "netra_session", Value: value})

		if httpapi.ValidSessionForTest("s3cret", req, now) {
			t.Errorf("cookie value %q validated", value)
		}
	}
}
