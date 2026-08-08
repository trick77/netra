package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/trick77/netra/internal/hub/admin"
)

type adminHandler struct {
	svc *admin.Service
}

// NewAdminHandler serves the host and token endpoints of spec 8's admin
// section. It is mounted behind RequireAdmin, never on its own.
func NewAdminHandler(svc *admin.Service) http.Handler {
	h := &adminHandler{svc: svc}

	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/hosts", http.HandlerFunc(h.list))
	mux.Handle("POST /api/v1/hosts", http.HandlerFunc(h.create))
	mux.Handle("POST /api/v1/hosts/{id}/token", http.HandlerFunc(h.rotate))
	mux.Handle("DELETE /api/v1/hosts/{id}", http.HandlerFunc(h.delete))
	mux.Handle("GET /api/v1/sites", http.HandlerFunc(h.listSites))
	mux.Handle("POST /api/v1/sites", http.HandlerFunc(h.createSite))
	mux.Handle("PATCH /api/v1/sites/{id}", http.HandlerFunc(h.patchSite))
	mux.Handle("GET /api/v1/providers", http.HandlerFunc(h.listProviders))
	mux.Handle("POST /api/v1/providers", http.HandlerFunc(h.createProvider))
	mux.Handle("PATCH /api/v1/providers/{id}", http.HandlerFunc(h.patchProvider))
	return mux
}

// hostJSON is the wire shape of a host. last_seen is a pointer so a host that
// has never posted renders as null rather than as a zero time reading as 1970.
type hostJSON struct {
	ID       int32      `json:"id"`
	Hostname string     `json:"hostname"`
	SiteID   *int32     `json:"site_id"`
	LastSeen *time.Time `json:"last_seen"`
}

func (h *adminHandler) list(w http.ResponseWriter, r *http.Request) {
	hosts, err := h.svc.ListHosts(r.Context())
	if err != nil {
		writeAdminError(w, r, err)
		return
	}

	out := make([]hostJSON, 0, len(hosts))
	for _, host := range hosts {
		out = append(out, hostJSON{
			ID:       host.ID,
			Hostname: host.Hostname,
			SiteID:   host.SiteID,
			LastSeen: host.LastSeen,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *adminHandler) create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Hostname string `json:"hostname"`
		SiteID   *int32 `json:"site_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "malformed JSON body"})
		return
	}

	host, token, err := h.svc.CreateHost(r.Context(), req.Hostname, req.SiteID)
	if err != nil {
		writeAdminError(w, r, err)
		return
	}

	// The only moment this plaintext exists outside the agent that will use
	// it. It is not stored, not logged, and not readable back.
	writeJSON(w, http.StatusCreated, struct {
		hostJSON
		Token string `json:"token"`
	}{
		hostJSON: hostJSON{ID: host.ID, Hostname: host.Hostname, SiteID: host.SiteID},
		Token:    token,
	})
}

func (h *adminHandler) rotate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	token, err := h.svc.RotateToken(r.Context(), id)
	if err != nil {
		writeAdminError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"id": id, "token": token})
}

func (h *adminHandler) delete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	if err := h.svc.DeleteHost(r.Context(), id); err != nil {
		writeAdminError(w, r, err)
		return
	}

	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

// parseID parses a path id. It is shared with the UI, which reports a bad one
// as HTML rather than JSON.
func parseID(raw string) (int32, bool) {
	id, err := strconv.ParseInt(raw, 10, 32)
	if err != nil {
		return 0, false
	}
	return int32(id), true
}

// pathID parses the {id} path value, answering 400 and reporting false when
// it is not an integer.
func pathID(w http.ResponseWriter, r *http.Request) (int32, bool) {
	id, ok := parseID(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id must be an integer"})
		return 0, false
	}
	return id, true
}

// writeAdminError maps a service error to a status. Only the two sentinel
// errors reach the client as a message; anything else is logged and answered
// with a bare 500, so an internal detail never leaves the process.
func writeAdminError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, admin.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	case errors.Is(err, admin.ErrInvalid):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	default:
		slog.Error("admin request failed", "path", r.URL.Path, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
	}
}

// writeJSON writes a response that must never be cached: these bodies carry
// freshly minted tokens and the inventory they belong to.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
