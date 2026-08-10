package read_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/trick77/netra/internal/hub/read"
)

// refresh materialises a continuous aggregate.
//
// Every aggregate in 0001_init.sql is created WITH NO DATA and OpenTest
// unschedules the refresh policies, so an insert-then-query against a _5m or
// _1h view returns nothing at all unless this runs first. That is not a quirk
// of the test harness: it is the same materialized_only behaviour tier.go
// clamps the query window for.
func refresh(t *testing.T, pool *pgxpool.Pool, view string) {
	t.Helper()

	if _, err := pool.Exec(context.Background(),
		`CALL refresh_continuous_aggregate('`+view+`', NULL, NULL)`); err != nil {
		t.Fatalf("refresh %s: %v", view, err)
	}
}

// seedHostSamples writes one host_samples row per offset before now.
func seedHostSamples(t *testing.T, pool *pgxpool.Pool, hostID int32, offsets ...time.Duration) {
	t.Helper()

	for i, off := range offsets {
		exec(t, pool, `
			INSERT INTO host_samples (host_id, ts, cpu_total, mem_used, load1)
			VALUES ($1, now() - $2::interval, $3, $4, 0.5)`,
			hostID, off.String(), float64(10+i), int64(1000+i))
	}
}

// The raw tier answers a recent window, and its column names are the table's
// own -- cpu_total, not cpu_total_avg.
func TestIntegrationMetricsRawTierRoundTrip(t *testing.T) {
	ctx := context.Background()
	svc, pool := newService(t)
	now := time.Now()
	id := seedHost(t, pool, "raw-host")

	seedHostSamples(t, pool, id, 30*time.Minute, 20*time.Minute, 10*time.Minute)

	res, err := svc.Metrics(ctx, read.MetricsQuery{
		HostID: id, Family: "host", From: now.Add(-2 * time.Hour), To: now,
		Columns: []string{"cpu_total", "mem_used"},
	}, now)
	if err != nil {
		t.Fatalf("Metrics: %v", err)
	}

	if res.Tier != read.TierRaw {
		t.Errorf("tier = %q, want %q", res.Tier, read.TierRaw)
	}
	if res.StepS != 60 {
		t.Errorf("step_s = %d, want 60", res.StepS)
	}
	if !slices.Equal(res.Columns, []string{"cpu_total", "mem_used"}) {
		t.Errorf("columns = %v, want the raw names", res.Columns)
	}
	if len(res.KeyColumns) != 0 {
		t.Errorf("key_columns = %v, want none -- host is a single-series family", res.KeyColumns)
	}
	if len(res.Series) != 1 {
		t.Fatalf("got %d series, want 1", len(res.Series))
	}
	if got := len(res.Series[0].Points); got != 3 {
		t.Fatalf("got %d points, want 3", got)
	}

	// A point is [ts_ms, then one value per column, in order].
	first := res.Series[0].Points[0]
	if len(first) != 3 {
		t.Fatalf("point = %v, want a timestamp and two values", first)
	}
	if ts, ok := first[0].(int64); !ok || ts <= 0 {
		t.Errorf("point[0] = %v, want a unix millisecond timestamp", first[0])
	}
	if v, ok := first[1].(float64); !ok || v != 10 {
		t.Errorf("cpu_total = %v, want 10", first[1])
	}
}

// THE load-bearing property. A client that ignores `tier` still cannot plot a
// five-minute mean as if it were a sixty-second sample, because the column it
// would read does not exist at the other tier: busy at raw, busy_avg and
// busy_max at 5m. It gets a key it does not recognise rather than a number
// that looks fine and is wrong.
func TestIntegrationMetricsColumnNamesDifferBetweenTiers(t *testing.T) {
	ctx := context.Background()
	svc, pool := newService(t)
	now := time.Now()
	id := seedHost(t, pool, "tiered")

	seedHostSamples(t, pool, id, 40*time.Minute, 35*time.Minute, 30*time.Minute)
	refresh(t, pool, "host_samples_5m")

	raw, err := svc.Metrics(ctx, read.MetricsQuery{
		HostID: id, Family: "host", From: now.Add(-2 * time.Hour), To: now,
		Step: time.Minute, StepSet: true,
	}, now)
	if err != nil {
		t.Fatalf("Metrics(raw): %v", err)
	}
	rolled, err := svc.Metrics(ctx, read.MetricsQuery{
		HostID: id, Family: "host", From: now.Add(-2 * time.Hour), To: now,
		Step: 5 * time.Minute, StepSet: true,
	}, now)
	if err != nil {
		t.Fatalf("Metrics(5m): %v", err)
	}

	if rolled.Tier != read.Tier5m || rolled.StepS != 300 {
		t.Fatalf("rolled tier = %q step_s = %d, want 5m/300", rolled.Tier, rolled.StepS)
	}
	if !slices.Contains(raw.Columns, "cpu_total") {
		t.Errorf("raw columns = %v, want cpu_total", raw.Columns)
	}
	if slices.Contains(rolled.Columns, "cpu_total") {
		t.Errorf("5m columns include cpu_total; the tiers must not share a column name, "+
			"or a client can mix resolutions without noticing: %v", rolled.Columns)
	}
	if !slices.Contains(rolled.Columns, "cpu_total_avg") || !slices.Contains(rolled.Columns, "cpu_total_max") {
		t.Errorf("5m columns = %v, want cpu_total_avg and cpu_total_max", rolled.Columns)
	}
	if len(rolled.Series) != 1 || len(rolled.Series[0].Points) == 0 {
		t.Fatalf("5m series = %+v, want materialised points", rolled.Series)
	}
}

