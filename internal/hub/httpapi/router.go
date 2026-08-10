package httpapi

import (
	"net/http"
	"time"

	"github.com/trick77/netra/internal/hub/admin"
	"github.com/trick77/netra/internal/hub/auth"
	"github.com/trick77/netra/internal/hub/config"
	"github.com/trick77/netra/internal/hub/read"
	"github.com/trick77/netra/internal/hub/store"
)

// NewRouter builds the hub's route table. Go 1.22 method routing is used
// directly; there is no framework.
//
// Only /api/agent/ is routed from the internet -- see the Traefik PathPrefix
// in compose.yaml -- and the published port is bound to 127.0.0.1. Everything
// mounted below outside that prefix is reachable on the hub host alone, and
// is additionally gated on NETRA_ADMIN_TOKEN. Widening that PathPrefix would
// publish host creation and token minting with no other visible change.
func NewRouter(a *auth.Authenticator, s *store.Store, cfg config.Config) http.Handler {
	svc := admin.NewService(s.Pool())
	rd := read.NewService(s.Pool())

	mux := http.NewServeMux()
	mux.Handle("GET /api/health", NewHealthHandler(s))
	mux.Handle("POST /api/agent/v1/ingest", NewIngestHandler(a, s))

	// The admin API answers 401 to an unauthenticated caller; the UI sends it
	// to a login page instead. Both accept the same credential.
	mux.Handle("/api/v1/", RequireAdmin(cfg.AdminToken, false, NewAdminHandler(svc, rd, time.Now)))
	mux.Handle("/", NewUIHandler(svc, cfg.AdminToken, cfg.HubURL))

	return mux
}
