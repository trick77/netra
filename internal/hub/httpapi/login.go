package httpapi

import (
	"bytes"
	"crypto/subtle"
	"embed"
	"html/template"
	"log/slog"
	"net/http"
	"time"
)

// templateFS carries the login page into the binary, the same way migrationFS
// carries the schema. It is all that is left of the server-rendered UI: the
// phase-2 SPA (internal/hub/web) owns every other page now.
//
//go:embed templates/layout.gohtml templates/login.gohtml
var templateFS embed.FS

// loginHandler serves the no-JS login page and the session it mints.
//
// It stays server-rendered on purpose. The SPA posts to this same endpoint --
// same form encoding, same cookie -- so the page below is the fallback for a
// browser that never ran the bundle, and the one page that must work when the
// rest of the UI cannot.
type loginHandler struct {
	adminToken string
	login      *template.Template
}

// NewLoginHandler returns the /login and /logout routes. They sit OUTSIDE
// RequireAdmin: this is where an unauthenticated browser is sent, so gating
// them would loop.
func NewLoginHandler(adminToken string) http.Handler {
	h := &loginHandler{
		adminToken: adminToken,
		login: template.Must(template.ParseFS(templateFS,
			"templates/layout.gohtml", "templates/login.gohtml")),
	}

	mux := http.NewServeMux()
	mux.Handle("GET /login", http.HandlerFunc(h.form))
	mux.Handle("POST /login", http.HandlerFunc(h.submit))
	mux.Handle("POST /logout", http.HandlerFunc(h.logout))
	return mux
}

func (h *loginHandler) form(w http.ResponseWriter, r *http.Request) {
	// An already-valid session has no business seeing the form again.
	if validSession(h.adminToken, r, time.Now()) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	h.render(w, http.StatusOK, map[string]any{"Title": "Log in"})
}

// submit is the form post. The submitted value is never echoed back into the
// response: a wrong token is often a right token for something else.
func (h *loginHandler) submit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.render(w, http.StatusBadRequest, map[string]any{
			"Title": "Log in", "Error": "Could not read the form.",
		})
		return
	}

	submitted := r.PostFormValue("token")
	if subtle.ConstantTimeCompare([]byte(submitted), []byte(h.adminToken)) != 1 {
		h.render(w, http.StatusUnauthorized, map[string]any{
			"Title": "Log in", "Error": "That is not the admin token.",
		})
		return
	}

	http.SetCookie(w, newSessionCookie(h.adminToken, time.Now()))
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *loginHandler) logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, SameSite: http.SameSiteStrictMode,
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
