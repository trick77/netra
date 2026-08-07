package httpapi

import (
	"net/http"

	"github.com/trick77/netra/internal/hub/auth"
	"github.com/trick77/netra/internal/hub/store"
)

// NewRouter builds the hub's route table. Go 1.22 method routing is used
// directly; there is no framework.
func NewRouter(a *auth.Authenticator, s *store.Store) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /api/health", NewHealthHandler(s))
	mux.Handle("POST /api/agent/v1/ingest", NewIngestHandler(a, s))
	return mux
}
