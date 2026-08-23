package httpapi

import (
	"net/http"
	"time"

	"github.com/trick77/netra/internal/hub/admin"
	"github.com/trick77/netra/internal/hub/auth"
	"github.com/trick77/netra/internal/hub/config"
	"github.com/trick77/netra/internal/hub/oidc"
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
func NewRouter(a *auth.Authenticator, s *store.Store, cfg config.Config, oidcSvc *oidc.Service) http.Handler {
	svc := admin.NewService(s.Pool())
	rd := read.NewService(s.Pool())

	mux := http.NewServeMux()
	// The one deliberate exception to the paragraph above: /api/health
	// answers an unauthenticated caller, from the internet included, because
	// the compose healthcheck wgets it inside the container with no
	// credential to present. Its body is fixed -- status, database
	// reachability, version -- and must stay that way: anything added here is
	// added for anonymous readers.
	mux.Handle("GET /api/health", NewHealthHandler(s))
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

	// Everything else is the single-page UI, behind the same admin token as
	// the API. redirectToLogin stays true so a browser without a session
	// lands on the login page rather than reading a bare 401 -- the SPA
	// itself routes a 401 the same way, and the two must agree.
	mux.Handle("/", RequireAdmin(cfg.AdminToken, true, web.Handler()))

	return mux
}
