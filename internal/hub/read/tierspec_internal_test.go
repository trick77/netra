package read

import (
	"context"
	"fmt"
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
							"a materialized_only relation that lags is queried past its horizon", rel, n)
					}
				})
				continue
			}

			t.Run(name+"/"+spec.name+"/lag", func(t *testing.T) {
				assertPolicyInterval(t, ctx, s.Pool(),
					"policy_refresh_continuous_aggregate", rel, "end_offset", spec.lag)
			})
		}
	}
}

// Every continuous aggregate must stay materialized_only. If one is ever
// created with real-time aggregation on, its data is fresh to now and the lag
// clamp in tier.go turns from a correctness fix into a bug that hides the most
// recent hour.
func TestIntegrationEveryAggregateIsMaterializedOnly(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	rows, err := s.Pool().Query(ctx, `
		SELECT view_name FROM timescaledb_information.continuous_aggregates
		 WHERE NOT materialized_only`)
	if err != nil {
		t.Fatalf("query aggregates: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var view string
		if err := rows.Scan(&view); err != nil {
			t.Fatalf("scan: %v", err)
		}
		t.Errorf("%s is not materialized_only; tier.go clamps every aggregate query to "+
			"now - end_offset on the assumption that it is", view)
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