// The trailing clamp, end to end: a 5m query asking for "up to now" comes back
// with a window ending ten minutes ago and says why. Without it the missing
// last ten minutes read as a host that stopped reporting.
func TestIntegrationMetricsReportsTheWindowItActuallyCovers(t *testing.T) {
	ctx := context.Background()
	svc, pool := newService(t)
	now := time.Now()
	id := seedHost(t, pool, "windowed")

	res, err := svc.Metrics(ctx, read.MetricsQuery{
		HostID: id, Family: "host", From: now.Add(-10 * 24 * time.Hour), To: now,
	}, now)
	if err != nil {
		t.Fatalf("Metrics: %v", err)
	}

	if res.Tier != read.Tier5m {
		t.Fatalf("tier = %q, want 5m", res.Tier)
	}
	if !res.Requested.To.Equal(now) {
		t.Errorf("requested_window.to = %v, want the echo of now", res.Requested.To)
	}
	if !res.Window.To.Before(now.Add(-9 * time.Minute)) {
		t.Errorf("window.to = %v, want it clamped ten minutes back from %v", res.Window.To, now)
	}
	if len(res.Warnings) == 0 {
		t.Error("warnings = [], want one explaining the clamp")
	}
}

// Each dimension value is its own series, and the key is the dimension, not
// the surrogate id: a core renders as its number, and every per-entity family
// that hides an id behind a join renders the joined name.
func TestIntegrationMetricsSplitsSeriesByDimension(t *testing.T) {
	ctx := context.Background()
	svc, pool := newService(t)
	now := time.Now()
	id := seedHost(t, pool, "many-cores")

	exec(t, pool, `
		INSERT INTO cpu_core_samples (host_id, ts, core, busy)
		VALUES ($1, now() - INTERVAL '10 minutes', 0, 5.0),
		       ($1, now() - INTERVAL '10 minutes', 1, 6.0),
		       ($1, now() - INTERVAL '5 minutes',  0, 7.0),
		       ($1, now() - INTERVAL '5 minutes',  1, 8.0)`, id)

	res, err := svc.Metrics(ctx, read.MetricsQuery{
		HostID: id, Family: "cpu_core", From: now.Add(-time.Hour), To: now,
	}, now)
	if err != nil {
		t.Fatalf("Metrics: %v", err)
	}

	if !slices.Equal(res.KeyColumns, []string{"core"}) {
		t.Errorf("key_columns = %v, want [core]", res.KeyColumns)
	}
	if len(res.Series) != 2 {
		t.Fatalf("got %d series, want one per core", len(res.Series))
	}
	for _, s := range res.Series {
		if len(s.Points) != 2 {
			t.Errorf("series %v has %d points, want 2", s.Key, len(s.Points))
		}
	}
	if res.Series[0].Key["core"] != "0" || res.Series[1].Key["core"] != "1" {
		t.Errorf("keys = %v, %v, want core 0 then core 1", res.Series[0].Key, res.Series[1].Key)
	}
}

