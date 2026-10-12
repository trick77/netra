package httpapi

import (
	"net/http"
	"net/http/httptest"

	kitauth "github.com/trick77/ship/kit/auth"

	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
)

// NewSessionCookieForTest mints the cookie a sign-in would: user "" is a
// session from the admin token, anything else a person's from the provider.
func NewSessionCookieForTest(adminToken, user string) *http.Cookie {
	c := kitauth.Claims{Subject: adminTokenSubject}
	if user != "" {
		c = kitauth.Claims{Subject: "sub-" + user, PreferredUsername: user}
	}
	rec := httptest.NewRecorder()
	if err := NewSessions(adminToken).Issue(rec, c); err != nil {
		panic(err)
	}
	return rec.Result().Cookies()[0]
}

// SessionUserForTest is the identity a request's session carries, "" for one
// minted from the admin token, and whether the session is valid at all.
func SessionUserForTest(adminToken string, r *http.Request) (string, bool) {
	c, ok := NewSessions(adminToken).Read(r)
	if !ok || c.Subject == adminTokenSubject {
		return "", ok
	}
	return c.Username(), true
}

// LatestNetTotalsForTest exposes latestNetTotals to the external test
// package. It is a pure function of one post's net samples, which is why its
// edge cases -- several scrapes in one batch, one direction reported and not
// the other -- are ordinary unit tests rather than something needing a
// database.
func LatestNetTotalsForTest(nets []*netrav1.NetSample) (rx, tx *float64) {
	return latestNetTotals(nets)
}
