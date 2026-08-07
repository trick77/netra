package httpapi

import (
	"net/http"
	"time"

	"github.com/trick77/netra/hub/internal/auth"
	"github.com/trick77/netra/hub/internal/store"
)

// NewRouter builds the hub's route table. Go 1.22 method routing is used
// directly; there is no framework.
func NewRouter(a *auth.Authenticator, s *store.Store, interval time.Duration) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /api/health", NewHealthHandler(s))
	mux.Handle("POST /api/agent/v1/ingest", NewIngestHandler(a, s, interval))
	return mux
}
