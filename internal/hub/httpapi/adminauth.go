package httpapi

import (
	"crypto/subtle"
	"net/http"
	"strings"
	"time"
)

// RequireAdmin gates the admin API and the UI on NETRA_ADMIN_TOKEN.
//
// One credential, two carriers: an Authorization: Bearer header for curl and
// scripts, or a session cookie minted from the same token for the browser,
// which cannot set a header on a form post.
//
// redirectToLogin is set for the UI mount only. An API client wants a status
// it can act on; a browser wants somewhere to go.
func RequireAdmin(token string, redirectToLogin bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The header is checked first so an API client never pays for a
		// cookie parse.
		bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		authorized := subtle.ConstantTimeCompare([]byte(bearer), []byte(token)) == 1 ||
			validSession(token, r, time.Now())
		if authorized {
			next.ServeHTTP(w, r)
			return
		}

		// Never cached: the next request may carry a valid credential, and a
		// cached rejection would survive the login.
		w.Header().Set("Cache-Control", "no-store")
		if redirectToLogin {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
}
