package read

import (
	"context"
	"fmt"
	"time"
)

// The five dimension listings behind a host. Each is a projection of one
// dimension table -- the entities the metric families key on -- so a UI can
// name a series before it has any points for it.
//
// Every one of them returns an empty slice, never nil, for a host that has
// none. A host with no containers and a host that does not exist are
// different facts, and the second is ErrNotFound: without the existence check
// they would both render as [] and "is this host registered?" would have no
// answer through the API.

// Container is one row of /hosts/{id}/containers.
type Container struct {
	ID    int32   `json:"id"`
	Key   string  `json:"container_key"`
	Name  *string `json:"name"`
	Image *string `json:"image"`
	// IsAgent marks netra's own container. A fleet view that does not
	// separate it reports the monitoring as part of the workload.
	IsAgent bool `json:"is_agent"`
}

// Filesystem is one row of /hosts/{id}/filesystems.
//
// No capacity figures here: those are the filesystem metric family, where
// total, used and free carry their timestamps. The comment on
// filesystem_samples in 0001_init.sql governs any fullness a consumer
// computes -- used / (used + free), never used / total.
type Filesystem struct {
	ID         int32   `json:"id"`
	Label      string  `json:"label"`
	Mountpoint *string `json:"mountpoint"`
	// DeviceID is the st_dev the collector dedups bind mounts by, not a
	// reference to the devices table.
	DeviceID *int64 `json:"device_id"`
}

// Address is one row of /hosts/{id}/addresses.
type Address struct {
	Iface   string  `json:"iface"`
	IfIndex *int32  `json:"if_index"`
	Address string  `json:"address"`
	Family  int16   `json:"family"`
	Scope   *string `json:"scope"`
	VRF     *string `json:"vrf"`

	Description *string   `json:"description"`
	FirstSeen   time.Time `json:"first_seen"`
	LastSeen    time.Time `json:"last_seen"`
}

