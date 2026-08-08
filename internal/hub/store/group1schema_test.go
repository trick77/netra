package store_test

import (
	"context"
	"testing"

	"github.com/trick77/netra/internal/hub/store"
)

// group1Hypertables are the five hypertables the Group 1 collectors write
// into (spec §5.3). Each ships with its 5m and 1h continuous aggregates and
// all three retention policies, so no tier is ever half-configured.
var group1Hypertables = []string{
	"cpu_core_samples",
	"disk_io_samples",
	"sensor_samples",
	"net_samples",
	"collector_samples",
}

func TestIntegrationGroup1TablesAreHypertables(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	for _, name := range group1Hypertables {
		var n int
		if err := s.Pool().QueryRow(ctx,
			`SELECT count(*) FROM timescaledb_information.hypertables
			  WHERE hypertable_name = $1`, name).Scan(&n); err != nil {
			t.Fatalf("query %s: %v", name, err)
		}
		if n != 1 {
			t.Errorf("%s hypertable count = %d, want 1", name, n)
		}
	}
}

// Every Group 1 table carries a dimension alongside (host_id, ts) — one row
// per core, per device, per interface, per sensor, per collector, all sharing
// the scrape's single timestamp. Inheriting host_samples' PRIMARY KEY
// (host_id, ts) would therefore be silently catastrophic: ingest deduplicates
// with ON CONFLICT DO NOTHING (spec §5.5), so sixteen cores would collapse to
// one surviving row per scrape with no error raised anywhere. This asserts
// the dimension is part of the key by writing two rows that differ in nothing
// else.
func TestIntegrationGroup1PrimaryKeysIncludeTheirDimension(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	hostID := seedHost(t, s)
	ts := recentBucket()

	sensorA := seedSensor(t, s, hostID, "coretemp", "Package id 0")
	sensorB := seedSensor(t, s, hostID, "coretemp", "Core 0")

	cases := []struct {
		table  string
		insert string
		args   [2]any
	}{
		{
			table:  "cpu_core_samples",
			insert: `INSERT INTO cpu_core_samples (host_id, ts, core, busy) VALUES ($1, $2, $3, 12.5)`,
			args:   [2]any{int32(0), int32(1)},
		},
		{
			table:  "disk_io_samples",
			insert: `INSERT INTO disk_io_samples (host_id, ts, device, read_bytes) VALUES ($1, $2, $3, 1024)`,
			args:   [2]any{"sda", "nvme0n1"},
		},
		{
			table:  "net_samples",
			insert: `INSERT INTO net_samples (host_id, ts, iface, rx_bytes) VALUES ($1, $2, $3, 2048)`,
			args:   [2]any{"eth0", "eth1"},
		},
		{
			table:  "sensor_samples",
			insert: `INSERT INTO sensor_samples (host_id, ts, sensor_id, temp) VALUES ($1, $2, $3, 41.0)`,
			args:   [2]any{sensorA, sensorB},
		},
		{
			table:  "collector_samples",
			insert: `INSERT INTO collector_samples (host_id, ts, collector, duration_ms, ok) VALUES ($1, $2, $3, 7, TRUE)`,
			args:   [2]any{"cpu", "memory"},
		},
	}

	for _, tc := range cases {
		for _, dimension := range tc.args {
			if _, err := s.Pool().Exec(ctx, tc.insert, hostID, ts, dimension); err != nil {
				t.Fatalf("insert into %s (dimension %v): %v", tc.table, dimension, err)
			}
		}

		var n int
		if err := s.Pool().QueryRow(ctx,
			`SELECT count(*) FROM `+tc.table+` WHERE host_id = $1 AND ts = $2`,
			hostID, ts).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", tc.table, err)
		}
		if n != 2 {
			t.Errorf("%s rows for one scrape = %d, want 2 "+
				"(the dimension is missing from the primary key, so rows are "+
				"silently discarded by ON CONFLICT DO NOTHING)", tc.table, n)
		}
	}
}

// The sensors dimension is keyed on chip + label, never on hwmonN: the hwmon
// index is assigned in probe order and moves between boots, so keying on it
// forks a sensor's history every time the kernel enumerates differently.
func TestIntegrationSensorsNaturalKeyIsChipAndLabelPerHost(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	hostID := seedHost(t, s)

	seedSensor(t, s, hostID, "coretemp", "Package id 0")

	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO sensors (host_id, chip, label) VALUES ($1, 'coretemp', 'Package id 0')`,
		hostID); err == nil {
		t.Error("duplicate (host_id, chip, label) was accepted, want a unique violation")
	}

	// The same chip and label on another host is a different sensor, not a
	// collision: every host has a coretemp Package id 0.
	var otherHost int32
	if err := s.Pool().QueryRow(ctx,
		`INSERT INTO hosts (hostname) VALUES ('sensors-other-host') RETURNING id`).Scan(&otherHost); err != nil {
		t.Fatalf("insert second host: %v", err)
	}
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO sensors (host_id, chip, label) VALUES ($1, 'coretemp', 'Package id 0')`,
		otherHost); err != nil {
		t.Fatalf("same chip and label on a second host: %v", err)
	}
}

