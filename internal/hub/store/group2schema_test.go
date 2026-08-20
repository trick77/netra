package store_test

import (
	"context"
	"testing"

	"github.com/trick77/netra/internal/hub/store"
)

// group2Dimensions are the dimension tables the Group 2-4 collectors resolve
// their natural keys against (spec §5.2). Each carries a surrogate id that
// hypertables reference, plus the natural key that identifies the thing on the
// host.
var group2Dimensions = []struct{ table, natural string }{
	{"containers", "container_key"},
	{"filesystems", "label"},
	{"devices", "device"},
	{"systemd_units", "unit_name"},
}

// Every dimension carries a surrogate id AND its natural key, with the natural
// key unique per host.
//
// Hypertables reference the id only, so renaming the thing on the host touches
// one dimension row and no history (§5.2). Without the uniqueness constraint
// the same container could be inserted twice and its history would split
// silently across two ids.
func TestIntegrationGroup2DimensionsHaveSurrogateAndNaturalKeys(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	for _, d := range group2Dimensions {
		var hasID bool
		if err := s.Pool().QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM information_schema.columns
				 WHERE table_name = $1 AND column_name = 'id')`, d.table).Scan(&hasID); err != nil {
			t.Fatalf("%s id lookup: %v", d.table, err)
		}
		if !hasID {
			t.Errorf("%s has no surrogate id", d.table)
		}

		// Unique WITHIN a host, not globally: two hosts both having an "sda"
		// is normal, and a global constraint would reject the second host.
		var unique bool
		if err := s.Pool().QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM pg_indexes
				 WHERE tablename = $1
				   AND indexdef LIKE '%UNIQUE%'
				   AND indexdef LIKE '%host_id%'
				   AND indexdef LIKE '%' || $2 || '%')`,
			d.table, d.natural).Scan(&unique); err != nil {
			t.Fatalf("%s unique lookup: %v", d.table, err)
		}
		if !unique {
			t.Errorf("%s has no UNIQUE (host_id, %s); the same natural key could be inserted twice",
				d.table, d.natural)
		}
	}
}

