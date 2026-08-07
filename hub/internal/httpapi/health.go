package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/trick77/netra/hub/internal/store"
	"github.com/trick77/netra/internal/buildinfo"
)

type healthHandler struct {
	store *store.Store
}

// NewHealthHandler reports liveness plus database reachability. The compose
// healthcheck hits this, so a hub that cannot reach Postgres must not look
// healthy.
func NewHealthHandler(s *store.Store) http.Handler {
	return &healthHandler{store: s}
}

func (h *healthHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body := map[string]string{
		"status":   "ok",
		"database": "ok",
		"version":  buildinfo.Version(),
	}
	status := http.StatusOK

	if err := h.store.Pool().Ping(r.Context()); err != nil {
		body["status"] = "degraded"
		body["database"] = "unreachable"
		status = http.StatusServiceUnavailable
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
