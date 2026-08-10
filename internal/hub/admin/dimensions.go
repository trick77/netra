package admin

import (
	"context"
	"fmt"
	"strings"
)

// Provider is a hosting provider: the top of the site hierarchy.
type Provider struct {
	ID   int32
	Name string
}

// Site is one location belonging to a provider. Every optional column is a
// pointer: a site with no coordinates has none, and 0,0 is a real place in
// the Gulf of Guinea rather than a way to say "unknown".
type Site struct {
	ID          int32
	ProviderID  *int32
	Name        string
	Facility    *string
	Address     *string
	Latitude    *float64
	Longitude   *float64
	CountryCode *string
	Timezone    *string
}

// SitePatch carries the fields of a site to change. A nil field is left alone,
// which is what makes "a manual lat/lon is never overwritten by a geocode
// result" enforceable rather than aspirational: a caller that says nothing
// about latitude cannot move it.
//
// The converse does NOT hold, and the pointers should not be read as implying
// it. JSON `null` and an omitted key both decode to nil, so no nullable column
// can be CLEARED through this type -- there is no way to say "unset the
// facility". Adding one needs a presence-aware decode (json.RawMessage per
// field, or a decoded map of the keys actually present), not another pointer.
type SitePatch struct {
	ProviderID  *int32
	Name        *string
	Facility    *string
	Address     *string
	Latitude    *float64
	Longitude   *float64
	CountryCode *string
	Timezone    *string
}

// CreateProvider registers a hosting provider.
func (s *Service) CreateProvider(ctx context.Context, name string) (Provider, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Provider{}, fmt.Errorf("%w: name is required", ErrInvalid)
	}

	var id int32
	if err := s.pool.QueryRow(ctx,
		`INSERT INTO providers (name) VALUES ($1) RETURNING id`, name).Scan(&id); err != nil {
		return Provider{}, fmt.Errorf("insert provider: %w", classify(err))
	}
	return Provider{ID: id, Name: name}, nil
}

// ListProviders returns every provider, ordered by name.
func (s *Service) ListProviders(ctx context.Context) ([]Provider, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, name FROM providers ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("query providers: %w", err)
	}
	defer rows.Close()

	providers := []Provider{}
	for rows.Next() {
		var p Provider
		if err := rows.Scan(&p.ID, &p.Name); err != nil {
			return nil, fmt.Errorf("scan provider: %w", err)
		}
		providers = append(providers, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate providers: %w", err)
	}
	return providers, nil
}

// PatchProvider renames a provider.
func (s *Service) PatchProvider(ctx context.Context, id int32, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("%w: name is required", ErrInvalid)
	}

	tag, err := s.pool.Exec(ctx, `UPDATE providers SET name = $2 WHERE id = $1`, id, name)
	if err != nil {
		return fmt.Errorf("update provider: %w", classify(err))
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// CreateSite registers a site, optionally under a provider.
func (s *Service) CreateSite(ctx context.Context, name string, providerID *int32) (Site, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Site{}, fmt.Errorf("%w: name is required", ErrInvalid)
	}

	var id int32
	if err := s.pool.QueryRow(ctx,
		`INSERT INTO sites (name, provider_id) VALUES ($1, $2) RETURNING id`,
		name, providerID).Scan(&id); err != nil {
		return Site{}, fmt.Errorf("insert site: %w", classify(err))
	}
	return Site{ID: id, Name: name, ProviderID: providerID}, nil
}

// ListSites returns every site, ordered by name.
func (s *Service) ListSites(ctx context.Context) ([]Site, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, provider_id, name, facility, address,
		        latitude, longitude, country_code, timezone
		   FROM sites
		  ORDER BY name, id`)
	if err != nil {
		return nil, fmt.Errorf("query sites: %w", err)
	}
	defer rows.Close()

	sites := []Site{}
	for rows.Next() {
		var site Site
		if err := rows.Scan(&site.ID, &site.ProviderID, &site.Name, &site.Facility,
			&site.Address, &site.Latitude, &site.Longitude,
			&site.CountryCode, &site.Timezone); err != nil {
			return nil, fmt.Errorf("scan site: %w", err)
		}
		sites = append(sites, site)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sites: %w", err)
	}
	return sites, nil
}

// PatchSite updates only the fields the caller set.
//
// The UPDATE is built from the non-nil fields rather than writing every
// column, so a caller that says nothing about latitude and longitude cannot
// move them. That is spec 8's rule about a manual coordinate surviving a
// geocode result, and it has to hold here because the phase-2 geocoder will
// patch through this same method.
//
// A field can be set but not cleared -- see SitePatch.
func (s *Service) PatchSite(ctx context.Context, id int32, patch SitePatch) error {
	var (
		sets []string
		args []any
	)
	add := func(column string, value any) {
		args = append(args, value)
		sets = append(sets, fmt.Sprintf("%s = $%d", column, len(args)+1))
	}

	if patch.ProviderID != nil {
		add("provider_id", *patch.ProviderID)
	}
	if patch.Name != nil {
		name := strings.TrimSpace(*patch.Name)
		if name == "" {
			return fmt.Errorf("%w: name cannot be blank", ErrInvalid)
		}
		add("name", name)
	}
	if patch.Facility != nil {
		add("facility", *patch.Facility)
	}
	if patch.Address != nil {
		add("address", *patch.Address)
	}
	if patch.Latitude != nil {
		add("latitude", *patch.Latitude)
	}
	if patch.Longitude != nil {
		add("longitude", *patch.Longitude)
	}
	if patch.CountryCode != nil {
		add("country_code", *patch.CountryCode)
	}
	if patch.Timezone != nil {
		add("timezone", *patch.Timezone)
	}

	if len(sets) == 0 {
		return fmt.Errorf("%w: no fields to update", ErrInvalid)
	}

	query := fmt.Sprintf(`UPDATE sites SET %s WHERE id = $1`, strings.Join(sets, ", "))
	tag, err := s.pool.Exec(ctx, query, append([]any{id}, args...)...)
	if err != nil {
		return fmt.Errorf("update site: %w", classify(err))
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
