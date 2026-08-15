package httpapi

import (
	"net/http"
	"time"

	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
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

// LatestNetTotalsForTest exposes latestNetTotals to the external test
// package. It is a pure function of one post's net samples, which is why its
// edge cases -- several scrapes in one batch, one direction reported and not
// the other -- are ordinary unit tests rather than something needing a
// database.
func LatestNetTotalsForTest(nets []*netrav1.NetSample) (rx, tx *float64) {
	return latestNetTotals(nets)
}
