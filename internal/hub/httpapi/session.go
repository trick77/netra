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

// sessionTTL bounds how long a browser stays logged in without re-entering
// the admin token.
const sessionTTL = 12 * time.Hour

// sessionKey derives the cookie-signing key from the admin token.
//
// Deriving rather than configuring means there is no second secret to manage,
// and it buys a property worth having: changing NETRA_ADMIN_TOKEN invalidates
// every session issued under the old one, because the verification key moves
// with it. There is no session table to clear and no server-side state.
//
// The separator keeps this derivation distinct from any other use of the same
// token as key material.
func sessionKey(adminToken string) []byte {
	sum := sha256.Sum256([]byte("netra-session\x00" + adminToken))
	return sum[:]
}

// sign returns the MAC over an expiry timestamp. The expiry is the entire
// signed payload: a cookie carries no identity beyond "whoever held the admin
// token at this moment", so there is nothing else to bind.
func sign(key []byte, expiry int64) string {
	m := hmac.New(sha256.New, key)
	fmt.Fprintf(m, "%d", expiry)
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// newSessionCookie mints a session cookie valid for sessionTTL from now.
//
// now is a parameter rather than a call to time.Now so expiry is tested
// exactly rather than with a sleep.
func newSessionCookie(adminToken string, now time.Time) *http.Cookie {
	expires := now.Add(sessionTTL)
	expiry := expires.Unix()

	return &http.Cookie{
		Name:  sessionCookieName,
		Value: fmt.Sprintf("%d.%s", expiry, sign(sessionKey(adminToken), expiry)),
		Path:  "/",
		// Secure, because the UI now moves behind TLS: Traefik fronts the
		// whole hub on NETRA_HOSTNAME's websecure entrypoint and the
		// container publishes no host port, so there is no plain-HTTP
		// deployment left for this cookie to be needed on. Without the flag
		// a browser holding a session sends it in cleartext the moment
		// someone types http://<hostname>/ -- before any redirect to https
		// can answer.
		//
		// HttpOnly keeps the session out of script. SameSite=Strict is what
		// stops a cross-site form post from reaching the state-changing UI
		// routes with a live session attached, which is why this stage ships
		// no separate CSRF token.
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Expires:  expires,
	}
}

// validSession reports whether the request carries an unexpired session
// cookie signed by the current admin token.
func validSession(adminToken string, r *http.Request, now time.Time) bool {
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return false
	}

	rawExpiry, mac, ok := strings.Cut(c.Value, ".")
	if !ok {
		return false
	}
	expiry, err := strconv.ParseInt(rawExpiry, 10, 64)
	if err != nil {
		return false
	}

	// Verified before the expiry is trusted: the MAC is the only thing making
	// that number unforgeable.
	want := sign(sessionKey(adminToken), expiry)
	if subtle.ConstantTimeCompare([]byte(mac), []byte(want)) != 1 {
		return false
	}

	return now.Unix() < expiry
}
