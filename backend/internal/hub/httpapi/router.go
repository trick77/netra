package httpapi

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	kitauth "github.com/trick77/ship/kit/auth"
	"github.com/trick77/ship/kit/httpx"

	"github.com/trick77/netra/internal/hub/admin"
	"github.com/trick77/netra/internal/hub/auth"
	"github.com/trick77/netra/internal/hub/config"
	"github.com/trick77/netra/internal/hub/read"
	"github.com/trick77/netra/internal/hub/store"
	"github.com/trick77/netra/internal/hub/web"
)

// NewRouter builds the hub's route table. Go 1.22 method routing is used
// directly; there is no framework.
//
// Every route below is reachable from the internet: compose.yaml routes the
// whole host through Traefik and publishes no port on the hub box. Nothing but
// the handler's own check stands between a caller and host creation or token
// minting, so RequireAdmin on everything outside /api/agent/ is load-bearing
// rather than defence in depth -- do not mount a route here without deciding,
// explicitly, which credential it answers to.
func NewRouter(a *auth.Authenticator, s *store.Store, cfg config.Config, oidcSvc *kitauth.OIDC) http.Handler {
	svc := admin.NewService(s.Pool())
	rd := read.NewService(s.Pool())

	mux := http.NewServeMux()
	// The one exception to the paragraph above: the probes take no credential,
	// because the container healthcheck runs inside it with none to present.
	// They are not reachable from the internet: kit answers them to a direct
	// local caller only, so through Traefik they are a 404. /healthz is
	// liveness; /readyz pings the database. Their bodies carry no error detail
	// (that goes to the log).
	httpx.Health(mux, slog.Default(), httpx.Check{Name: "database", Run: s.Pool().Ping})
	mux.Handle("POST /api/agent/v1/ingest", NewIngestHandler(a, s))

	// The admin API answers 401 to an unauthenticated caller; the UI sends it
	// to a login page instead. Both accept the same credential.
	mux.Handle("/api/v1/", RequireAdmin(cfg.AdminToken, false, NewAdminHandler(svc, rd, time.Now, cfg.HubURL)))

	// /login and /logout sit outside RequireAdmin: this is where an
	// unauthenticated browser is sent, so gating them would loop.
	//
	// /auth/login and /auth/callback are the OpenID Connect flow and sit here
	// for the same reason: an unauthenticated browser walks through them.
	login := NewLoginHandler(cfg.AdminToken, oidcSvc)
	mux.Handle("GET /login", login)
	mux.Handle("POST /login", login)
	mux.Handle("POST /logout", login)
	mux.Handle("GET /auth/login", login)
	mux.Handle("GET /auth/callback", login)
	// The login page's background art. Registered here rather than left to the
	// catch-all below, which would send a signed-out browser's image request
	// through RequireAdmin and answer it with a redirect to the login page.
	mux.Handle("GET /login-cover.webp", login)

	// The tab icon, the touch and launcher icons, and the web manifest, for
	// the same reason as the line above: a signed-out browser is on the login
	// page and asks for them, and the catch-all would answer each with a
	// redirect to the page it is already on. The handler enforces its own
	// allowlist, so these routes cannot reach anything else in dist/.
	publicAssets := web.PublicAssets()
	for _, p := range web.PublicAssetPaths {
		mux.Handle("GET /"+p, publicAssets)
	}

	// Everything else is the single-page UI, behind the same admin token as
	// the API. redirectToLogin stays true so a browser without a session
	// lands on the login page rather than reading a bare 401 -- the SPA
	// itself routes a 401 the same way, and the two must agree.
	mux.Handle("/", RequireAdmin(cfg.AdminToken, true, web.Handler()))

	// SameSite=Lax still sends the session cookie on a form POST from a
	// sibling subdomain, so a cross-origin browser mutation is refused on
	// every route: the admin API, and /login and /logout too. A request
	// carrying neither Sec-Fetch-Site nor Origin (an agent, curl, netra-sim)
	// passes: it is not a browser, and cannot be riding a cookie. GET is never
	// checked, so the OIDC callback is unaffected.
	//
	// AccessLog outermost so a recovered panic's 500 and a refused cross-origin
	// mutation's 403 both get a line under the request id. Routine lines are
	// debug; the UI's polled reads are dropped even there while they succeed
	// (every open tab re-reads its page each minute), a failing one still logs.
	// A probe never logs here: Health logs readiness changes itself. A refused
	// one, from outside the container, logs as any 404.
	return httpx.Chain(mux,
		httpx.AccessLog(slog.Default(), httpx.AccessLogOptions{
			// Traefik is the one hop in front (compose.yaml publishes no port):
			// the TCP peer is always Traefik.
			ClientIP: func(r *http.Request) string { return httpx.ClientIP(r, 1) },
			Skip:     polledRead,
		}),
		httpx.Recover(slog.Default()),
		httpx.CrossOrigin,
	)
}

// polledRead is a successful GET under /api/v1/: the UI's poll traffic. Its
// mutations (POST, DELETE) and the agents' ingest are not polled reads and keep
// their line.
func polledRead(r *http.Request, status int) bool {
	return status < 400 && r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/")
}
