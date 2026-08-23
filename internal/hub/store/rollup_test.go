package store_test

import (
	"context"
	"testing"

	"github.com/trick77/netra/internal/hub/store"
)

func TestIntegrationHostSamplesIsHypertable(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	var n int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM timescaledb_information.hypertables
		 WHERE hypertable_name = 'host_samples'`).Scan(&n); err != nil {
		t.Fatalf("query: %v", err)
	}
	if n != 1 {
		t.Fatalf("host_samples hypertable count = %d, want 1", n)
	}
}

// start_offset must exceed the agent ring-buffer window, or data replayed
// after an outage is recorded as invalid but never re-materialised, leaving
// the rollup permanently wrong. Buffer is 1h, so the floor is 1h.
func TestIntegrationRefreshPolicyStartOffsetExceedsBufferWindow(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	rows, err := s.Pool().Query(ctx,
		`SELECT config ->> 'start_offset'
		   FROM timescaledb_information.jobs
		  WHERE proc_name = 'policy_refresh_continuous_aggregate'`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()

	seen := 0
	for rows.Next() {
		var startOffset string
		if err := rows.Scan(&startOffset); err != nil {
			t.Fatalf("scan: %v", err)
		}
		seen++

		var greater bool
		if err := s.Pool().QueryRow(ctx,
			`SELECT $1::interval > interval '1 hour'`, startOffset).Scan(&greater); err != nil {
			t.Fatalf("compare: %v", err)
		}
		if !greater {
			t.Fatalf("start_offset = %s, want greater than the 1h buffer window", startOffset)
		}
	}
	// host_samples, host_snmp_samples, host_proto_samples, agent_samples and
	// the five Group 1 hypertables (cpu_core_samples, disk_io_samples,
	// sensor_samples, net_samples, collector_samples), each with a 5m and a 1h
	// aggregate. This literal is deliberately hard-coded rather than derived:
	// a new hypertable whose refresh policy is forgotten is a permanently
	// silent failure, so adding one must break this test until it is counted.
	if seen != 22 {
		t.Fatalf("refresh policies found = %d, want 22 "+
			"(host_samples, host_snmp_samples, host_proto_samples, "+
			"agent_samples, the five Group 1 tables, plus container_samples "+
			"and filesystem_samples, 5m and 1h each)", seen)
	}
}

// TestIntegrationBackfillOlderThanStartOffsetIsExcludedFromRollup is the
// regression spec §10 and §14 both call for: a sample older than the
// continuous aggregate's start_offset must not appear in the rollup after a
// refresh, while a sample within the window must. Asserting only that the
// migration's start_offset config says "6 hours" (as the tests above do)
// restates the constraint; this exercises it against real TimescaleDB
// refresh behaviour, which is the part that can silently regress.
func TestIntegrationBackfillOlderThanStartOffsetIsExcludedFromRollup(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	var hostID int32
	if err := s.Pool().QueryRow(ctx,
		`INSERT INTO hosts (hostname) VALUES ('rollup-host') RETURNING id`).Scan(&hostID); err != nil {
		t.Fatalf("insert host: %v", err)
	}

	// Mirrors host_samples_5m's policy: start_offset 6h, end_offset 10m.
	// tooOld sits outside the refreshed range and must never be
	// materialised; withinWindow sits inside it and must be.
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO host_samples (host_id, ts, cpu_total)
		 VALUES ($1, now() - interval '8 hours', $2)`, hostID, 11.0); err != nil {
		t.Fatalf("insert too-old sample: %v", err)
	}
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO host_samples (host_id, ts, cpu_total)
		 VALUES ($1, now() - interval '1 hour', $2)`, hostID, 22.0); err != nil {
		t.Fatalf("insert within-window sample: %v", err)
	}

	// Unschedule the policy jobs before refreshing by hand. A background
	// refresh that is already running makes a manual one fail outright with
	// "concurrent refresh" rather than wait, and the schema now carries
	// fourteen aggregates whose policies all fire within seconds of being
	// created. See refreshTiers in group1rollup_test.go.
	if _, err := s.Pool().Exec(ctx,
		`SELECT alter_job(job_id, scheduled => false)
		   FROM timescaledb_information.jobs
		  WHERE proc_name = 'policy_refresh_continuous_aggregate'`); err != nil {
		t.Fatalf("unschedule refresh policies: %v", err)
	}

	// Mirror host_samples_5m's own policy window rather than refreshing
	// everything: the point of the test is that the 8-hour-old sample falls
	// outside start_offset, which only holds if the refresh stops at 6h.
	// refreshAggregateRange carries the 55P03 retry, because unscheduling
	// cannot recall a worker that has already started.
	refreshAggregateRange(t, s, "host_samples_5m",
		"now() - interval '6 hours'", "now() - interval '10 minutes'")

	var tooOldCount int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM host_samples_5m
		  WHERE host_id = $1 AND bucket < now() - interval '6 hours'`, hostID).Scan(&tooOldCount); err != nil {
		t.Fatalf("query too-old bucket: %v", err)
	}
	if tooOldCount != 0 {
		t.Fatalf("rollup rows older than start_offset = %d, want 0 (silently excluded forever)", tooOldCount)
	}

	var withinWindowCount int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM host_samples_5m
		  WHERE host_id = $1 AND bucket >= now() - interval '6 hours'
		    AND bucket <= now() - interval '10 minutes'`, hostID).Scan(&withinWindowCount); err != nil {
		t.Fatalf("query within-window bucket: %v", err)
	}
	if withinWindowCount == 0 {
		t.Fatal("rollup rows within the refreshed window = 0, want at least 1")
	}
}

// Raw retention must exceed the refresh lag on EVERY raw hypertable, not
// just host_samples: a chunk dropped before the 5m aggregate has materialised
// it is gone from both tiers, and nothing reports that it happened. Sweeping
// all of them means a new hypertable given a too-short retention fails here
// rather than losing data quietly months later.
func TestIntegrationRawRetentionExceedsRefreshLag(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	rows, err := s.Pool().Query(ctx,
		`SELECT hypertable_name, config ->> 'drop_after'
		   FROM timescaledb_information.jobs
		  WHERE proc_name = 'policy_retention'
		    AND hypertable_name NOT IN (
		        SELECT view_name FROM timescaledb_information.continuous_aggregates)`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()

	seen := 0
	for rows.Next() {
		var name, dropAfter string
		if err := rows.Scan(&name, &dropAfter); err != nil {
			t.Fatalf("scan: %v", err)
		}
		seen++

		var ok bool
		if err := s.Pool().QueryRow(ctx,
			`SELECT $1::interval > interval '6 hours'`, dropAfter).Scan(&ok); err != nil {
			t.Fatalf("compare %s: %v", name, err)
		}
		if !ok {
			t.Errorf("%s drop_after = %s, want greater than the 6h start_offset", name, dropAfter)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate: %v", err)
	}

	// Twelve raw hypertables. Continuous aggregates carry their own retention
	// policies too, and Timescale reports those under the aggregate's own
	// view name rather than the internal materialisation hypertable — hence
	// the anti-join above rather than a name pattern.
	if seen != 12 {
		t.Fatalf("raw retention policies found = %d, want 12 "+
			"(host_samples, host_snmp_samples, host_proto_samples, "+
			"agent_samples and the five Group 1 tables, plus "+
			"container_samples, filesystem_samples and smart_attributes)", seen)
	}
}

// Every continuous aggregate needs a retention policy of its own; without one
// the 5m and 1h tiers grow without bound while the raw tier is trimmed, which
// looks like nothing at all until the disk fills. Counted against the
// aggregate list rather than enumerated by name, so a new aggregate with no
// retention policy fails here without anyone maintaining a second list.
func TestIntegrationEveryContinuousAggregateHasRetention(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	var aggregates, policies int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM timescaledb_information.continuous_aggregates`).Scan(&aggregates); err != nil {
		t.Fatalf("count aggregates: %v", err)
	}
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM timescaledb_information.jobs
		  WHERE proc_name = 'policy_retention'
		    AND hypertable_name IN (
		        SELECT view_name FROM timescaledb_information.continuous_aggregates)`).Scan(&policies); err != nil {
		t.Fatalf("count aggregate retention policies: %v", err)
	}

	if aggregates != 22 {
		t.Fatalf("continuous aggregates found = %d, want 22", aggregates)
	}
	if policies != aggregates {
		t.Fatalf("aggregate retention policies = %d, want %d (one per aggregate)", policies, aggregates)
	}
}