// The same natural key on two different hosts must both be storable. A unique
// constraint that forgot host_id would let the first host to report "sda" own
// that name across the whole fleet.
func TestIntegrationGroup2NaturalKeysAreUniquePerHostNotGlobally(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	var hostA, hostB int32
	if err := s.Pool().QueryRow(ctx,
		`INSERT INTO hosts (hostname) VALUES ('g2-host-a') RETURNING id`).Scan(&hostA); err != nil {
		t.Fatalf("insert host a: %v", err)
	}
	if err := s.Pool().QueryRow(ctx,
		`INSERT INTO hosts (hostname) VALUES ('g2-host-b') RETURNING id`).Scan(&hostB); err != nil {
		t.Fatalf("insert host b: %v", err)
	}

	for _, host := range []int32{hostA, hostB} {
		if _, err := s.Pool().Exec(ctx,
			`INSERT INTO devices (host_id, device) VALUES ($1, 'sda')`, host); err != nil {
			t.Fatalf("insert device for host %d: %v", host, err)
		}
	}

	// And a second "sda" on the SAME host must be rejected.
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO devices (host_id, device) VALUES ($1, 'sda')`, hostA); err == nil {
		t.Error("a duplicate device on one host was accepted; the natural key must be unique per host")
	}
}

// Deleting a host must take every dimension and event row with it, or the next
// host to reuse the id inherits a stranger's inventory.
func TestIntegrationGroup2TablesCascadeOnHostDelete(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	var hostID int32
	if err := s.Pool().QueryRow(ctx,
		`INSERT INTO hosts (hostname) VALUES ('cascade-g2') RETURNING id`).Scan(&hostID); err != nil {
		t.Fatalf("insert host: %v", err)
	}

	seed := []string{
		`INSERT INTO containers (host_id, container_key, name) VALUES ($1, 'proj/svc', 'c1')`,
		`INSERT INTO filesystems (host_id, label, mountpoint) VALUES ($1, 'root', '/')`,
		`INSERT INTO devices (host_id, device) VALUES ($1, 'sda')`,
		`INSERT INTO systemd_units (host_id, unit_name) VALUES ($1, 'ssh.service')`,
		`INSERT INTO host_addresses (host_id, iface, address, family) VALUES ($1, 'eth0', '10.0.0.1', 4)`,
		`INSERT INTO host_packages (host_id, name, version, arch, format) VALUES ($1, 'bash', '5.2', 'amd64', 'dpkg')`,
	}
	for _, q := range seed {
		if _, err := s.Pool().Exec(ctx, q, hostID); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}

	// The event tables hang off a dimension, not off hosts directly, so they
	// exercise a two-hop cascade: hosts -> systemd_units -> systemd_unit_events.
	var unitID int32
	if err := s.Pool().QueryRow(ctx,
		`SELECT id FROM systemd_units WHERE host_id = $1`, hostID).Scan(&unitID); err != nil {
		t.Fatalf("select unit: %v", err)
	}
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO systemd_unit_events (host_id, unit_id, ts, state)
		 VALUES ($1, $2, now(), 'failed')`, hostID, unitID); err != nil {
		t.Fatalf("seed systemd_unit_events: %v", err)
	}
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO package_events (host_id, ts, name, action, to_version)
		 VALUES ($1, now(), 'bash', 'upgrade', '5.3')`, hostID); err != nil {
		t.Fatalf("seed package_events: %v", err)
	}

	if _, err := s.Pool().Exec(ctx, `DELETE FROM hosts WHERE id = $1`, hostID); err != nil {
		t.Fatalf("delete host: %v", err)
	}

	for _, table := range []string{
		"containers", "filesystems", "devices", "systemd_units",
		"host_addresses", "host_packages", "systemd_unit_events", "package_events",
	} {
		var n int
		if err := s.Pool().QueryRow(ctx,
			`SELECT count(*) FROM `+table+` WHERE host_id = $1`, hostID).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if n != 0 {
			t.Errorf("%s kept %d rows after the host was deleted", table, n)
		}
	}
}

// host_addresses.address is inet, not text, so subnet queries work: "every
// host with an address in 172.19.0.0/16", "every host with a public IPv4". As
// text those queries are string matching and get the answer wrong.
func TestIntegrationHostAddressesSupportsSubnetQueries(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	var hostID int32
	if err := s.Pool().QueryRow(ctx,
		`INSERT INTO hosts (hostname) VALUES ('addr-host') RETURNING id`).Scan(&hostID); err != nil {
		t.Fatalf("insert host: %v", err)
	}

	for _, addr := range []string{"172.19.0.5", "10.0.0.1", "2001:db8::1"} {
		family := 4
		if addr == "2001:db8::1" {
			family = 6
		}
		if _, err := s.Pool().Exec(ctx,
			`INSERT INTO host_addresses (host_id, iface, address, family)
			 VALUES ($1, 'eth0', $2, $3)`, hostID, addr, family); err != nil {
			t.Fatalf("insert %s: %v", addr, err)
		}
	}

	var n int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM host_addresses WHERE address << '172.19.0.0/16'`).Scan(&n); err != nil {
		t.Fatalf("subnet query: %v", err)
	}
	if n != 1 {
		t.Errorf("addresses in 172.19.0.0/16 = %d, want 1", n)
	}

	// IPv4 and IPv6 are treated identically throughout; both must store.
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM host_addresses WHERE family = 6`).Scan(&n); err != nil {
		t.Fatalf("v6 query: %v", err)
	}
	if n != 1 {
		t.Errorf("IPv6 addresses = %d, want 1", n)
	}
}

// smart_attributes is raw-only BY DESIGN.
//
// SMART is read hourly, so a 5-minute bucket holds at most one reading and a
// 1-hour bucket exactly one -- the aggregates would restate the raw table at
// triple the storage.
//
// Pinned so a later change adding an aggregate has to move this deliberately,
// rather than shipping one without the retention policy nobody assigned it.
func TestIntegrationRawOnlyTablesHaveNoContinuousAggregates(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	for _, table := range []string{"smart_attributes"} {
		var n int
		if err := s.Pool().QueryRow(ctx, `
			SELECT count(*) FROM timescaledb_information.continuous_aggregates
			 WHERE hypertable_name = $1`, table).Scan(&n); err != nil {
			t.Fatalf("count aggregates on %s: %v", table, err)
		}
		if n != 0 {
			t.Errorf("%s has %d continuous aggregates, want 0 -- it is raw-only by design", table, n)
		}

		// Raw-only still means retained: a hypertable with no retention policy
		// grows without bound, and nothing reports that either.
		var retention int
		if err := s.Pool().QueryRow(ctx, `
			SELECT count(*) FROM timescaledb_information.jobs
			 WHERE proc_name = 'policy_retention' AND hypertable_name = $1`, table).Scan(&retention); err != nil {
			t.Fatalf("count retention on %s: %v", table, err)
		}
		if retention != 1 {
			t.Errorf("%s has %d retention policies, want 1", table, retention)
		}
	}
}
