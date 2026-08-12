package store_test

import (
	"context"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
	"github.com/trick77/netra/internal/hub/store"
)

// A column added to host_samples but forgotten in the two continuous
// aggregates is the defect this file exists to prevent, and it is invisible
// until someone looks past the raw retention window. cpu_user, mem_total,
// swap_total, load5 and load15 all shipped that way: present in the raw table
// and absent from both rollups, so their panels silently empty out above 6h.
//
// The pressure and exhaustion columns are the ones most likely to be looked
// at long after the fact -- "was this host thrashing last Tuesday" is not a
// question anyone asks inside the raw window -- so the rollups are asserted
// here rather than assumed.
//
// Two 5m buckets per series, with different values, are what make a FUNCTION
// transposition visible: with one input row the 1h tier's avg, max and last
// are all the same number, and max-where-avg-was-meant passes silently.
func TestIntegrationPressureColumnsSurviveBothRollupTiers(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	hostID := seedHost(t, s)
	base := rollupHour()

	// Two rows in the first 5m bucket, one in a later bucket of the same
	// hour. The peak (90) sits beside a low value so an avg cannot pass for
	// a max, and oom_kill_total climbs so last() cannot pass for max().
	samples := []*netrav1.HostSample{
		{
			TsMs:           base.UnixMilli(),
			PgmajfaultPerS: proto.Float64(10),
			SocketsUsed:    proto.Uint32(100),
			OomKillTotal:   proto.Uint64(1),
			FdUsed:         proto.Uint64(500),
			FdLimit:        proto.Uint64(1024),
			ConntrackCount: proto.Uint32(50),
			ConntrackLimit: proto.Uint32(65536),
		},
		{
			TsMs:           base.Add(time.Minute).UnixMilli(),
			PgmajfaultPerS: proto.Float64(90),
			SocketsUsed:    proto.Uint32(300),
			OomKillTotal:   proto.Uint64(2),
			FdUsed:         proto.Uint64(700),
			FdLimit:        proto.Uint64(1024),
			ConntrackCount: proto.Uint32(150),
			ConntrackLimit: proto.Uint32(65536),
		},
		{
			TsMs:           base.Add(7 * time.Minute).UnixMilli(),
			PgmajfaultPerS: proto.Float64(20),
			SocketsUsed:    proto.Uint32(200),
			OomKillTotal:   proto.Uint64(5),
			FdUsed:         proto.Uint64(600),
			FdLimit:        proto.Uint64(1024),
			ConntrackCount: proto.Uint32(100),
			ConntrackLimit: proto.Uint32(65536),
		},
	}
	if _, err := s.InsertHostSamples(ctx, hostID, samples); err != nil {
		t.Fatalf("InsertHostSamples: %v", err)
	}

	refreshTiers(t, s, "host_samples")

	// 5m: bucket one averages 10 and 90 to 50 and peaks at 90; bucket two
	// holds the single 20.
	assertHostTier(t, s, "host_samples_5m", "pgmajfault_per_s_avg", hostID, []float64{50, 20})
	assertHostTier(t, s, "host_samples_5m", "pgmajfault_per_s_max", hostID, []float64{90, 20})
	assertHostTier(t, s, "host_samples_5m", "sockets_used_max", hostID, []float64{300, 200})
	// A running total takes the last reading in the bucket, not the largest
	// -- the two agree here only because the counter is monotonic, which is
	// why the 1h assertion below matters more.
	assertHostTier(t, s, "host_samples_5m", "oom_kill_total", hostID, []float64{2, 5})
	assertHostTier(t, s, "host_samples_5m", "fd_limit", hostID, []float64{1024, 1024})
	assertHostTier(t, s, "host_samples_5m", "conntrack_limit", hostID, []float64{65536, 65536})

	// 1h: both 5m buckets collapse into one. avg of avgs (50, 20) is 35;
	// max of maxes is 90. Taking max(x_avg) here would report 50 -- the
	// busiest five minutes -- as though it were an instantaneous peak.
	assertHostTier(t, s, "host_samples_1h", "pgmajfault_per_s_avg", hostID, []float64{35})
	assertHostTier(t, s, "host_samples_1h", "pgmajfault_per_s_max", hostID, []float64{90})
	assertHostTier(t, s, "host_samples_1h", "sockets_used_max", hostID, []float64{300})
	// last of lasts: 5, not the 2 of the earlier bucket and not a sum.
	assertHostTier(t, s, "host_samples_1h", "oom_kill_total", hostID, []float64{5})
	assertHostTier(t, s, "host_samples_1h", "fd_used_max", hostID, []float64{700})
	assertHostTier(t, s, "host_samples_1h", "conntrack_count_max", hostID, []float64{150})
}

// assertHostTier reads one column of a host_samples rollup in bucket order.
//
// Separate from assertTier because the host tiers carry no dimension column:
// there is one row per (host, bucket) rather than one per device or core.
func assertHostTier(t *testing.T, s *store.Store, view, column string, hostID int32, want []float64) {
	t.Helper()

	// Cast in SQL: these columns are variously double precision, bigint and
	// numeric (avg of an integer column), and pgx will not scan the last into
	// a float64.
	//nolint:gosec // view and column are literals from this test
	rows, err := s.Pool().Query(context.Background(),
		`SELECT `+column+`::double precision FROM `+view+`
		  WHERE host_id = $1 ORDER BY bucket`, hostID)
	if err != nil {
		t.Fatalf("query %s.%s: %v", view, column, err)
	}
	defer rows.Close()

	var got []float64
	for rows.Next() {
		var v *float64
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("scan %s.%s: %v", view, column, err)
		}
		if v == nil {
			t.Fatalf("%s.%s is NULL; the column reached the raw table but not this tier", view, column)
		}
		got = append(got, *v)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows %s.%s: %v", view, column, err)
	}

	if len(got) != len(want) {
		t.Fatalf("%s.%s returned %d buckets %v, want %d %v", view, column, len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s.%s bucket %d = %v, want %v", view, column, i, got[i], want[i])
		}
	}
}
