package httpapi

import (
	"net/http"
	"time"

	kitauth "github.com/trick77/ship/kit/auth"
)

// sessionCookieName is the cookie the UI authenticates with. The admin API
// accepts a bearer header instead; a browser cannot send one on a form post.
const sessionCookieName = "netra_session"

// sessionTTL bounds how long a browser stays logged in without signing in
// again -- with the admin token, or through the identity provider.
//
// A month, because nothing renews it: the cookie is minted once and never
// re-issued on use, so the TTL is the whole session and a short one means
// signing in again on a schedule rather than after going idle. The provider
// cannot set this for us -- the code flow ends at the callback, netra requests
// no refresh token, and a hub with no issuer configured mints the same cookie
// from the admin token. Rotating BACKEND_ADMIN_TOKEN still invalidates every
// session at once, which is what keeps a long window affordable.
const sessionTTL = 30 * 24 * time.Hour

// adminTokenSubject is the identity of a session minted from the admin token
// rather than by a person: "whoever held the token".
const adminTokenSubject = "admin-token"

// NewSessions signs session cookies with a key derived from the admin token.
//
// Deriving rather than configuring means there is no second secret to manage,
// and changing BACKEND_ADMIN_TOKEN invalidates every session issued under the
// old one. There is no session table to clear and no server-side state.
//
// Secure, because the hub sits behind TLS: Traefik fronts it on
// BACKEND_HOSTNAME's websecure entrypoint and the container publishes no host
// port, so there is no plain-HTTP deployment for this cookie to be needed on.
func NewSessions(adminToken string) *kitauth.Sessions {
	s, err := kitauth.NewSessions(kitauth.SessionConfig{
		Name:   sessionCookieName,
		Key:    kitauth.DeriveKey(adminToken, "netra-session"),
		TTL:    sessionTTL,
		Secure: true,
	})
	if err != nil {
		// Unreachable: the name is fixed and DeriveKey returns 32 bytes.
		panic("httpapi: session config: " + err.Error())
	}
	return s
}

// validSession reports whether the request carries an unexpired, correctly
// signed session cookie, regardless of which credential minted it.
func validSession(s *kitauth.Sessions, r *http.Request) bool {
	_, ok := s.Read(r)
	return ok
}