// A family behind a surrogate id joins its dimension, so the series is keyed
// on the thing a person recognises. That is the point of the id: a rename
// touches one dimension row and forks no history (spec 5.1 rule 2).
func TestIntegrationMetricsJoinsTheDimensionForItsKey(t *testing.T) {
	ctx := context.Background()
	svc, pool := newService(t)
	now := time.Now()
	id := seedHost(t, pool, "sensored")

	var sensorID int32
	if err := pool.QueryRow(ctx, `
		INSERT INTO sensors (host_id, chip, label) VALUES ($1, 'coretemp', 'Package id 0')
		RETURNING id`, id).Scan(&sensorID); err != nil {
		t.Fatalf("insert sensor: %v", err)
	}
	exec(t, pool, `
		INSERT INTO sensor_samples (host_id, ts, sensor_id, temp)
		VALUES ($1, now() - INTERVAL '5 minutes', $2, 48.5)`, id, sensorID)

	res, err := svc.Metrics(ctx, read.MetricsQuery{
		HostID: id, Family: "sensor", From: now.Add(-time.Hour), To: now,
	}, now)
	if err != nil {
		t.Fatalf("Metrics: %v", err)
	}

	if !slices.Equal(res.KeyColumns, []string{"chip", "label"}) {
		t.Fatalf("key_columns = %v, want [chip label]", res.KeyColumns)
	}
	if len(res.Series) != 1 {
		t.Fatalf("got %d series, want 1", len(res.Series))
	}
	key := res.Series[0].Key
	if key["chip"] != "coretemp" || key["label"] != "Package id 0" {
		t.Errorf("key = %v, want the joined chip and label rather than a surrogate id", key)
	}
	// The surrogate id identifies the series; it never appears as a value.
	if slices.Contains(res.Columns, "sensor_id") {
		t.Errorf("columns = %v, want sensor_id excluded -- it identifies the series", res.Columns)
	}
}

// used and free do not sum to total: the gap is the root reserve, which holds
// no data and is not allocatable either. The read API exposes all three and
// computes no percentage, at any tier. A consumer's fullness is
// used / (used + free), as df's Use%.
func TestIntegrationMetricsFilesystemExposesTheThreeQuantitiesAndNoPercentage(t *testing.T) {
	ctx := context.Background()
	svc, pool := newService(t)
	now := time.Now()
	id := seedHost(t, pool, "disks")

	var fsID int32
	if err := pool.QueryRow(ctx, `
		INSERT INTO filesystems (host_id, label, mountpoint) VALUES ($1, 'root', '/')
		RETURNING id`, id).Scan(&fsID); err != nil {
		t.Fatalf("insert filesystem: %v", err)
	}
	// Deliberately a set where used + free < total, as every default ext4
	// filesystem reports.
	exec(t, pool, `
		INSERT INTO filesystem_samples (host_id, ts, fs_id, total, used, free)
		VALUES ($1, now() - INTERVAL '5 minutes', $2, 1000, 800, 150)`, id, fsID)

	for _, tc := range []struct {
		name string
		step time.Duration
		want []string
	}{
		{"raw", time.Minute, []string{"total", "used", "free"}},
		{"5m", 5 * time.Minute, []string{"total", "used_avg", "used_max", "free_min"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.step > time.Minute {
				refresh(t, pool, "filesystem_samples_5m")
			}
			res, err := svc.Metrics(ctx, read.MetricsQuery{
				HostID: id, Family: "filesystem", From: now.Add(-time.Hour), To: now,
				Step: tc.step, StepSet: true,
			}, now)
			if err != nil {
				t.Fatalf("Metrics: %v", err)
			}

			for _, want := range tc.want {
				if !slices.Contains(res.Columns, want) {
					t.Errorf("columns = %v, want %q", res.Columns, want)
				}
			}
			for _, c := range res.Columns {
				if c == "used_pct" || c == "fullness" || c == "use_pct" {
					t.Errorf("columns include %q; used and free do not sum to total, so any "+
						"percentage computed here would be wrong at every resolution", c)
				}
			}
		})
	}
}

func TestIntegrationMetricsRejectsBadRequests(t *testing.T) {
	ctx := context.Background()
	svc, pool := newService(t)
	now := time.Now()
	id := seedHost(t, pool, "picky")

	t.Run("an unknown family", func(t *testing.T) {
		_, err := svc.Metrics(ctx, read.MetricsQuery{HostID: id, Family: "cpu"}, now)
		if !errors.Is(err, read.ErrInvalid) {
			t.Errorf("err = %v, want ErrInvalid", err)
		}
	})

	t.Run("an unknown host", func(t *testing.T) {
		_, err := svc.Metrics(ctx, read.MetricsQuery{HostID: 4242, Family: "host"}, now)
		if !errors.Is(err, read.ErrNotFound) {
			t.Errorf("err = %v, want ErrNotFound", err)
		}
	})

	t.Run("an unknown column", func(t *testing.T) {
		_, err := svc.Metrics(ctx, read.MetricsQuery{
			HostID: id, Family: "host", Columns: []string{"cpu_totl"}}, now)
		if !errors.Is(err, read.ErrInvalid) {
			t.Errorf("err = %v, want ErrInvalid -- silently dropping it would answer with "+
				"columns the caller did not ask for", err)
		}
	})

	t.Run("an inverted window", func(t *testing.T) {
		_, err := svc.Metrics(ctx, read.MetricsQuery{
			HostID: id, Family: "host", From: now, To: now.Add(-time.Hour)}, now)
		if !errors.Is(err, read.ErrInvalid) {
			t.Errorf("err = %v, want ErrInvalid", err)
		}
	})
}

