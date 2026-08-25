package httpapi

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"time"

	"github.com/trick77/netra/internal/hub/oidc"
)

// Transient cookies holding the CSRF state and replay nonce for one sign-in.
//
// SameSite=Lax, unlike the session cookie's Strict: the callback is a top-level
// navigation arriving from the identity provider's origin, and Strict would
// withhold these exactly when they are needed, turning every login into a state
// mismatch.
const (
	oidcStateCookie = "netra_oidc_state"
	oidcNonceCookie = "netra_oidc_nonce"
	oidcFlowTTL     = 10 * time.Minute
)

// templateFS carries the login page into the binary, the same way migrationFS
// carries the schema. It is all that is left of the server-rendered UI: the
// phase-2 SPA (internal/hub/web) owns every other page now.
//
//go:embed templates/layout.gohtml templates/login.gohtml
var templateFS embed.FS

// loginCover is the sign-in screen's background art, carried in the binary for
// the same reason the templates are: the login page is what an unauthenticated
// browser sees, so everything it needs must be servable before the SPA bundle
// is reachable and without a static directory to deploy alongside the hub.
//
// 71 KB of webp. It is decoration and nothing reads it, so the page states an
// empty alt and the handler below is the only route outside RequireAdmin that
// serves bytes rather than HTML.
//
//go:embed assets/login-cover.webp
var loginCover []byte

// loginHandler serves the no-JS login page and the session it mints.
//
// It stays server-rendered on purpose. The SPA posts to this same endpoint --
// same form encoding, same cookie -- so the page below is the fallback for a
// browser that never ran the bundle, and the one page that must work when the
// rest of the UI cannot.
type loginHandler struct {
	adminToken string
	login      *template.Template

	// oidc is nil when BACKEND_OIDC_ISSUER is unset. Every OIDC route checks it
	// and 404s rather than 500s: a hub without sign-in configured should look
	// like it has no such endpoint, not like it has a broken one.
	oidc *oidc.Service
}

// NewLoginHandler returns the /login and /logout routes. They sit OUTSIDE
// RequireAdmin: this is where an unauthenticated browser is sent, so gating
// them would loop.
func NewLoginHandler(adminToken string, svc *oidc.Service) http.Handler {
	h := &loginHandler{
		adminToken: adminToken,
		login: template.Must(template.ParseFS(templateFS,
			"templates/layout.gohtml", "templates/login.gohtml")),
		oidc: svc,
	}

	mux := http.NewServeMux()
	mux.Handle("GET /login", http.HandlerFunc(h.form))
	mux.Handle("POST /login", http.HandlerFunc(h.submit))
	mux.Handle("POST /logout", http.HandlerFunc(h.logout))
	mux.Handle("GET /auth/login", http.HandlerFunc(h.oidcStart))
	mux.Handle("GET /auth/callback", http.HandlerFunc(h.oidcCallback))
	mux.Handle("GET /login-cover.webp", http.HandlerFunc(h.cover))
	return mux
}

// cover serves the sign-in background.
//
// ServeContent rather than a bare Write so a conditional or ranged request is
// answered properly; the zero modtime leaves Last-Modified off, which is right
// for bytes that only ever change with the binary. The cache is long and the
// name is not fingerprinted, so replacing the art means the old one lingers in
// a browser that has it -- acceptable for a background nobody navigates to,
// and the alternative is refetching it on every sign-in.
func (h *loginHandler) cover(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/webp")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeContent(w, r, "login-cover.webp", time.Time{}, bytes.NewReader(loginCover))
}

// oidcStart mints the state and nonce, then sends the browser to the provider.
func (h *loginHandler) oidcStart(w http.ResponseWriter, r *http.Request) {
	if h.oidc == nil {
		http.NotFound(w, r)
		return
	}

	state, err := randomToken()
	if err == nil {
		var nonce string
		if nonce, err = randomToken(); err == nil {
			http.SetCookie(w, flowCookie(oidcStateCookie, state))
			http.SetCookie(w, flowCookie(oidcNonceCookie, nonce))
			http.Redirect(w, r, h.oidc.AuthCodeURL(state, nonce), http.StatusSeeOther)
			return
		}
	}

	// Failing to read random bytes is not a user error and must not be
	// presented as one -- retrying will not help.
	slog.Error("login: mint oidc flow", "err", err)
	h.render(w, http.StatusInternalServerError, map[string]any{
		"Title": "Log in", "Error": "Could not start sign-in.", "OIDC": true,
	})
}

