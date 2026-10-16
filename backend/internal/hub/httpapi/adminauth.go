package httpapi

import (
	"net/http"

	"github.com/trick77/ship/kit/httpx"
	"github.com/trick77/ship/kit/secret"
)

// RequireAdmin gates the admin API and the UI on BACKEND_ADMIN_TOKEN.
//
// One credential, two carriers: an Authorization: Bearer header for curl and
// scripts, or a session cookie minted from the same token for the browser,
// which cannot set a header on a form post.
//
// redirectToLogin is set for the UI mount only. An API client wants a status
// it can act on; a browser wants somewhere to go.
func RequireAdmin(token string, redirectToLogin bool, next http.Handler) http.Handler {
	sessions := NewSessions(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// An empty configured token must deny everything: config.Load rejects an
		// empty BACKEND_ADMIN_TOKEN, but NewRouter takes a Config by value and
		// test code builds literals directly, so that guard is one struct
		// literal away from being bypassed. secret.Match refuses an empty want
		// on its own; the session path needs the guard here.
		//
		// httpx.Bearer is the ingest path's parser too, so the hub has ONE
		// definition of a valid Authorization header: a bare
		// `Authorization: <token>` is no bearer.
		presented, _ := httpx.Bearer(r)
		authorized := token != "" && (secret.Match(presented, token) || validSession(sessions, r))
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
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
	})
}
