package read

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/trick77/netra/internal/hub/store"
)

// Every number in tier.go mirrors 0001_init.sql and none of them is inferable
// from the migration at runtime -- tier selection has to know the retention
// horizon before it queries anything, which is the whole point of computing
// the window up front.
//
// So they are pinned here instead. Editing a retention interval or an
// end_offset in the migration without editing tier.go fails this test rather
// than silently returning a window the hub cannot answer: too wide, and the
// series starts late for no stated reason; too narrow, and a client is told
// data does not exist when it does.
func TestIntegrationTierSpecsMatchTheSchema(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	for name, fam := range families {
		for _, spec := range fam.tiers {
			rel := fam.relation(spec)

			t.Run(name+"/"+spec.name+"/retention", func(t *testing.T) {
				assertPolicyInterval(t, ctx, s.Pool(),
					"policy_retention", rel, "drop_after", spec.retention)
			})

			// Raw tiers have no refresh policy: there is nothing to
			// materialise, which is exactly why their lag is zero.
			if spec.lag == 0 {
				t.Run(name+"/"+spec.name+"/has no refresh policy", func(t *testing.T) {
					if n := countPolicies(t, ctx, s.Pool(),
						"policy_refresh_continuous_aggregate", rel); n != 0 {
						t.Errorf("%s has %d refresh policies but tier.go gives it no lag; "+
							"a relation that materialises is one whose live tail this table "+
							"is meant to describe", rel, n)
					}
				})
				continue
			}

			t.Run(name+"/"+spec.name+"/lag", func(t *testing.T) {
				assertPolicyInterval(t, ctx, s.Pool(),
					"policy_refresh_continuous_aggregate", rel, "end_offset", spec.lag)
			})

			// The other half of the same policy. end_offset says how far back
			// a refresh RUN reaches; schedule_interval says how long it can be
			// since one ran, and together they bound how much of a real-time
			// aggregate's answer is computed live rather than read. Pinned
			// separately, and each field mirrors exactly one value in the
			// migration, so a policy that quietly widened -- making every
			// query on the tier scan further back -- fails here instead of
			// only showing up as a slow page.
			t.Run(name+"/"+spec.name+"/refresh schedule", func(t *testing.T) {
				assertScheduleInterval(t, ctx, s.Pool(), rel, spec.refreshEvery)
			})
		}
	}
}

// sameQuantityAtEveryTier lists the value columns that may legitimately keep
// one name across tiers, with the reason each is exempt.
//
// The rule the exemptions are carved out of: an aggregate column named like
// its raw column is a bucket statistic wearing an instantaneous reading's
// clothes, and a client that ignores which tier answered reads it as the
// latter. These are the cases where the bucket value IS the raw quantity, so
// there is nothing to confuse:
//
//   - last() over the bucket -- the value at the bucket's end, which is what
//     the raw column holds at that instant.
//   - max() of a MONOTONIC counter -- likewise the value at the bucket's end,
//     because the counter only rises within it.
//
// max() of a capacity is NOT in either category, which is why
// filesystem_samples_5m spells its as total_max and container_samples_5m as
// mem_limit_max: a filesystem resized down mid-bucket makes the bucket
// maximum and the reading genuinely different numbers.
var sameQuantityAtEveryTier = map[string]string{
	"uptime_s":                   "last() over the bucket",
	"boot_time_s":                "last() over the bucket",
	"processes_total":            "last() over the bucket",
	"users_logged_in":            "last() over the bucket",
	"services_total":             "last() over the bucket",
	"services_failed":            "last() over the bucket",
	"error_code":                 "last() over the bucket",
	"buffer_dropped_total":       "max() of a monotonic counter",
	"post_failures_total":        "max() of a monotonic counter",
	"oom_kill_total":             "last() over the bucket",
	"hub_connect_failures_total": "max() of a monotonic counter",
	// Ceilings, and named _limit rather than _max precisely so that keeping
	// one name across tiers cannot be misread as a bucket maximum. Unlike a
	// filesystem's total, these are kernel tunables: raising one is a
	// deliberate sysctl, not something that happens mid-bucket, so the last
	// reading and the reading ARE the same number.
	"fd_limit":         "last() over the bucket",
	"conntrack_limit":  "last() over the bucket",
	"tcp_tw_limit":     "last() over the bucket",
	"tcp_orphan_limit": "last() over the bucket",
}

