package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/trick77/netra/internal/hub/admin"
	"github.com/trick77/netra/internal/hub/read"
)

type adminHandler struct {
	svc *admin.Service
	// hubURL is BACKEND_HUB_URL, or empty when it is unset. Empty is passed
	// through as empty rather than as a guess: the UI says so and asks the
	// operator, which is honest, where a wrong hostname in a setup command
	// sends an agent token to whoever owns that name.
	hubURL string
}

// NewAdminHandler serves ALL of spec 8's /api/v1: the admin operations here,
// and the read endpoints readHandler registers on the same mux.
//
// One mux for both halves is not tidiness. NewRouter mounts "/api/v1/" once,
// and http.ServeMux panics at startup on a duplicate pattern -- so a separate
// read mount, or a second "GET /api/v1/hosts" beside the one below, would
// take the hub down on boot rather than at request time.
//
// It is mounted behind RequireAdmin, never on its own.
func NewAdminHandler(svc *admin.Service, rd *read.Service, now func() time.Time, hubURL string) http.Handler {
	h := &adminHandler{svc: svc, hubURL: hubURL}

	mux := http.NewServeMux()
	// The SPA cannot know the address agents post to: the browser reaches
	// this hub on loopback, so its own location says nothing about the name
	// agents use. BACKEND_HUB_URL is that name, and the host admin page needs
	// it to render a setup command an operator can paste.
	//
	// An unset value comes back as "" and the page renders no command at all,
	// which is the whole reason this endpoint exists rather than letting the
	// SPA fall back to something. It fell back to a hardcoded placeholder
	// once, and the resulting command was complete, runnable and wrong.
	mux.Handle("GET /api/v1/config", http.HandlerFunc(h.config))
	mux.Handle("POST /api/v1/hosts", http.HandlerFunc(h.create))
	mux.Handle("POST /api/v1/hosts/{id}/token", http.HandlerFunc(h.rotate))
	mux.Handle("DELETE /api/v1/hosts/{id}", http.HandlerFunc(h.delete))
	mux.Handle("DELETE /api/v1/hosts/{id}/containers/{containerID}",
		http.HandlerFunc(h.deleteContainer))

	(&readHandler{svc: rd, now: now}).register(mux)
	return mux
}

// hostJSON is the wire shape of a host. last_seen is a pointer so a host that
// has never posted renders as null rather than as a zero time reading as 1970.
type hostJSON struct {
	ID       int32      `json:"id"`
	Hostname string     `json:"hostname"`
	LastSeen *time.Time `json:"last_seen"`
}

func (h *adminHandler) create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Hostname string `json:"hostname"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "malformed JSON body"})
		return
	}

	host, token, err := h.svc.CreateHost(r.Context(), req.Hostname)
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
		hostJSON: hostJSON{ID: host.ID, Hostname: host.Hostname},
		Token:    token,
	})
}

func (h *adminHandler) rotate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	_, token, err := h.svc.RotateToken(r.Context(), id)
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

// deleteContainer purges one container from a host's inventory, samples and
// all.
//
// A DELETE rather than a flag, because there is nothing to flag: the agent
// reports what is RUNNING, so a container that was removed is simply absent
// from every scrape after it, exactly like one that is stopped or one whose
// host is offline. The UI marks a row gone from its last_seen and offers this
// to the operator, who is the only party that knows which of the three it was.
func (h *adminHandler) deleteContainer(w http.ResponseWriter, r *http.Request) {
	hostID, ok := pathID(w, r)
	if !ok {
		return
	}
	containerID, ok := parseID(r.PathValue("containerID"))
	if !ok {
		writeJSON(w, http.StatusBadRequest,
			map[string]string{"error": "container id must be an integer"})
		return
	}

	if err := h.svc.DeleteContainer(r.Context(), hostID, containerID); err != nil {
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
	case errors.Is(err, admin.ErrConflict):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
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

func (h *adminHandler) config(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"hub_url": h.hubURL})
}
