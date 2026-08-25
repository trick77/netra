package httpapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
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

// sessionKey derives the cookie-signing key from the admin token.
//
// Deriving rather than configuring means there is no second secret to manage,
// and it buys a property worth having: changing BACKEND_ADMIN_TOKEN invalidates
// every session issued under the old one, because the verification key moves
// with it. There is no session table to clear and no server-side state.
//
// The separator keeps this derivation distinct from any other use of the same
// token as key material.
func sessionKey(adminToken string) []byte {
	sum := sha256.Sum256([]byte("netra-session\x00" + adminToken))
	return sum[:]
}

// sign returns the MAC over an expiry timestamp and the user it was issued to.
//
// The user is signed, not merely carried: an unsigned copy would let anyone
// holding a valid cookie rename themselves before anything reads it. Nothing
// production-side reads it yet -- validSession discards the name, and the
// sign-in log takes its username straight off the exchange, not off the cookie
// -- so this binds the field ahead of the first reader rather than after it.
// Empty means the session came from the admin token,
// which is an identity too -- "whoever held the token" -- and binding it stops
// a token session being edited into someone else's.
//
// The NUL separator keeps the two fields unambiguous: without it, expiry 12 and
// user "3x" would sign identically to expiry 123 and user "x".
func sign(key []byte, expiry int64, user string) string {
	m := hmac.New(sha256.New, key)
	fmt.Fprintf(m, "%d\x00%s", expiry, user)
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// newSessionCookie mints a session cookie valid for sessionTTL from now.
//
// user is the signed-in identity, or empty for a session minted from the admin
// token. It is base64url-encoded so it cannot contain the "." that separates
// the cookie's fields, whatever the provider chose to call someone.
//
// now is a parameter rather than a call to time.Now so expiry is tested
// exactly rather than with a sleep.
func newSessionCookie(adminToken, user string, now time.Time) *http.Cookie {
	expires := now.Add(sessionTTL)
	expiry := expires.Unix()

	return &http.Cookie{
		Name: sessionCookieName,
		Value: fmt.Sprintf("%d.%s.%s",
			expiry,
			base64.RawURLEncoding.EncodeToString([]byte(user)),
			sign(sessionKey(adminToken), expiry, user)),
		Path: "/",
		// Secure, because the UI now moves behind TLS: Traefik fronts the
		// whole hub on BACKEND_HOSTNAME's websecure entrypoint and the
		// container publishes no host port, so there is no plain-HTTP
		// deployment left for this cookie to be needed on. Without the flag
		// a browser holding a session sends it in cleartext the moment
		// someone types http://<hostname>/ -- before any redirect to https
		// can answer.
		//
		// HttpOnly keeps the session out of script. SameSite=Lax is what stops
		// a cross-site form post from reaching the state-changing UI routes
		// with a live session attached, which is why this stage ships no
		// separate CSRF token: Lax withholds the cookie from every cross-site
		// request except a top-level GET navigation.
		//
		// Not Strict, which browsers do not send on a navigation the identity
		// provider initiated -- including the redirect out of the OIDC
		// callback that has just set this cookie. A sign-in that succeeded
		// would land back on the login form until the user reloaded by hand.
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  expires,
	}
}

// sessionUser returns the identity a valid session cookie was issued to, and
// whether the cookie is valid at all. An empty user with ok true is a session
// minted from the admin token.
//
// Only validSession calls this today, and it keeps the boolean and drops the
// name. The identity is carried and signed so that whatever displays or records
// it next can trust it, not because something already does.
//
// Sessions issued before the cookie carried an identity have two fields rather
// than three and fail here. That is deliberate: they end at the deploy that
// adds sign-in, which costs one re-login and avoids carrying a second cookie
// format forever.
func sessionUser(adminToken string, r *http.Request, now time.Time) (string, bool) {
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return "", false
	}

	rawExpiry, rest, ok := strings.Cut(c.Value, ".")
	if !ok {
		return "", false
	}
	rawUser, mac, ok := strings.Cut(rest, ".")
	if !ok {
		return "", false
	}
	expiry, err := strconv.ParseInt(rawExpiry, 10, 64)
	if err != nil {
		return "", false
	}
	user, err := base64.RawURLEncoding.DecodeString(rawUser)
	if err != nil {
		return "", false
	}

	// Verified before either field is trusted: the MAC is the only thing making
	// the expiry unforgeable and the name authentic.
	want := sign(sessionKey(adminToken), expiry, string(user))
	if subtle.ConstantTimeCompare([]byte(mac), []byte(want)) != 1 {
		return "", false
	}
	if now.Unix() >= expiry {
		return "", false
	}

	return string(user), true
}

// validSession reports whether the request carries an unexpired, correctly
// signed session cookie, regardless of which credential minted it.
func validSession(adminToken string, r *http.Request, now time.Time) bool {
	_, ok := sessionUser(adminToken, r, now)
	return ok
}