// The read API's central guarantee, enumerated rather than asserted.
//
// metrics.go claims a client that ignores `tier` cannot silently mix
// resolutions, because the column names differ per tier BY CONSTRUCTION: busy
// at raw, busy_avg and busy_max at 5m. That claim is only worth making if it
// holds for EVERY family and EVERY column, and the test that used to guard it
// checked one column of one family while asserting the universal rule in its
// failure message -- so it gave more confidence than it earned. Three capacity
// columns did in fact share a name, and this is the test that found them.
//
// A new continuous aggregate that reintroduces a shared name fails here rather
// than in a chart drawn six months later.
func TestIntegrationNoValueColumnNameIsSharedBetweenTiers(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	svc := NewService(s.Pool())

	for name, fam := range families {
		if len(fam.tiers) < 2 {
			continue
		}

		// Which tiers each column name appears at.
		appearsAt := map[string][]string{}
		for _, spec := range fam.tiers {
			cols, err := svc.valueColumns(ctx, fam, spec)
			if err != nil {
				t.Fatalf("%s/%s columns: %v", name, spec.name, err)
			}
			for _, c := range cols {
				appearsAt[c.name] = append(appearsAt[c.name], spec.name)
			}
		}

		for col, tiers := range appearsAt {
			if len(tiers) < 2 {
				continue
			}
			// Shared between the two AGGREGATE tiers alone is fine and
			// expected -- 5m and 1h roll up identically. The confusion this
			// guards against needs raw on one side.
			if !slices.Contains(tiers, TierRaw) {
				continue
			}
			if reason, exempt := sameQuantityAtEveryTier[col]; exempt {
				t.Logf("%s.%s is shared across %v, exempt: %s", name, col, tiers, reason)
				continue
			}
			t.Errorf("family %s: column %q exists at tiers %v including raw.\n"+
				"    A client that ignores `tier` reads the bucketed value as an instantaneous one.\n"+
				"    Either give the aggregate column a distinct name (total -> total_max), or add it\n"+
				"    to sameQuantityAtEveryTier with the reason the bucket value IS the raw value.",
				name, col, tiers)
		}
	}
}

// Every continuous aggregate must be a REAL-TIME aggregate.
//
// The inverse of this test used to stand here, and it was right at the time:
// tier.go clamped every window back to now - (end_offset + schedule_interval)
// on the assumption that a query past the watermark returned nothing, so an
// aggregate created with real-time aggregation on would have had its freshest
// buckets thrown away by a clamp that no longer needed to exist.
//
// 0014_realtime_aggregates.sql flipped every view and planQuery dropped the
// clamp, so the assumption runs the other way now: a view that goes back to
// materialized_only silently loses its newest fifteen minutes at the 5m tier
// and its newest ninety at 1h, with nothing on the chart to say so. That is
// the defect the migration exists to fix -- a saturation absent from the 24h
// chart for a quarter of an hour -- so it is the one worth failing a build
// over.
func TestIntegrationEveryAggregateIsRealTime(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	rows, err := s.Pool().Query(ctx, `
		SELECT view_name FROM timescaledb_information.continuous_aggregates
		 WHERE materialized_only`)
	if err != nil {
		t.Fatalf("query aggregates: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var view string
		if err := rows.Scan(&view); err != nil {
			t.Fatalf("scan: %v", err)
		}
		t.Errorf("%s is materialized_only; planQuery answers to the last CLOSED bucket "+
			"on the assumption that every aggregate is real-time, so this view now "+
			"returns nothing for the window past its watermark", view)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate: %v", err)
	}
}

func assertPolicyInterval(t *testing.T, ctx context.Context, pool *pgxpool.Pool,
	procName, relation, key string, want time.Duration,
) {
	t.Helper()

	var actual time.Duration
	err := pool.QueryRow(ctx, fmt.Sprintf(`
		SELECT (config ->> '%s')::interval
		  FROM timescaledb_information.jobs
		 WHERE proc_name = $1 AND hypertable_name = $2`, key), procName, relation).Scan(&actual)
	if err != nil {
		t.Fatalf("read %s.%s for %s: %v", procName, key, relation, err)
	}
	if actual != want {
		t.Errorf("%s %s = %s, but tier.go says %s", relation, key, actual, want)
	}
}

// schedule_interval is a COLUMN of timescaledb_information.jobs, not a key
// inside its config JSON the way end_offset is -- reading it through
// assertPolicyInterval above returns NULL and fails on the scan rather than on
// the comparison, which says nothing useful about the schema.
func assertScheduleInterval(t *testing.T, ctx context.Context, pool *pgxpool.Pool,
	relation string, want time.Duration,
) {
	t.Helper()

	var actual time.Duration
	err := pool.QueryRow(ctx, `
		SELECT schedule_interval
		  FROM timescaledb_information.jobs
		 WHERE proc_name = 'policy_refresh_continuous_aggregate'
		   AND hypertable_name = $1`, relation).Scan(&actual)
	if err != nil {
		t.Fatalf("read schedule_interval for %s: %v", relation, err)
	}
	if actual != want {
		t.Errorf("%s schedule_interval = %s, but tier.go says %s", relation, actual, want)
	}
}

func countPolicies(t *testing.T, ctx context.Context, pool *pgxpool.Pool, procName, relation string) int {
	t.Helper()

	var n int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM timescaledb_information.jobs
		 WHERE proc_name = $1 AND hypertable_name = $2`, procName, relation).Scan(&n); err != nil {
		t.Fatalf("count %s for %s: %v", procName, relation, err)
	}
	return n
}
