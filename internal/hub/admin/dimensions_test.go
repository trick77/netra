package admin_test

import (
	"context"
	"errors"
	"testing"

	"github.com/trick77/netra/internal/hub/admin"
)

func TestCreateAndListProviders(t *testing.T) {
	svc, _ := newService(t)
	ctx := context.Background()

	if _, err := svc.CreateProvider(ctx, "hetzner"); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	if _, err := svc.CreateProvider(ctx, "aws"); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}

	providers, err := svc.ListProviders(ctx)
	if err != nil {
		t.Fatalf("ListProviders: %v", err)
	}
	if len(providers) != 2 {
		t.Fatalf("len = %d, want 2", len(providers))
	}
	if providers[0].Name != "aws" {
		t.Errorf("providers[0] = %q, want aws — the list is ordered by name", providers[0].Name)
	}
}

func TestCreateProviderRejectsADuplicateName(t *testing.T) {
	svc, _ := newService(t)
	ctx := context.Background()

	if _, err := svc.CreateProvider(ctx, "hetzner"); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	if _, err := svc.CreateProvider(ctx, "hetzner"); err == nil {
		t.Error("a duplicate provider name was accepted")
	}
}

// A duplicate name is an operator mistake with an obvious fix, not a broken
// hub. It has to be distinguishable from a database failure, or the handler
// answers 500 to what is really "try a different name".
func TestCreateProviderReportsADuplicateAsConflict(t *testing.T) {
	svc, _ := newService(t)
	ctx := context.Background()

	if _, err := svc.CreateProvider(ctx, "hetzner"); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}

	if _, err := svc.CreateProvider(ctx, "hetzner"); !errors.Is(err, admin.ErrConflict) {
		t.Errorf("err = %v, want ErrConflict", err)
	}
}

func TestPatchProviderOntoAnExistingNameIsConflict(t *testing.T) {
	svc, _ := newService(t)
	ctx := context.Background()

	if _, err := svc.CreateProvider(ctx, "hetzner"); err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	second, err := svc.CreateProvider(ctx, "aws")
	if err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}

	if err := svc.PatchProvider(ctx, second.ID, "hetzner"); !errors.Is(err, admin.ErrConflict) {
		t.Errorf("err = %v, want ErrConflict", err)
	}
}

// A reference to a row that does not exist is the caller's mistake to fix, so
// it must read as invalid input rather than as a hub failure.
func TestCreateSiteWithAnUnknownProviderIsInvalid(t *testing.T) {
	svc, _ := newService(t)

	unknown := int32(4242)
	if _, err := svc.CreateSite(context.Background(), "zrh", &unknown); !errors.Is(err, admin.ErrInvalid) {
		t.Errorf("err = %v, want ErrInvalid", err)
	}
}

func TestCreateProviderRejectsAnEmptyName(t *testing.T) {
	svc, _ := newService(t)

	if _, err := svc.CreateProvider(context.Background(), "   "); !errors.Is(err, admin.ErrInvalid) {
		t.Errorf("err = %v, want ErrInvalid", err)
	}
}

func TestCreateSiteRejectsAnEmptyName(t *testing.T) {
	svc, _ := newService(t)

	if _, err := svc.CreateSite(context.Background(), "", nil); !errors.Is(err, admin.ErrInvalid) {
		t.Errorf("err = %v, want ErrInvalid", err)
	}
}

// The rule spec 8 calls out: a manually set lat/lon is never overwritten by
// anything the caller did not explicitly send. The phase-2 geocoder will
// patch through this same method, so the guard has to live here.
func TestPatchSiteLeavesUnsetFieldsAlone(t *testing.T) {
	svc, s := newService(t)
	ctx := context.Background()

	var siteID int32
	if err := s.Pool().QueryRow(ctx,
		`INSERT INTO sites (name, latitude, longitude) VALUES ('zrh', 47.37, 8.54)
		 RETURNING id`).Scan(&siteID); err != nil {
		t.Fatalf("insert site: %v", err)
	}

	facility := "Interxion ZUR1"
	if err := svc.PatchSite(ctx, siteID, admin.SitePatch{Facility: &facility}); err != nil {
		t.Fatalf("PatchSite: %v", err)
	}

	var (
		lat, lon float64
		got      string
	)
	if err := s.Pool().QueryRow(ctx,
		`SELECT latitude, longitude, facility FROM sites WHERE id = $1`, siteID).
		Scan(&lat, &lon, &got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if lat != 47.37 || lon != 8.54 {
		t.Errorf("lat,lon = %v,%v — a patch that did not mention them must not move them", lat, lon)
	}
	if got != facility {
		t.Errorf("facility = %q, want %q", got, facility)
	}
}

// An explicitly sent coordinate is an instruction, not an accident.
func TestPatchSiteAppliesAnExplicitCoordinate(t *testing.T) {
	svc, s := newService(t)
	ctx := context.Background()

	var siteID int32
	if err := s.Pool().QueryRow(ctx,
		`INSERT INTO sites (name, latitude, longitude) VALUES ('zrh', 47.37, 8.54)
		 RETURNING id`).Scan(&siteID); err != nil {
		t.Fatalf("insert site: %v", err)
	}

	lat, lon := 46.20, 6.14
	if err := svc.PatchSite(ctx, siteID, admin.SitePatch{Latitude: &lat, Longitude: &lon}); err != nil {
		t.Fatalf("PatchSite: %v", err)
	}

	var gotLat, gotLon float64
	if err := s.Pool().QueryRow(ctx,
		`SELECT latitude, longitude FROM sites WHERE id = $1`, siteID).Scan(&gotLat, &gotLon); err != nil {
		t.Fatalf("query: %v", err)
	}
	if gotLat != lat || gotLon != lon {
		t.Errorf("lat,lon = %v,%v, want %v,%v", gotLat, gotLon, lat, lon)
	}
}

// A patch with no fields at all must not be a silent no-op that reports
// success on a site that does not exist.
func TestPatchSiteOnUnknownSiteIsNotFound(t *testing.T) {
	svc, _ := newService(t)

	name := "zrh"
	if err := svc.PatchSite(context.Background(), 4242, admin.SitePatch{Name: &name}); !errors.Is(err, admin.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestPatchSiteWithNothingToChangeIsInvalid(t *testing.T) {
	svc, _ := newService(t)

	if err := svc.PatchSite(context.Background(), 1, admin.SitePatch{}); !errors.Is(err, admin.ErrInvalid) {
		t.Errorf("err = %v, want ErrInvalid", err)
	}
}

func TestListSitesReportsItsProvider(t *testing.T) {
	svc, _ := newService(t)
	ctx := context.Background()

	provider, err := svc.CreateProvider(ctx, "hetzner")
	if err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	if _, err := svc.CreateSite(ctx, "fsn1", &provider.ID); err != nil {
		t.Fatalf("CreateSite: %v", err)
	}

	sites, err := svc.ListSites(ctx)
	if err != nil {
		t.Fatalf("ListSites: %v", err)
	}
	if len(sites) != 1 {
		t.Fatalf("len = %d, want 1", len(sites))
	}
	if sites[0].ProviderID == nil || *sites[0].ProviderID != provider.ID {
		t.Errorf("ProviderID = %v, want %d", sites[0].ProviderID, provider.ID)
	}
	// A site with no coordinates has none. Absent is NULL, never 0,0 -- which
	// is a real place in the Gulf of Guinea.
	if sites[0].Latitude != nil {
		t.Errorf("Latitude = %v, want nil for a site with no coordinates", *sites[0].Latitude)
	}
}
