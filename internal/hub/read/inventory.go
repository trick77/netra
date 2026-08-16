package read

import (
	"context"
	"fmt"
	"time"

	"github.com/trick77/netra/internal/hub/systemdstate"
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

// Unit is one row of /hosts/{id}/units: a unit that needs attention, and the
// state it is in.
//
// The state is read straight off systemd_units. It used to be a LAST-VALUE
// lookup into systemd_unit_events, on the reasoning that the collector emits
// events rather than samples (spec 5.3) so "what state is this unit in" is by
// construction "what did the newest event say". That reasoning held only while
// every transition arrived; the agent now also sends a periodic snapshot so
// the hub can be told what IS, and the answer lives on the row. See Units
// below and 0003_systemd_unit_state.sql.
//
// Since is when the unit ENTERED this state, not when the hub last heard about
// it -- the write paths advance it only when the state actually changes. A
// unit with no state recorded yet reports null rather than "active", which
// would be a guess.
type Unit struct {
	ID       int32      `json:"id"`
	Name     string     `json:"unit_name"`
	State    *string    `json:"state"`
	Substate *string    `json:"substate"`
	Since    *time.Time `json:"since"`
	// Restarts1h is how many state changes this unit has recorded in the last
	// hour, and it is the ONLY thing that reveals a unit which is broken
	// without ever looking broken -- a service that runs for a few minutes,
	// dies, and comes back is healthy at nearly every scrape. See
	// systemdstate.FlapThreshold.
	Restarts1h int32 `json:"restarts_1h"`
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

// Units lists the host's systemd units that NEED ATTENTION.
//
// This is not an inventory, and the distinction matters to anyone reading the
// result: a unit missing from this list is a unit that is fine, not a unit the
// host does not have. A normal host runs 300-400 loaded services, nearly all
// of them active/running daemons or inactive/dead oneshots, and returning them
// buries the one row an operator opened the page for. package systemdstate
// holds the rule and the reasoning behind which states qualify. For "how many
// services does this host run", read host_current.services_total, which is the
// count the agent actually reported.
//
// The state comes off the row rather than from the newest event, which is what
// this used to do through a LATERAL over systemd_unit_events. That made the
// event log double as the state store, and a log cannot say "nothing changed,
// and here is the truth anyway" -- so a transition the hub never received left
// a unit pinned at its last known state forever, and the 90-day event prune
// could silently blank the state of a unit that was still failed. See
// 0003_systemd_unit_state.sql.
func (s *Service) Units(ctx context.Context, hostID int32) ([]Unit, error) {
	if err := s.hostExists(ctx, hostID); err != nil {
		return nil, err
	}

	// The transition count is what catches a unit that is BROKEN WITHOUT EVER
	// LOOKING BROKEN: a service that runs a few minutes, dies, and comes back
	// is healthy at nearly every scrape, so no snapshot of its current state
	// can reveal it. Only the history can, which is why the event log is
	// joined here rather than the columns being read alone.
	//
	// The count rides the same query as the filter because it is part of the
	// filter -- such a unit is listed BECAUSE of its count, not despite it.
	rows, err := s.pool.Query(ctx, `
		SELECT u.id, u.unit_name, u.state, u.substate, u.state_ts, coalesce(f.n, 0)
		  FROM systemd_units u
		  LEFT JOIN LATERAL (
		       SELECT count(*) AS n
		         FROM systemd_unit_events e
		        WHERE e.unit_id = u.id AND e.host_id = u.host_id
		          AND e.ts > now() - $2::interval
		  ) f ON TRUE
		 WHERE u.host_id = $1
		   AND (`+systemdstate.NotableSQL("u")+` OR coalesce(f.n, 0) >= $3)
		 ORDER BY u.unit_name`,
		hostID, systemdstate.FlapWindow, systemdstate.FlapThreshold)
	if err != nil {
		return nil, fmt.Errorf("query units: %w", err)
	}
	defer rows.Close()

	out := []Unit{}
	for rows.Next() {
		var u Unit
		if err := rows.Scan(&u.ID, &u.Name, &u.State, &u.Substate, &u.Since, &u.Restarts1h); err != nil {
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