// sensor_samples carries both host_id and sensor_id, and a foreign key on
// sensor_id alone lets the two disagree: a row with host B's host_id and host
// A's sensor_id inserts cleanly, and the join then attributes A's chip and
// label to B's sample. Worse, deleting host A cascades away rows belonging to
// B. Every other Group 1 table's dimension is self-describing; this is the
// one that needs the composite key to say so.
func TestIntegrationSensorSamplesCannotReferenceAnotherHostsSensor(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	hostA := seedHost(t, s)
	var hostB int32
	if err := s.Pool().QueryRow(ctx,
		`INSERT INTO hosts (hostname) VALUES ('sensor-fk-host-b') RETURNING id`).Scan(&hostB); err != nil {
		t.Fatalf("insert second host: %v", err)
	}
	sensorA := seedSensor(t, s, hostA, "coretemp", "Package id 0")

	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO sensor_samples (host_id, ts, sensor_id, temp) VALUES ($1, $2, $3, 40.0)`,
		hostB, recentBucket(), sensorA); err == nil {
		t.Error("host B accepted a sample referencing host A's sensor, want a foreign key violation")
	}

	// The same sensor with its own host is of course fine.
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO sensor_samples (host_id, ts, sensor_id, temp) VALUES ($1, $2, $3, 40.0)`,
		hostA, recentBucket(), sensorA); err != nil {
		t.Fatalf("host A's own sensor: %v", err)
	}
}

// events is a plain Postgres table (spec §5.2), not a hypertable. mdraid
// degradation, SMART threshold crossings and public-IP changes land here
// precisely because they are constant for hours: spec §5.1 rule 4 sends
// discrete state changes to events rather than adding a near-constant series
// per array. Making it a hypertable would also add refresh and retention
// policies that the counts in rollup_test.go deliberately pin.
func TestIntegrationEventsIsAPlainTableNotAHypertable(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	var tables int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM information_schema.tables
		  WHERE table_schema = 'public' AND table_name = 'events'`).Scan(&tables); err != nil {
		t.Fatalf("query information_schema: %v", err)
	}
	if tables != 1 {
		t.Fatalf("events table count = %d, want 1", tables)
	}

	var hypertables int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM timescaledb_information.hypertables
		  WHERE hypertable_name = 'events'`).Scan(&hypertables); err != nil {
		t.Fatalf("query timescaledb_information: %v", err)
	}
	if hypertables != 0 {
		t.Errorf("events hypertable count = %d, want 0 (spec §5.2 lists it as a plain table)", hypertables)
	}
}

// Replayed batches are harmless (spec §5.5), and that has to hold for events
// too: the agent re-posts its ring buffer after an outage, so the same
// degradation event arrives twice. subject is nullable — an agent-version
// change has no subject — which is why the index needs NULLS NOT DISTINCT.
func TestIntegrationEventsDedupeOnReplay(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	hostID := seedHost(t, s)
	ts := recentBucket()

	const insert = `INSERT INTO events (host_id, ts, type, subject, detail)
	                VALUES ($1, $2, 'mdraid', $3, '{"state":"degraded"}'::jsonb)
	                ON CONFLICT DO NOTHING`

	for range 2 {
		if _, err := s.Pool().Exec(ctx, insert, hostID, ts, "md0"); err != nil {
			t.Fatalf("insert event: %v", err)
		}
	}
	for range 2 {
		if _, err := s.Pool().Exec(ctx, insert, hostID, ts, nil); err != nil {
			t.Fatalf("insert subjectless event: %v", err)
		}
	}

	var n int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM events WHERE host_id = $1`, hostID).Scan(&n); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if n != 2 {
		t.Errorf("events after replaying each row twice = %d, want 2", n)
	}
}

// seedSensor inserts one sensor dimension row and returns its surrogate id.
func seedSensor(t *testing.T, s *store.Store, hostID int32, chip, label string) int32 {
	t.Helper()

	var id int32
	if err := s.Pool().QueryRow(context.Background(),
		`INSERT INTO sensors (host_id, chip, label) VALUES ($1, $2, $3) RETURNING id`,
		hostID, chip, label).Scan(&id); err != nil {
		t.Fatalf("insert sensor %s/%s: %v", chip, label, err)
	}
	return id
}
