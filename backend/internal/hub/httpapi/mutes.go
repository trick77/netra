package httpapi

import (
	"net/http"
	"time"

	"github.com/trick77/ship/kit/httpx"

	"github.com/trick77/netra/internal/hub/admin"
)

// muteJSON is the wire shape of a mute. kind is "" for the whole host;
// subject is null for every subject of the kind. until is null for a mute
// that lasts until lifted, or until resolved when until_resolved is set.
type muteJSON struct {
	ID            int32      `json:"id"`
	HostID        int32      `json:"host_id"`
	Hostname      string     `json:"hostname"`
	Kind          string     `json:"kind"`
	Subject       *string    `json:"subject"`
	UntilResolved bool       `json:"until_resolved"`
	Until         *time.Time `json:"until"`
	Note          string     `json:"note"`
	Created       time.Time  `json:"created"`
}

func muteOf(m admin.Mute) muteJSON {
	return muteJSON{
		ID:            m.ID,
		HostID:        m.HostID,
		Hostname:      m.Hostname,
		Kind:          m.Kind,
		Subject:       m.Subject,
		UntilResolved: m.ConditionID != nil,
		Until:         m.Until,
		Note:          m.Note,
		Created:       m.Created,
	}
}

func (h *adminHandler) listMutes(w http.ResponseWriter, r *http.Request) {
	var hostID *int32
	if raw := r.URL.Query().Get("host"); raw != "" {
		id, ok := parseID(raw)
		if !ok {
			httpx.WriteError(w, http.StatusBadRequest, "host must be an integer")
			return
		}
		hostID = &id
	}
	mutes, err := h.svc.ListMutes(r.Context(), h.now(), hostID)
	if err != nil {
		writeAdminError(w, r, err)
		return
	}
	out := make([]muteJSON, 0, len(mutes))
	for _, m := range mutes {
		out = append(out, muteOf(m))
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (h *adminHandler) createMute(w http.ResponseWriter, r *http.Request) {
	var req struct {
		HostID int32  `json:"host_id"`
		Kind   string `json:"kind"`
		// Subject is absent or null for every subject of the kind.
		Subject *string `json:"subject"`
		// UntilResolved lifts the mute when the condition open now resolves.
		UntilResolved bool `json:"until_resolved"`
		// Minutes is how long the mute lasts; 0 or absent means until lifted.
		Minutes int64  `json:"minutes"`
		Note    string `json:"note"`
	}
	if !httpx.DecodeJSONWith(w, r, &req, maxAdminBodyBytes,
		httpx.DecodeOptions{BadRequest: "malformed JSON body"}) {
		return
	}
	m, err := h.svc.CreateMute(r.Context(), admin.NewMute{
		HostID:        req.HostID,
		Kind:          req.Kind,
		Subject:       req.Subject,
		UntilResolved: req.UntilResolved,
		Minutes:       req.Minutes,
		Note:          req.Note,
	}, h.now())
	if err != nil {
		writeAdminError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, muteOf(m))
}

func (h *adminHandler) deleteMute(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := h.svc.DeleteMute(r.Context(), id); err != nil {
		writeAdminError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}
