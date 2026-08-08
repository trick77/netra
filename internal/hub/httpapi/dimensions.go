package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/trick77/netra/internal/hub/admin"
)

// siteJSON is the wire shape of a site. Every optional column is a pointer so
// an unset one renders as null rather than as a zero that reads as a fact --
// 0,0 in particular is a real place, not "no coordinates".
type siteJSON struct {
	ID          int32    `json:"id"`
	ProviderID  *int32   `json:"provider_id"`
	Name        string   `json:"name"`
	Facility    *string  `json:"facility"`
	Address     *string  `json:"address"`
	Latitude    *float64 `json:"latitude"`
	Longitude   *float64 `json:"longitude"`
	CountryCode *string  `json:"country_code"`
	Timezone    *string  `json:"timezone"`
}

func toSiteJSON(s admin.Site) siteJSON {
	return siteJSON{
		ID:          s.ID,
		ProviderID:  s.ProviderID,
		Name:        s.Name,
		Facility:    s.Facility,
		Address:     s.Address,
		Latitude:    s.Latitude,
		Longitude:   s.Longitude,
		CountryCode: s.CountryCode,
		Timezone:    s.Timezone,
	}
}

func (h *adminHandler) listSites(w http.ResponseWriter, r *http.Request) {
	sites, err := h.svc.ListSites(r.Context())
	if err != nil {
		writeAdminError(w, r, err)
		return
	}

	out := make([]siteJSON, 0, len(sites))
	for _, s := range sites {
		out = append(out, toSiteJSON(s))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *adminHandler) createSite(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name       string `json:"name"`
		ProviderID *int32 `json:"provider_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "malformed JSON body"})
		return
	}

	site, err := h.svc.CreateSite(r.Context(), req.Name, req.ProviderID)
	if err != nil {
		writeAdminError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toSiteJSON(site))
}

// patchSite decodes into a struct of pointers, so a field the request body
// omits stays nil and the update leaves that column untouched. That is what
// keeps a manually set latitude and longitude safe from a caller -- later, a
// geocoder -- that only meant to set an address.
func (h *adminHandler) patchSite(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	var req struct {
		ProviderID  *int32   `json:"provider_id"`
		Name        *string  `json:"name"`
		Facility    *string  `json:"facility"`
		Address     *string  `json:"address"`
		Latitude    *float64 `json:"latitude"`
		Longitude   *float64 `json:"longitude"`
		CountryCode *string  `json:"country_code"`
		Timezone    *string  `json:"timezone"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "malformed JSON body"})
		return
	}

	patch := admin.SitePatch{
		ProviderID:  req.ProviderID,
		Name:        req.Name,
		Facility:    req.Facility,
		Address:     req.Address,
		Latitude:    req.Latitude,
		Longitude:   req.Longitude,
		CountryCode: req.CountryCode,
		Timezone:    req.Timezone,
	}
	if err := h.svc.PatchSite(r.Context(), id, patch); err != nil {
		writeAdminError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (h *adminHandler) listProviders(w http.ResponseWriter, r *http.Request) {
	providers, err := h.svc.ListProviders(r.Context())
	if err != nil {
		writeAdminError(w, r, err)
		return
	}

	type providerJSON struct {
		ID   int32  `json:"id"`
		Name string `json:"name"`
	}
	out := make([]providerJSON, 0, len(providers))
	for _, p := range providers {
		out = append(out, providerJSON{ID: p.ID, Name: p.Name})
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *adminHandler) createProvider(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "malformed JSON body"})
		return
	}

	provider, err := h.svc.CreateProvider(r.Context(), req.Name)
	if err != nil {
		writeAdminError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": provider.ID, "name": provider.Name})
}

func (h *adminHandler) patchProvider(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "malformed JSON body"})
		return
	}

	if err := h.svc.PatchProvider(r.Context(), id, req.Name); err != nil {
		writeAdminError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}
