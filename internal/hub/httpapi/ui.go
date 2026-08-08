package httpapi

import (
	"crypto/subtle"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"time"

	"github.com/trick77/netra/internal/hub/admin"
)

// templateFS carries the UI templates into the binary, the same way
// migrationFS carries the schema. There is no asset directory to deploy and
// no frontend build step.
//
//go:embed templates/*.gohtml
var templateFS embed.FS

// hubURLPlaceholder stands in when NETRA_HUB_URL is unset. The browser
// reaches this hub on loopback, so the request cannot reveal the name agents
// use -- a guess would be worse than an obvious gap.
const hubURLPlaceholder = "https://netra.example.com"

type uiHandler struct {
	svc        *admin.Service
	adminToken string
	hubURL     string

	hosts *template.Template
	token *template.Template
	login *template.Template
}

// NewUIHandler serves the host management page.
//
// It is server-rendered with html/template: no JavaScript, no build step and
// no design system. The phase-2 UI owns those decisions, and prejudging them
// here would be the expensive kind of wrong.
func NewUIHandler(svc *admin.Service, adminToken, hubURL string) http.Handler {
	h := &uiHandler{
		svc:        svc,
		adminToken: adminToken,
		hubURL:     hubURL,
		hosts:      mustParse("hosts.gohtml"),
		token:      mustParse("token.gohtml"),
		login:      mustParse("login.gohtml"),
	}

	// /login is the only route outside RequireAdmin: it is where an
	// unauthenticated browser is sent, so gating it would loop.
	mux := http.NewServeMux()
	mux.Handle("GET /login", http.HandlerFunc(h.loginForm))
	mux.Handle("POST /login", http.HandlerFunc(h.loginSubmit))
	mux.Handle("POST /logout", http.HandlerFunc(h.logout))

	guarded := http.NewServeMux()
	guarded.Handle("GET /{$}", http.HandlerFunc(h.list))
	guarded.Handle("POST /ui/hosts", http.HandlerFunc(h.create))
	guarded.Handle("POST /ui/hosts/{id}/token", http.HandlerFunc(h.rotate))
	guarded.Handle("POST /ui/hosts/{id}/delete", http.HandlerFunc(h.delete))
	mux.Handle("/", RequireAdmin(h.adminToken, true, guarded))

	return mux
}

// mustParse fails at construction rather than on the first request: a broken
// template is a build-time mistake and must not reach a running hub.
func mustParse(name string) *template.Template {
	return template.Must(template.ParseFS(templateFS, "templates/layout.gohtml", "templates/"+name))
}

func (h *uiHandler) loginForm(w http.ResponseWriter, r *http.Request) {
	// An already-valid session has no business seeing the form again.
	if validSession(h.adminToken, r, time.Now()) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	h.render(w, h.login, http.StatusOK, map[string]any{"Title": "Log in"})
}

// loginSubmit is the form post. The submitted value is never echoed back into the
// response: a wrong token is often a right token for something else.
func (h *uiHandler) loginSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.render(w, h.login, http.StatusBadRequest, map[string]any{
			"Title": "Log in", "Error": "Could not read the form.",
		})
		return
	}

	submitted := r.PostFormValue("token")
	if subtle.ConstantTimeCompare([]byte(submitted), []byte(h.adminToken)) != 1 {
		h.render(w, h.login, http.StatusUnauthorized, map[string]any{
			"Title": "Log in", "Error": "That is not the admin token.",
		})
		return
	}

	http.SetCookie(w, newSessionCookie(h.adminToken, time.Now()))
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *uiHandler) logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, SameSite: http.SameSiteStrictMode,
	})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (h *uiHandler) list(w http.ResponseWriter, r *http.Request) {
	hosts, err := h.svc.ListHosts(r.Context())
	if err != nil {
		slog.Error("ui: list hosts", "err", err)
		h.render(w, h.hosts, http.StatusInternalServerError, map[string]any{
			"Title": "Hosts", "Hosts": nil, "Error": "Could not read the host list.",
		})
		return
	}

	h.render(w, h.hosts, http.StatusOK, map[string]any{"Title": "Hosts", "Hosts": hosts})
}