// oidcCallback completes the flow: check state, exchange the code, mint the
// session. Every failure renders the login page with a generic message; the
// detail goes to the log, because a caller who reached here with a bad state is
// as likely probing as mistaken.
func (h *loginHandler) oidcCallback(w http.ResponseWriter, r *http.Request) {
	if h.oidc == nil {
		http.NotFound(w, r)
		return
	}

	fail := func(msg string, err error) {
		slog.Warn("login: oidc callback", "reason", msg, "err", err)
		clearCookie(w, oidcStateCookie)
		clearCookie(w, oidcNonceCookie)
		h.render(w, http.StatusUnauthorized, map[string]any{
			"Title": "Log in", "Error": "Sign-in did not complete.", "OIDC": true,
		})
	}

	// A provider that refuses is not an error to debug -- the user was denied.
	if e := r.URL.Query().Get("error"); e != "" {
		fail("provider returned error: "+e, nil)
		return
	}

	stateCookie, err := r.Cookie(oidcStateCookie)
	if err != nil {
		fail("no state cookie", err)
		return
	}
	nonceCookie, err := r.Cookie(oidcNonceCookie)
	if err != nil {
		fail("no nonce cookie", err)
		return
	}
	// Constant time: the state is a secret for the length of one login.
	if subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("state")), []byte(stateCookie.Value)) != 1 {
		fail("state mismatch", nil)
		return
	}

	identity, err := h.oidc.Exchange(r.Context(), r.URL.Query().Get("code"), nonceCookie.Value)
	if err != nil {
		if errors.Is(err, oidc.ErrNonceMismatch) {
			fail("nonce mismatch (replay?)", err)
			return
		}
		fail("exchange", err)
		return
	}

	// Single-use: leaving them set would let a captured callback URL be
	// replayed for as long as the cookies lived.
	clearCookie(w, oidcStateCookie)
	clearCookie(w, oidcNonceCookie)

	slog.Info("login: oidc sign-in", "user", identity.Username(), "subject", identity.Subject)
	http.SetCookie(w, newSessionCookie(h.adminToken, identity.Username(), time.Now()))
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func flowCookie(name, value string) *http.Cookie {
	return &http.Cookie{
		Name: name, Value: value, Path: "/",
		MaxAge:   int(oidcFlowTTL.Seconds()),
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
}

func clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: "", Path: "/", MaxAge: -1,
		Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
}

func (h *loginHandler) form(w http.ResponseWriter, r *http.Request) {
	// An already-valid session has no business seeing the form again.
	if validSession(h.adminToken, r, time.Now()) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	h.render(w, http.StatusOK, map[string]any{"Title": "Log in", "OIDC": h.oidc != nil})
}

// submit is the form post. The submitted value is never echoed back into the
// response: a wrong token is often a right token for something else.
func (h *loginHandler) submit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.render(w, http.StatusBadRequest, map[string]any{
			"Title": "Log in", "Error": "Could not read the form.", "OIDC": h.oidc != nil,
		})
		return
	}

	submitted := r.PostFormValue("token")
	if subtle.ConstantTimeCompare([]byte(submitted), []byte(h.adminToken)) != 1 {
		h.render(w, http.StatusUnauthorized, map[string]any{
			"Title": "Log in", "Error": "That is not the admin token.", "OIDC": h.oidc != nil,
		})
		return
	}

	// Empty user: this session was minted by the token, not by a person.
	http.SetCookie(w, newSessionCookie(h.adminToken, "", time.Now()))
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *loginHandler) logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1,
		// Secure to match the cookie this clears: attributes that differ
		// from the ones it was set with are not a reliable deletion.
		Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode,
	})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// The template is executed into a buffer before anything is written.
// Executing straight to the ResponseWriter commits the status line on the
// first byte, so a template that fails halfway leaves a truncated page that
// cannot be corrected.
func (h *loginHandler) render(w http.ResponseWriter, status int, data map[string]any) {
	var buf bytes.Buffer
	if err := h.login.ExecuteTemplate(&buf, "layout", data); err != nil {
		slog.Error("login: render", "err", err)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, "the page could not be rendered", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if _, err := buf.WriteTo(w); err != nil {
		slog.Error("login: write response", "err", err)
	}
}
