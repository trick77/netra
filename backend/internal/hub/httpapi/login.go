package httpapi

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"time"

	kitauth "github.com/trick77/ship/kit/auth"
	"github.com/trick77/ship/kit/httpx"
	"github.com/trick77/ship/kit/secret"
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

// maxLoginBodyBytes caps the login form. It holds one token field; without
// this the unauthenticated POST would read up to the router's 4 MiB before the
// token is even checked. config.Load bounds the admin token so that the
// longest one fits even with every byte url-encoded as %XX.
const maxLoginBodyBytes = 8 << 10

// loginHandler serves the no-JS login page and the session it mints.
//
// It stays server-rendered on purpose. The SPA posts to this same endpoint --
// same form encoding, same cookie -- so the page below is the fallback for a
// browser that never ran the bundle, and the one page that must work when the
// rest of the UI cannot.
type loginHandler struct {
	adminToken string
	sessions   *kitauth.Sessions
	login      *template.Template

	// oidc is nil when BACKEND_OIDC_ISSUER is unset. Every OIDC route checks it
	// and 404s rather than 500s: a hub without sign-in configured should look
	// like it has no such endpoint, not like it has a broken one.
	oidc *kitauth.OIDC
}

// NewLoginHandler returns the /login and /logout routes. They sit OUTSIDE
// RequireAdmin: this is where an unauthenticated browser is sent, so gating
// them would loop.
func NewLoginHandler(adminToken string, svc *kitauth.OIDC) http.Handler {
	h := &loginHandler{
		adminToken: adminToken,
		sessions:   NewSessions(adminToken),
		login: template.Must(template.ParseFS(templateFS,
			"templates/layout.gohtml", "templates/login.gohtml")),
		oidc: svc,
	}

	mux := http.NewServeMux()
	mux.Handle("GET /login", http.HandlerFunc(h.form))
	mux.Handle("POST /login", httpx.MaxBody(maxLoginBodyBytes)(http.HandlerFunc(h.submit)))
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

// oidcStart sends the browser to the provider, the flow's state, nonce and
// PKCE verifier in transient cookies (netra_oidc_*).
func (h *loginHandler) oidcStart(w http.ResponseWriter, r *http.Request) {
	if h.oidc == nil {
		http.NotFound(w, r)
		return
	}
	h.oidc.Start(w, r)
}

// oidcCallback completes the flow and mints the session. Every failure renders
// the login page with a generic message; the detail goes to the log, because a
// caller who reached here with a bad state is as likely probing as mistaken.
func (h *loginHandler) oidcCallback(w http.ResponseWriter, r *http.Request) {
	if h.oidc == nil {
		http.NotFound(w, r)
		return
	}

	claims, err := h.oidc.Callback(w, r)
	if err == nil {
		err = h.sessions.Issue(w, claims)
	}
	if err != nil {
		slog.Warn("login: oidc callback", "err", err)
		h.fail(w, r, http.StatusUnauthorized, "Sign-in did not complete.")
		return
	}

	slog.Info("login: oidc sign-in", "user", claims.Username(), "subject", claims.Subject)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *loginHandler) form(w http.ResponseWriter, r *http.Request) {
	// An already-valid session has no business seeing the form again.
	if validSession(h.sessions, r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	h.render(w, r, http.StatusOK, map[string]any{"Title": "Log in", "OIDC": h.oidc != nil})
}

// submit is the form post. The submitted value is never echoed back into the
// response: a wrong token is often a right token for something else.
func (h *loginHandler) submit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			h.fail(w, r, http.StatusRequestEntityTooLarge, "The form is too large.")
			return
		}
		h.fail(w, r, http.StatusBadRequest, "Could not read the form.")
		return
	}

	submitted := r.PostFormValue("token")
	if !secret.Match(submitted, h.adminToken) {
		h.fail(w, r, http.StatusUnauthorized, "That is not the admin token.")
		return
	}

	// Minted by the token, not by a person.
	if err := h.sessions.Issue(w, kitauth.Claims{Subject: adminTokenSubject}); err != nil {
		httpx.ServerError(w, r, fmt.Errorf("login: issue session: %w", err), "could not sign in")
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *loginHandler) logout(w http.ResponseWriter, r *http.Request) {
	h.sessions.Clear(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// fail renders the login page again with msg.
func (h *loginHandler) fail(w http.ResponseWriter, r *http.Request, status int, msg string) {
	h.render(w, r, status, map[string]any{"Title": "Log in", "Error": msg, "OIDC": h.oidc != nil})
}

// The template is executed into a buffer before anything is written.
// Executing straight to the ResponseWriter commits the status line on the
// first byte, so a template that fails halfway leaves a truncated page that
// cannot be corrected.
func (h *loginHandler) render(w http.ResponseWriter, r *http.Request, status int, data map[string]any) {
	var buf bytes.Buffer
	if err := h.login.ExecuteTemplate(&buf, "layout", data); err != nil {
		httpx.ServerError(w, r, fmt.Errorf("login: render: %w", err), "the page could not be rendered")
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if _, err := buf.WriteTo(w); err != nil {
		slog.ErrorContext(r.Context(), "login: write response", "err", err)
	}
}