func (h *uiHandler) create(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		h.uiError(w, r, "Could not read the form.")
		return
	}

	hostname := r.PostFormValue("hostname")
	host, token, err := h.svc.CreateHost(r.Context(), hostname, nil)
	if err != nil {
		if errors.Is(err, admin.ErrInvalid) {
			h.uiError(w, r, "A hostname is required.")
			return
		}
		slog.Error("ui: create host", "err", err)
		h.uiError(w, r, "Could not create the host.")
		return
	}

	h.renderToken(w, host.Hostname, token, false)
}

func (h *uiHandler) rotate(w http.ResponseWriter, r *http.Request) {
	id, ok := uiPathID(w, r)
	if !ok {
		return
	}

	token, err := h.svc.RotateToken(r.Context(), id)
	if err != nil {
		if errors.Is(err, admin.ErrNotFound) {
			h.uiError(w, r, "That host no longer exists.")
			return
		}
		slog.Error("ui: rotate token", "err", err)
		h.uiError(w, r, "Could not rotate the token.")
		return
	}

	h.renderToken(w, h.hostnameOf(r, id), token, true)
}

func (h *uiHandler) delete(w http.ResponseWriter, r *http.Request) {
	id, ok := uiPathID(w, r)
	if !ok {
		return
	}

	if err := h.svc.DeleteHost(r.Context(), id); err != nil && !errors.Is(err, admin.ErrNotFound) {
		slog.Error("ui: delete host", "err", err)
		h.uiError(w, r, "Could not delete the host.")
		return
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// hostnameOf resolves a host id to its name for display. A failure here is
// cosmetic -- the token still has to be shown -- so it degrades to the id
// rather than losing the one thing the operator came for.
func (h *uiHandler) hostnameOf(r *http.Request, id int32) string {
	hosts, err := h.svc.ListHosts(r.Context())
	if err != nil {
		return fmt.Sprintf("host %d", id)
	}
	for _, host := range hosts {
		if host.ID == id {
			return host.Hostname
		}
	}
	return fmt.Sprintf("host %d", id)
}

// renderToken shows a freshly minted token exactly once, with the command
// that consumes it. That command is the whole point of the page: setup-agent.sh
// already accepts --token, so the operator copies one line instead of
// transcribing a secret into a flag by hand.
func (h *uiHandler) renderToken(w http.ResponseWriter, hostname, token string, rotated bool) {
	hubURL := h.hubURL
	if hubURL == "" {
		hubURL = hubURLPlaceholder
	}

	h.render(w, h.token, http.StatusOK, map[string]any{
		"Title":    hostname,
		"Hostname": hostname,
		"Token":    token,
		"Rotated":  rotated,
		"SetupCommand": fmt.Sprintf(
			"curl -fsSL %s/setup-agent.sh | sh -s -- \\\n  --hub-url %s --token %s",
			hubURL, hubURL, token),
		"HubURLConfigured": h.hubURL != "",
	})
}

func (h *uiHandler) uiError(w http.ResponseWriter, r *http.Request, message string) {
	hosts, err := h.svc.ListHosts(r.Context())
	if err != nil {
		slog.Error("ui: list hosts while reporting an error", "err", err)
	}
	h.render(w, h.hosts, http.StatusBadRequest, map[string]any{
		"Title": "Hosts", "Hosts": hosts, "Error": message,
	})
}

// render writes a page that must never be cached: one of them carries a
// token, and the rest reflect state that changes under the operator.
func (h *uiHandler) render(w http.ResponseWriter, t *template.Template, status int, data map[string]any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := t.ExecuteTemplate(w, "layout", data); err != nil {
		// The status line is already written, so this can only be logged.
		slog.Error("ui: render", "err", err)
	}
}

func uiPathID(w http.ResponseWriter, r *http.Request) (int32, bool) {
	id, ok := parseID(r.PathValue("id"))
	if !ok {
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, "id must be an integer", http.StatusBadRequest)
		return 0, false
	}
	return id, true
}