// Package is one row of /hosts/{id}/packages.
type Package struct {
	Name      string    `json:"name"`
	Version   string    `json:"version"`
	Arch      string    `json:"arch"`
	Format    string    `json:"format"`
	SizeBytes *int64    `json:"size_bytes"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
}

// Unit is one row of /hosts/{id}/units: the unit, and the most recent state
// transition recorded for it.
//
// The state is a LAST-VALUE lookup into systemd_unit_events rather than a
// column on systemd_units, because that is where the collector puts it: the
// systemd collector emits events, not samples (spec 5.3), so "what state is
// this unit in" is by construction "what did the newest event say". A unit
// with no events yet reports null rather than "active", which would be a
// guess.
type Unit struct {
	ID       int32      `json:"id"`
	Name     string     `json:"unit_name"`
	State    *string    `json:"state"`
	Substate *string    `json:"substate"`
	Since    *time.Time `json:"since"`
}

// Containers lists the containers seen on a host.
func (s *Service) Containers(ctx context.Context, hostID int32) ([]Container, error) {
	if err := s.hostExists(ctx, hostID); err != nil {
		return nil, err
	}

	rows, err := s.pool.Query(ctx, `
		SELECT id, container_key, name, image, is_agent
		  FROM containers
		 WHERE host_id = $1
		 ORDER BY container_key`, hostID)
	if err != nil {
		return nil, fmt.Errorf("query containers: %w", err)
	}
	defer rows.Close()

	out := []Container{}
	for rows.Next() {
		var c Container
		if err := rows.Scan(&c.ID, &c.Key, &c.Name, &c.Image, &c.IsAgent); err != nil {
			return nil, fmt.Errorf("scan container: %w", err)
		}
		out = append(out, c)
	}
	return out, rowsErr(rows.Err(), "containers")
}

// Filesystems lists the filesystems seen on a host.
func (s *Service) Filesystems(ctx context.Context, hostID int32) ([]Filesystem, error) {
	if err := s.hostExists(ctx, hostID); err != nil {
		return nil, err
	}

	rows, err := s.pool.Query(ctx, `
		SELECT id, label, mountpoint, device_id
		  FROM filesystems
		 WHERE host_id = $1
		 ORDER BY label`, hostID)
	if err != nil {
		return nil, fmt.Errorf("query filesystems: %w", err)
	}
	defer rows.Close()

	out := []Filesystem{}
	for rows.Next() {
		var f Filesystem
		if err := rows.Scan(&f.ID, &f.Label, &f.Mountpoint, &f.DeviceID); err != nil {
			return nil, fmt.Errorf("scan filesystem: %w", err)
		}
		out = append(out, f)
	}
	return out, rowsErr(rows.Err(), "filesystems")
}

// Addresses lists the host's addresses, with the scope the HUB derived.
//
// host(address) rather than the inet itself, matching what the agent reported
// and what UpsertHostAddresses prunes against: rendering the column directly
// would append a /32 the agent never sent, and the two halves of the same
// answer would disagree.
func (s *Service) Addresses(ctx context.Context, hostID int32) ([]Address, error) {
	if err := s.hostExists(ctx, hostID); err != nil {
		return nil, err
	}

	rows, err := s.pool.Query(ctx, `
		SELECT iface, if_index, host(address), family, scope, vrf, description,
		       first_seen, last_seen
		  FROM host_addresses
		 WHERE host_id = $1
		 ORDER BY iface, address`, hostID)
	if err != nil {
		return nil, fmt.Errorf("query addresses: %w", err)
	}
	defer rows.Close()

	out := []Address{}
	for rows.Next() {
		var a Address
		if err := rows.Scan(&a.Iface, &a.IfIndex, &a.Address, &a.Family,
			&a.Scope, &a.VRF, &a.Description, &a.FirstSeen, &a.LastSeen); err != nil {
			return nil, fmt.Errorf("scan address: %w", err)
		}
		out = append(out, a)
	}
	return out, rowsErr(rows.Err(), "addresses")
}

// Packages lists the host's installed packages.
func (s *Service) Packages(ctx context.Context, hostID int32) ([]Package, error) {
	if err := s.hostExists(ctx, hostID); err != nil {
		return nil, err
	}

	rows, err := s.pool.Query(ctx, `
		SELECT name, version, arch, format, size_bytes, first_seen, last_seen
		  FROM host_packages
		 WHERE host_id = $1
		 ORDER BY name, arch`, hostID)
	if err != nil {
		return nil, fmt.Errorf("query packages: %w", err)
	}
	defer rows.Close()

	out := []Package{}
	for rows.Next() {
		var p Package
		if err := rows.Scan(&p.Name, &p.Version, &p.Arch, &p.Format,
			&p.SizeBytes, &p.FirstSeen, &p.LastSeen); err != nil {
			return nil, fmt.Errorf("scan package: %w", err)
		}
		out = append(out, p)
	}
	return out, rowsErr(rows.Err(), "packages")
}

// Units lists the host's systemd units with their newest recorded state.
//
// The newest event per unit comes from a LATERAL ... LIMIT 1, which uses
// systemd_unit_events_unit_id_host_id_idx and stops at the first row per unit.
// A plain join to a grouped max(ts) would read every event the unit ever had
// -- the exact table the retention job below exists because it grows.
func (s *Service) Units(ctx context.Context, hostID int32) ([]Unit, error) {
	if err := s.hostExists(ctx, hostID); err != nil {
		return nil, err
	}

	rows, err := s.pool.Query(ctx, `
		SELECT u.id, u.unit_name, e.state, e.substate, e.ts
		  FROM systemd_units u
		  LEFT JOIN LATERAL (
		       SELECT state, substate, ts
		         FROM systemd_unit_events
		        WHERE unit_id = u.id AND host_id = u.host_id
		        ORDER BY ts DESC
		        LIMIT 1
		  ) e ON TRUE
		 WHERE u.host_id = $1
		 ORDER BY u.unit_name`, hostID)
	if err != nil {
		return nil, fmt.Errorf("query units: %w", err)
	}
	defer rows.Close()

	out := []Unit{}
	for rows.Next() {
		var u Unit
		if err := rows.Scan(&u.ID, &u.Name, &u.State, &u.Substate, &u.Since); err != nil {
			return nil, fmt.Errorf("scan unit: %w", err)
		}
		out = append(out, u)
	}
	return out, rowsErr(rows.Err(), "units")
}

// rowsErr wraps a row-iteration failure, which is separate from a scan
// failure: it is how a connection dropped mid-result reaches the caller
// rather than looking like a short list.
func rowsErr(err error, what string) error {
	if err != nil {
		return fmt.Errorf("iterate %s: %w", what, err)
	}
	return nil
}
