package httpapi

import (
	"net/http"
	"time"
)

// NewSessionCookieForTest exposes newSessionCookie to the external test
// package. The clock is a parameter so expiry is asserted exactly rather than
// with a sleep; this file is compiled only into the test binary, so the seam
// cannot reach a production call site.
func NewSessionCookieForTest(adminToken string, now time.Time) *http.Cookie {
	return newSessionCookie(adminToken, now)
}

// ValidSessionForTest exposes validSession to the external test package.
func ValidSessionForTest(adminToken string, r *http.Request, now time.Time) bool {
	return validSession(adminToken, r, now)
}