// A window the clamps closed entirely is a valid answer, not an error:
// nothing about the request was wrong, and the warning already explains it.
func TestIntegrationMetricsAnswersAnEmptyWindowWithNoSeries(t *testing.T) {
	ctx := context.Background()
	svc, pool := newService(t)
	now := time.Now()
	id := seedHost(t, pool, "too-fresh")

	res, err := svc.Metrics(ctx, read.MetricsQuery{
		HostID: id, Family: "host", From: now.Add(-5 * time.Minute), To: now,
		Step: time.Hour, StepSet: true,
	}, now)
	if err != nil {
		t.Fatalf("Metrics: %v", err)
	}

	if res.Series == nil {
		t.Fatal("series = nil, want an empty slice so it renders as [] rather than null")
	}
	if len(res.Series) != 0 {
		t.Errorf("series = %+v, want none", res.Series)
	}
	if len(res.Warnings) == 0 {
		t.Error("warnings = [], want one explaining why the window closed")
	}
}

// The cap is reported, never applied silently: a chart drawn from a quietly
// truncated series is wrong in the same way a chart drawn from the wrong tier
// is.
func TestIntegrationMetricsReportsTruncation(t *testing.T) {
	ctx := context.Background()
	svc, pool := newService(t)
	now := time.Now()
	id := seedHost(t, pool, "verbose")

	defer read.SetMaxPointsForTest(3)()

	exec(t, pool, `
		INSERT INTO host_samples (host_id, ts, cpu_total)
		SELECT $1, now() - (n || ' minutes')::INTERVAL, n
		  FROM generate_series(1, 10) AS n`, id)

	res, err := svc.Metrics(ctx, read.MetricsQuery{
		HostID: id, Family: "host", From: now.Add(-time.Hour), To: now,
		Columns: []string{"cpu_total"},
	}, now)
	if err != nil {
		t.Fatalf("Metrics: %v", err)
	}

	if !res.Truncated {
		t.Fatal("truncated = false, want true")
	}
	if got := len(res.Series[0].Points); got != 3 {
		t.Errorf("got %d points, want exactly the 3-point cap", got)
	}
	if len(res.Warnings) == 0 {
		t.Error("warnings = [], want one naming the truncation")
	}
}

// A metric the agent did not collect must come back as null, never as a zero.
// It is the single most repeated correctness rule in the spec (5.1 rule 3),
// and the read API is the last place it can be undone.
func TestIntegrationMetricsPreservesNullAsNullRatherThanZero(t *testing.T) {
	ctx := context.Background()
	svc, pool := newService(t)
	now := time.Now()
	id := seedHost(t, pool, "partial")

	// cpu_total set, swap_used deliberately absent.
	exec(t, pool, `
		INSERT INTO host_samples (host_id, ts, cpu_total)
		VALUES ($1, now() - INTERVAL '5 minutes', 42.0)`, id)

	res, err := svc.Metrics(ctx, read.MetricsQuery{
		HostID: id, Family: "host", From: now.Add(-time.Hour), To: now,
		Columns: []string{"cpu_total", "swap_used"},
	}, now)
	if err != nil {
		t.Fatalf("Metrics: %v", err)
	}

	point := res.Series[0].Points[0]
	if point[1] != 42.0 {
		t.Errorf("cpu_total = %v, want 42", point[1])
	}
	if point[2] != nil {
		t.Errorf("swap_used = %v, want nil -- a metric that was not collected is not zero", point[2])
	}
}

// avg() over a bigint returns numeric, which pgx decodes into a pgtype value
// rather than a Go float -- and a pgtype.Numeric marshals to JSON as an
// object full of internals rather than as a number. The cast in columns.go is
// what keeps mem_used_avg a number on the wire.
func TestIntegrationMetricsAggregatedIntegersAreNumbers(t *testing.T) {
	ctx := context.Background()
	svc, pool := newService(t)
	now := time.Now()
	id := seedHost(t, pool, "averaged")

	seedHostSamples(t, pool, id, 40*time.Minute, 35*time.Minute)
	refresh(t, pool, "host_samples_5m")

	res, err := svc.Metrics(ctx, read.MetricsQuery{
		HostID: id, Family: "host", From: now.Add(-2 * time.Hour), To: now,
		Step: 5 * time.Minute, StepSet: true, Columns: []string{"mem_used_avg"},
	}, now)
	if err != nil {
		t.Fatalf("Metrics: %v", err)
	}
	if len(res.Series) != 1 || len(res.Series[0].Points) == 0 {
		t.Fatalf("series = %+v, want materialised points", res.Series)
	}

	value := res.Series[0].Points[0][1]
	if _, ok := value.(float64); !ok {
		t.Errorf("mem_used_avg = %#v (%T), want a float64", value, value)
	}
}
