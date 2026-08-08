package httpapi_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestIntegrationAdminCreateAndListProviders(t *testing.T) {
	srv, _ := newAdminFixture(t)

	resp := doAdmin(t, srv, http.MethodPost, "/api/v1/providers", `{"name":"hetzner"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}

	var providers []struct {
		ID   int32  `json:"id"`
		Name string `json:"name"`
	}
	decodeJSON(t, doAdmin(t, srv, http.MethodGet, "/api/v1/providers", ""), &providers)

	if len(providers) != 1 || providers[0].Name != "hetzner" {
		t.Fatalf("providers = %+v, want one named hetzner", providers)
	}
}

// A duplicate name is a 409 the operator can act on, not a 500 that reads as
// "the hub is broken".
func TestIntegrationAdminDuplicateProviderIs409(t *testing.T) {
	srv, _ := newAdminFixture(t)

	if resp := doAdmin(t, srv, http.MethodPost, "/api/v1/providers", `{"name":"hetzner"}`); resp.StatusCode != http.StatusCreated {
		t.Fatalf("first create status = %d, want 201", resp.StatusCode)
	}

	resp := doAdmin(t, srv, http.MethodPost, "/api/v1/providers", `{"name":"hetzner"}`)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	if body := readBody(t, resp); !strings.Contains(body, "already exists") {
		t.Errorf("body does not say what collided: %s", body)
	}
}

// A reference to a row that does not exist is bad input, so 400 rather than
// the 500 an unclassified constraint violation would produce.
func TestIntegrationAdminCreateHostWithAnUnknownSiteIs400(t *testing.T) {
	srv, _ := newAdminFixture(t)

	resp := doAdmin(t, srv, http.MethodPost, "/api/v1/hosts", `{"hostname":"web01","site_id":4242}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestIntegrationAdminCreateProviderRejectsAnEmptyName(t *testing.T) {
	srv, _ := newAdminFixture(t)

	resp := doAdmin(t, srv, http.MethodPost, "/api/v1/providers", `{"name":""}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestIntegrationAdminCreateProviderRejectsMalformedJSON(t *testing.T) {
	srv, _ := newAdminFixture(t)

	resp := doAdmin(t, srv, http.MethodPost, "/api/v1/providers", `{`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestIntegrationAdminPatchProviderRenamesIt(t *testing.T) {
	srv, _ := newAdminFixture(t)

	var created struct {
		ID int32 `json:"id"`
	}
	decodeJSON(t, doAdmin(t, srv, http.MethodPost, "/api/v1/providers", `{"name":"hetzner"}`), &created)

	resp := doAdmin(t, srv, http.MethodPatch,
		fmt.Sprintf("/api/v1/providers/%d", created.ID), `{"name":"hetzner-cloud"}`)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}

	var providers []struct {
		Name string `json:"name"`
	}
	decodeJSON(t, doAdmin(t, srv, http.MethodGet, "/api/v1/providers", ""), &providers)
	if providers[0].Name != "hetzner-cloud" {
		t.Errorf("name = %q, want hetzner-cloud", providers[0].Name)
	}
}

func TestIntegrationAdminPatchUnknownProviderIs404(t *testing.T) {
	srv, _ := newAdminFixture(t)

	resp := doAdmin(t, srv, http.MethodPatch, "/api/v1/providers/4242", `{"name":"x"}`)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

// A site with no coordinates renders null, not 0 -- 0,0 is a real place in the
// Gulf of Guinea, and a map would happily draw a host there.
func TestIntegrationAdminSiteWithoutCoordinatesRendersNull(t *testing.T) {
	srv, _ := newAdminFixture(t)

	if resp := doAdmin(t, srv, http.MethodPost, "/api/v1/sites", `{"name":"zrh"}`); resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}

	var sites []struct {
		Latitude  *float64 `json:"latitude"`
		Longitude *float64 `json:"longitude"`
	}
	decodeJSON(t, doAdmin(t, srv, http.MethodGet, "/api/v1/sites", ""), &sites)

	if len(sites) != 1 {
		t.Fatalf("len = %d, want 1", len(sites))
	}
	if sites[0].Latitude != nil || sites[0].Longitude != nil {
		t.Errorf("lat,lon = %v,%v, want null,null", sites[0].Latitude, sites[0].Longitude)
	}
}

// The spec 8 rule, end to end over HTTP: a PATCH body that says nothing about
// coordinates must not move them.
func TestIntegrationAdminPatchSiteDoesNotClobberAManualCoordinate(t *testing.T) {
	srv, s := newAdminFixture(t)

	var siteID int32
	if err := s.Pool().QueryRow(context.Background(),
		`INSERT INTO sites (name, latitude, longitude) VALUES ('zrh', 47.37, 8.54)
		 RETURNING id`).Scan(&siteID); err != nil {
		t.Fatalf("insert site: %v", err)
	}

	resp := doAdmin(t, srv, http.MethodPatch,
		fmt.Sprintf("/api/v1/sites/%d", siteID), `{"address":"Zurich, CH"}`)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}

	var sites []struct {
		Address   *string  `json:"address"`
		Latitude  *float64 `json:"latitude"`
		Longitude *float64 `json:"longitude"`
	}
	decodeJSON(t, doAdmin(t, srv, http.MethodGet, "/api/v1/sites", ""), &sites)

	if sites[0].Address == nil || *sites[0].Address != "Zurich, CH" {
		t.Errorf("address = %v, want the patched value", sites[0].Address)
	}
	if sites[0].Latitude == nil || *sites[0].Latitude != 47.37 {
		t.Errorf("latitude = %v, want 47.37 untouched", sites[0].Latitude)
	}
	if sites[0].Longitude == nil || *sites[0].Longitude != 8.54 {
		t.Errorf("longitude = %v, want 8.54 untouched", sites[0].Longitude)
	}
}

func TestIntegrationAdminPatchSiteRejectsANonNumericID(t *testing.T) {
	srv, _ := newAdminFixture(t)

	resp := doAdmin(t, srv, http.MethodPatch, "/api/v1/sites/abc", `{"name":"zrh"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestIntegrationAdminPatchSiteRejectsMalformedJSON(t *testing.T) {
	srv, _ := newAdminFixture(t)

	resp := doAdmin(t, srv, http.MethodPatch, "/api/v1/sites/1", `{`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestIntegrationAdminCreateSiteRejectsAnEmptyName(t *testing.T) {
	srv, _ := newAdminFixture(t)

	resp := doAdmin(t, srv, http.MethodPost, "/api/v1/sites", `{"name":"  "}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestIntegrationAdminCreateSiteRejectsMalformedJSON(t *testing.T) {
	srv, _ := newAdminFixture(t)

	resp := doAdmin(t, srv, http.MethodPost, "/api/v1/sites", `{`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}
