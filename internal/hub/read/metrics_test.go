package read_test

import (
	"context"
	"errors"
	"slices"
	"strings"
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

	// kind joined the key when the collector learned to read fans, voltages
	// and power: it identifies the series rather than measuring anything, and
	// a client needs it to know a 1200 RPM fan does not share an axis with a
	// 45 degree package.
	if !slices.Equal(res.KeyColumns, []string{"chip", "label", "kind"}) {
		t.Fatalf("key_columns = %v, want [chip label kind]", res.KeyColumns)
	}
	if len(res.Series) != 1 {
		t.Fatalf("got %d series, want 1", len(res.Series))
	}
	key := res.Series[0].Key
	if key["chip"] != "coretemp" || key["label"] != "Package id 0" {
		t.Errorf("key = %v, want the joined chip and label rather than a surrogate id", key)
	}
	// The row was inserted without a kind, as an agent predating the column
	// would send it. temperature is the only kind such an agent could mean.
	if key["kind"] != "temperature" {
		t.Errorf("kind = %q, want temperature to be the default", key["kind"])
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
		{"5m", 5 * time.Minute, []string{"total_max", "used_avg", "used_max", "free_min"}},
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
			// The raw name must not reappear at an aggregate tier: max(total)
			// over a bucket is not the instantaneous capacity, and a client
			// reading "total" would take it for one.
			if tc.name != "raw" && slices.Contains(res.Columns, "total") {
				t.Errorf("columns = %v; the 5m tier must spell its bucket maximum total_max", res.Columns)
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

// The 1h tier, with data in it.
//
// Every other aggregate test here stops at 5m, which would leave the coarsest
// tier exercised for its envelope alone -- and the 1h relation is not simply
// the 5m one at a wider bucket. It has its own column set (host_samples_1h
// carries tcp_active_opens_per_s_avg with no _max sibling, which the 5m view
// does have), its own discovery pass, and it is materialised FROM the 5m view
// rather than from the raw table, so both refreshes are needed and the order
// between them matters.
//
// This is the tier a 90-day chart draws from, so "silently empty" here is a
// failure the UI would find rather than the suite.
func TestIntegrationMetricsHourlyTierReturnsBuckets(t *testing.T) {
	ctx := context.Background()
	svc, pool := newService(t)
	now := time.Now()
	id := seedHost(t, pool, "long-history")

	// Forty days back, so range selection reaches past the 5m tier for 1h.
	// The rows survive because OpenTest unschedules the retention policies.
	seedHostSamples(t, pool, id, 40*24*time.Hour, 40*24*time.Hour+time.Minute)

	// Order matters: the 1h view aggregates the 5m view, so refreshing 1h
	// first would materialise it from an empty source and leave it empty.
	refresh(t, pool, "host_samples_5m")
	refresh(t, pool, "host_samples_1h")

	res, err := svc.Metrics(ctx, read.MetricsQuery{
		HostID: id, Family: "host", From: now.Add(-60 * 24 * time.Hour), To: now,
	}, now)
	if err != nil {
		t.Fatalf("Metrics: %v", err)
	}

	if res.Tier != read.Tier1h || res.StepS != 3600 {
		t.Fatalf("tier = %s/%ds, want 1h/3600s", res.Tier, res.StepS)
	}
	if want := now.Add(-time.Hour); res.Window.To.After(want.Add(time.Second)) {
		t.Errorf("window.to = %v, want it clamped to the 1h materialisation horizon %v",
			res.Window.To, want)
	}
	if len(res.Series) != 1 {
		t.Fatalf("got %d series, want 1 -- the 1h relation returned nothing, which is exactly "+
			"what a 90-day chart would show", len(res.Series))
	}
	if len(res.Series[0].Points) == 0 {
		t.Fatal("the 1h series has no points")
	}

	// The column set is the 1h view's own, not the 5m view's.
	if !slices.Contains(res.Columns, "cpu_total_avg") {
		t.Errorf("columns = %v, want cpu_total_avg", res.Columns)
	}
	if slices.Contains(res.Columns, "tcp_active_opens_per_s_max") {
		t.Errorf("columns include tcp_active_opens_per_s_max, which host_samples_1h does not " +
			"define -- the columns are being read off the wrong relation")
	}
}

// A dimensional family at the 1h tier: the dimension join has to work against
// the aggregate's id column too, not only the raw table's.
func TestIntegrationMetricsHourlyTierJoinsItsDimension(t *testing.T) {
	ctx := context.Background()
	svc, pool := newService(t)
	now := time.Now()
	id := seedHost(t, pool, "long-cores")

	exec(t, pool, `
		INSERT INTO cpu_core_samples (host_id, ts, core, busy)
		VALUES ($1, now() - INTERVAL '40 days', 0, 11.0),
		       ($1, now() - INTERVAL '40 days' + INTERVAL '1 minute', 0, 13.0)`, id)
	refresh(t, pool, "cpu_core_samples_5m")
	refresh(t, pool, "cpu_core_samples_1h")

	res, err := svc.Metrics(ctx, read.MetricsQuery{
		HostID: id, Family: "cpu_core", From: now.Add(-60 * 24 * time.Hour), To: now,
	}, now)
	if err != nil {
		t.Fatalf("Metrics: %v", err)
	}

	if res.Tier != read.Tier1h {
		t.Fatalf("tier = %q, want 1h", res.Tier)
	}
	if len(res.Series) != 1 || len(res.Series[0].Points) == 0 {
		t.Fatalf("series = %+v, want one core with points", res.Series)
	}
	if res.Series[0].Key["core"] != "0" {
		t.Errorf("key = %v, want core 0", res.Series[0].Key)
	}
	if res.Series[0].Points[0][1] == nil {
		t.Error("busy_avg = nil, want the bucketed average")
	}
}

// An explicit columns filter is resolved AGAINST the tier the request lands
// on, and a BASE name is the way a caller asks without knowing which tier
// that will be.
//
// This used to be an outright 400: ?columns=cpu_total against a range that
// selects 5m named a column that tier does not have. The names still differ
// per tier by construction -- that is what stops a client confusing an
// average with a peak -- but requiring the caller to predict the tier made
// ?columns= unusable for exactly the caller who needs it most, a page drawing
// one chart at every range. So an unsuffixed name is now a base and expands
// to whichever aggregates the answering tier carries, while a name that
// already picks an aggregate is still matched exactly.
func TestIntegrationMetricsColumnFilterResolvesBaseNamesPerTier(t *testing.T) {
	ctx := context.Background()
	svc, pool := newService(t)
	now := time.Now()
	id := seedHost(t, pool, "filtered")

	// A base name at the 5m tier: both aggregates of it, and only of it.
	res, err := svc.Metrics(ctx, read.MetricsQuery{
		HostID: id, Family: "host", From: now.Add(-30 * 24 * time.Hour), To: now,
		Columns: []string{"cpu_total"},
	}, now)
	if err != nil {
		t.Fatalf("Metrics with a base column name: %v", err)
	}
	if res.Tier != read.Tier5m {
		t.Fatalf("tier = %q, want 5m -- the rest of this test is about that tier", res.Tier)
	}
	if !slices.Equal(res.Columns, []string{"cpu_total_avg", "cpu_total_max"}) {
		t.Errorf("columns = %v, want [cpu_total_avg cpu_total_max]", res.Columns)
	}

	// The SAME base name at the raw tier is the raw column, unsuffixed. One
	// request shape, every range -- which is the whole point.
	res, err = svc.Metrics(ctx, read.MetricsQuery{
		HostID: id, Family: "host", From: now.Add(-time.Hour), To: now,
		Columns: []string{"cpu_total"},
	}, now)
	if err != nil {
		t.Fatalf("Metrics with a base column name at raw: %v", err)
	}
	if !slices.Equal(res.Columns, []string{"cpu_total"}) {
		t.Errorf("columns = %v, want [cpu_total]", res.Columns)
	}

	// Naming one aggregate still pins it, and still gets only that one.
	res, err = svc.Metrics(ctx, read.MetricsQuery{
		HostID: id, Family: "host", From: now.Add(-30 * 24 * time.Hour), To: now,
		Columns: []string{"cpu_total_avg"},
	}, now)
	if err != nil {
		t.Fatalf("Metrics with the tier's own column name: %v", err)
	}
	if !slices.Equal(res.Columns, []string{"cpu_total_avg"}) {
		t.Errorf("columns = %v, want [cpu_total_avg]", res.Columns)
	}

	// And an aggregate the tier does NOT have is still a 400 naming what it
	// does have. A suffixed name is a claim about the tier, so silently
	// answering it with the base would hand back an average labelled a peak.
	_, err = svc.Metrics(ctx, read.MetricsQuery{
		HostID: id, Family: "host", From: now.Add(-time.Hour), To: now,
		Columns: []string{"cpu_total_avg"},
	}, now)
	if !errors.Is(err, read.ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid -- cpu_total_avg is a 5m column and this range selects raw", err)
	}
	if !strings.Contains(err.Error(), "cpu_total") {
		t.Errorf("err = %q, want it to name the columns the chosen tier does have", err)
	}
}

// A base the family measures SOMEWHERE but not at the answering tier is
// dropped and WARNED about -- never a 400, and never in silence.
//
// mem_available is the case that forces it: host_samples carries it and
// neither aggregate rolls it up, so a client naming the quantity once for
// every range is right and the 5m tier simply has nothing to give it. A 400
// there would make one unrollable column break a request for nine good ones;
// dropping it quietly would draw an empty chart with no way to find out why.
//
// The other half -- a base NO tier of the family has ever carried -- is still
// the 400 a typo deserves, and TestIntegrationMetricsRejectsBadRequests pins
// it.
func TestIntegrationMetricsDropsAndWarnsForAColumnTheTierLacks(t *testing.T) {
	ctx := context.Background()
	svc, pool := newService(t)
	now := time.Now()
	id := seedHost(t, pool, "partial")

	res, err := svc.Metrics(ctx, read.MetricsQuery{
		HostID: id, Family: "host", From: now.Add(-24 * time.Hour), To: now,
		Step: 5 * time.Minute, StepSet: true,
		Columns: []string{"cpu_total", "mem_available"},
	}, now)
	if err != nil {
		t.Fatalf("Metrics: %v", err)
	}
	if res.Tier != read.Tier5m {
		t.Fatalf("tier = %q, want 5m", res.Tier)
	}
	if !slices.Equal(res.Columns, []string{"cpu_total_avg", "cpu_total_max"}) {
		t.Errorf("columns = %v, want the cpu_total pair and nothing for mem_available", res.Columns)
	}

	var warned bool
	for _, w := range res.Warnings {
		if strings.Contains(w, "mem_available") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("warnings = %v, want one naming mem_available -- a dropped column "+
			"the caller cannot see is a chart that is empty for no stated reason", res.Warnings)
	}
}

// The container page at 7d and 30d, which is the only range that reaches the
// 1h tier.
//
// container_samples_5m rolls up the six columns that split a container's CPU
// and memory -- cpu_user, cpu_system, mem_anon, mem_file, mem_shmem,
// mem_kernel -- and container_samples_1h did not. A container page at 6h and
// 24h therefore drew the full breakdown and the SAME page at 7d silently
// collapsed to the single cpu_pct/mem_used line, with nothing on screen
// saying why. The host charts do not degrade that way, because
// host_samples_1h was updated when the host samples were split; this is the
// tier that was missed.
//
// avg only, never max: these are parts of a whole, and a chart stacking a max
// of one against an avg of another composes two different instants into one
// bar.
func TestIntegrationMetricsHourlyContainerTierCarriesTheBreakdown(t *testing.T) {
	ctx := context.Background()
	svc, pool := newService(t)
	now := time.Now()
	id := seedHost(t, pool, "long-containers")

	exec(t, pool, `
		INSERT INTO containers (host_id, container_key, name)
		VALUES ($1, 'proj/web', 'web-1')`, id)
	// Forty days back, so range selection reaches past the 5m tier for 1h.
	exec(t, pool, `
		INSERT INTO container_samples
		       (host_id, container_id, ts, cpu_pct, cpu_user, cpu_system,
		        mem_used, mem_anon, mem_file, mem_shmem, mem_kernel)
		SELECT $1, d.id, now() - INTERVAL '40 days', 30.0, 20.0, 10.0,
		       1000, 600, 200, 100, 100
		  FROM containers d WHERE d.host_id = $1 AND d.container_key = 'proj/web'`, id)
	exec(t, pool, `
		INSERT INTO container_samples
		       (host_id, container_id, ts, cpu_pct, cpu_user, cpu_system,
		        mem_used, mem_anon, mem_file, mem_shmem, mem_kernel)
		SELECT $1, d.id, now() - INTERVAL '40 days' + INTERVAL '1 minute',
		       50.0, 30.0, 20.0, 2000, 1200, 400, 200, 200
		  FROM containers d WHERE d.host_id = $1 AND d.container_key = 'proj/web'`, id)

	// Order matters: the 1h view aggregates the 5m view, so refreshing 1h
	// first would materialise it from an empty source and leave it empty.
	refresh(t, pool, "container_samples_5m")
	refresh(t, pool, "container_samples_1h")

	res, err := svc.Metrics(ctx, read.MetricsQuery{
		HostID: id, Family: "container", From: now.Add(-60 * 24 * time.Hour), To: now,
	}, now)
	if err != nil {
		t.Fatalf("Metrics: %v", err)
	}

	if res.Tier != read.Tier1h {
		t.Fatalf("tier = %q, want 1h -- this test only proves anything at the coarsest tier", res.Tier)
	}
	if len(res.Series) != 1 || len(res.Series[0].Points) == 0 {
		t.Fatalf("series = %+v, want one container with points", res.Series)
	}

	for _, want := range []string{
		"cpu_user_avg", "cpu_system_avg",
		"mem_anon_avg", "mem_file_avg", "mem_shmem_avg", "mem_kernel_avg",
	} {
		if !slices.Contains(res.Columns, want) {
			t.Errorf("columns = %v, want %s -- a container page at 7d collapses to the "+
				"single line without it", res.Columns, want)
		}
	}

	// avg only. A _max sibling on a column a chart stacks as part of a whole
	// is the bug the 5m tier's own comment exists to prevent.
	for _, unwanted := range []string{
		"cpu_user_max", "cpu_system_max",
		"mem_anon_max", "mem_file_max", "mem_shmem_max", "mem_kernel_max",
	} {
		if slices.Contains(res.Columns, unwanted) {
			t.Errorf("columns include %s; these are parts of a whole and must be avg only", unwanted)
		}
	}

	// The values are really bucketed, not merely declared: the two samples
	// average to 25 % system time.
	idx := slices.Index(res.Columns, "cpu_system_avg")
	// Points are [ts, col0, col1, ...], so the column's offset is idx + 1.
	value := res.Series[0].Points[0][idx+1]
	if value == nil {
		t.Fatal("cpu_system_avg = nil, want the bucketed average")
	}
	if got, ok := value.(float64); !ok || got != 15 {
		t.Errorf("cpu_system_avg = %#v, want 15 (the mean of 10 and 20)", value)
	}
}

// --- the fleet form ---
//
// FleetMetrics answers several hosts from one query. The fleet page used to
// ask N hosts the identical question N times -- four families per host, five
// where the host was small enough for a per-core stack, plus a container call
// each, re-sent on every sixty-second poll and every range toggle -- through a
// browser that opens six connections.
//
// The tests below pin the three things that make collapsing that safe: one
// header describes every host, the grouping survives hosts that share a key
// value, and a host that reported nothing is still in the answer.

// seedNetSamples writes one net_samples row per offset, for one interface.
func seedNetSamples(t *testing.T, pool *pgxpool.Pool, hostID int32, iface string, offsets ...time.Duration) {
	t.Helper()

	for i, off := range offsets {
		exec(t, pool, `
			INSERT INTO net_samples (host_id, ts, iface, rx_bytes, tx_bytes)
			VALUES ($1, now() - $2::interval, $3, $4, $5)`,
			hostID, off.String(), iface, float64(100+i), float64(200+i))
	}
}

func TestIntegrationFleetMetricsAnswersEveryHostFromOneQuery(t *testing.T) {
	ctx := context.Background()
	svc, pool := newService(t)
	now := time.Now()

	a := seedHost(t, pool, "fleet-a")
	b := seedHost(t, pool, "fleet-b")
	// Registered, and silent. It must appear in the answer with no series: a
	// caller has to be able to tell a quiet host from one it never asked
	// about, which is why Hosts is seeded from the request rather than from
	// the rows that came back.
	quiet := seedHost(t, pool, "fleet-quiet")

	seedHostSamples(t, pool, a, 30*time.Minute, 20*time.Minute)
	seedHostSamples(t, pool, b, 30*time.Minute, 20*time.Minute, 10*time.Minute)

	res, err := svc.FleetMetrics(ctx, read.FleetMetricsQuery{
		HostIDs: []int32{a, b, quiet},
		Family:  "host",
		From:    now.Add(-2 * time.Hour),
		To:      now,
		Columns: []string{"cpu_total"},
	}, now)
	if err != nil {
		t.Fatalf("FleetMetrics: %v", err)
	}

	if res.Tier != read.TierRaw {
		t.Errorf("tier = %q, want raw", res.Tier)
	}
	if !slices.Equal(res.Columns, []string{"cpu_total"}) {
		t.Errorf("columns = %v, want [cpu_total]", res.Columns)
	}
	if len(res.Hosts) != 3 {
		t.Fatalf("hosts = %d, want 3 -- every requested host, answered or not", len(res.Hosts))
	}

	byID := map[int32][]read.Series{}
	for _, h := range res.Hosts {
		byID[h.HostID] = h.Series
	}
	if got := len(byID[a][0].Points); got != 2 {
		t.Errorf("host a points = %d, want 2", got)
	}
	if got := len(byID[b][0].Points); got != 3 {
		t.Errorf("host b points = %d, want 3", got)
	}
	if series, ok := byID[quiet]; !ok || len(series) != 0 {
		t.Errorf("quiet host = %v (present %v), want an empty series list", series, ok)
	}
}

// The grouping break is on host AND key, never on the key alone. Two hosts
// with an interface of the same name are what catches a break that watched
// only the key: eth0's rows would run together across the host boundary and
// one host would be handed the other's traffic.
func TestIntegrationFleetMetricsKeepsHostsApartWhenTheyShareAKey(t *testing.T) {
	ctx := context.Background()
	svc, pool := newService(t)
	now := time.Now()

	a := seedHost(t, pool, "iface-a")
	b := seedHost(t, pool, "iface-b")

	seedNetSamples(t, pool, a, "eth0", 30*time.Minute, 20*time.Minute)
	seedNetSamples(t, pool, b, "eth0", 30*time.Minute)

	res, err := svc.FleetMetrics(ctx, read.FleetMetricsQuery{
		HostIDs: []int32{a, b},
		Family:  "net",
		From:    now.Add(-2 * time.Hour),
		To:      now,
		Columns: []string{"rx_bytes"},
	}, now)
	if err != nil {
		t.Fatalf("FleetMetrics: %v", err)
	}

	points := map[int32]int{}
	for _, h := range res.Hosts {
		if len(h.Series) != 1 {
			t.Fatalf("host %d series = %d, want 1", h.HostID, len(h.Series))
		}
		if h.Series[0].Key["iface"] != "eth0" {
			t.Errorf("host %d key = %v, want eth0", h.HostID, h.Series[0].Key)
		}
		points[h.HostID] = len(h.Series[0].Points)
	}
	if points[a] != 2 || points[b] != 1 {
		t.Errorf("points = %v, want host a with 2 and host b with 1 -- a break that "+
			"watched only the key would run eth0 together across the host boundary", points)
	}
}

// The point cap is applied PER HOST, and this is the test that says so.
//
// A single LIMIT over the whole result would look like a bound and behave
// like a silent regression: the rows arrive in host_id order, so the first
// hosts would spend the budget and every host after the cut would come back
// empty -- deterministically the same hosts, on every poll, with nothing on
// screen to say why. The per-host route this replaced gave each host its own
// cap by construction, because each host was its own request.
//
// Both hosts below are seeded past the cap. Both must come back capped, and
// neither may be starved by the other.
func TestIntegrationFleetMetricsCapsEachHostSeparately(t *testing.T) {
	ctx := context.Background()
	svc, pool := newService(t)
	now := time.Now()

	a := seedHost(t, pool, "cap-a")
	b := seedHost(t, pool, "cap-b")
	seedHostSamples(t, pool, a, 40*time.Minute, 30*time.Minute, 20*time.Minute)
	seedHostSamples(t, pool, b, 40*time.Minute, 30*time.Minute, 20*time.Minute)

	restore := read.SetMaxPointsForTest(2)
	defer restore()

	res, err := svc.FleetMetrics(ctx, read.FleetMetricsQuery{
		HostIDs: []int32{a, b},
		Family:  "host",
		From:    now.Add(-2 * time.Hour),
		To:      now,
		Columns: []string{"cpu_total"},
	}, now)
	if err != nil {
		t.Fatalf("FleetMetrics: %v", err)
	}
	if !res.Truncated {
		t.Fatal("truncated = false, want true -- three points per host against a cap of two")
	}

	perHost := map[int32]int{}
	for _, h := range res.Hosts {
		for _, s := range h.Series {
			perHost[h.HostID] += len(s.Points)
		}
	}
	if perHost[a] != 2 || perHost[b] != 2 {
		t.Errorf("points = %v, want 2 for each host -- a shared budget would have "+
			"given the first host 2 and the second none", perHost)
	}
}

// A family whose key comes from a JOINED dimension table, with two key
// columns rather than one.
//
// The fleet query wraps its select in a subquery so it can rank rows per host,
// which means every key expression has to be carried out through an alias --
// d.label and d.mountpoint are not columns of the sample relation, and the
// window's ORDER BY cannot name the aliases either. filesystem exercises both
// halves of that at once, and net (one key, no join) would not.
func TestIntegrationFleetMetricsCarriesJoinedDimensionKeys(t *testing.T) {
	ctx := context.Background()
	svc, pool := newService(t)
	now := time.Now()

	a := seedHost(t, pool, "disks-a")
	b := seedHost(t, pool, "disks-b")

	seedFS := func(hostID int32, label, mount string) {
		t.Helper()
		var fsID int32
		if err := pool.QueryRow(ctx, `
			INSERT INTO filesystems (host_id, label, mountpoint) VALUES ($1, $2, $3)
			RETURNING id`, hostID, label, mount).Scan(&fsID); err != nil {
			t.Fatalf("insert filesystem: %v", err)
		}
		exec(t, pool, `
			INSERT INTO filesystem_samples (host_id, ts, fs_id, total, used, free)
			VALUES ($1, now() - INTERVAL '5 minutes', $2, 1000, 800, 150)`, hostID, fsID)
	}
	// The same mountpoint on both hosts, so a grouping break that forgot the
	// host would run them together.
	seedFS(a, "root", "/")
	seedFS(a, "logs", "/var/log")
	seedFS(b, "root", "/")

	res, err := svc.FleetMetrics(ctx, read.FleetMetricsQuery{
		HostIDs: []int32{a, b},
		Family:  "filesystem",
		From:    now.Add(-time.Hour),
		To:      now,
		Columns: []string{"used", "free"},
	}, now)
	if err != nil {
		t.Fatalf("FleetMetrics: %v", err)
	}

	mounts := map[int32][]string{}
	for _, h := range res.Hosts {
		for _, s := range h.Series {
			if s.Key["mountpoint"] == "" {
				t.Errorf("host %d series has no mountpoint: %v -- the joined key "+
					"did not survive the subquery", h.HostID, s.Key)
			}
			mounts[h.HostID] = append(mounts[h.HostID], s.Key["mountpoint"])
		}
	}
	slices.Sort(mounts[a])
	if !slices.Equal(mounts[a], []string{"/", "/var/log"}) {
		t.Errorf("host a mounts = %v, want [/ /var/log]", mounts[a])
	}
	if !slices.Equal(mounts[b], []string{"/"}) {
		t.Errorf("host b mounts = %v, want [/]", mounts[b])
	}
}

func TestIntegrationFleetMetricsRejectsBadRequests(t *testing.T) {
	ctx := context.Background()
	svc, pool := newService(t)
	now := time.Now()
	id := seedHost(t, pool, "fleet-picky")

	t.Run("no hosts at all", func(t *testing.T) {
		_, err := svc.FleetMetrics(ctx, read.FleetMetricsQuery{
			HostIDs: nil, Family: "host"}, now)
		if !errors.Is(err, read.ErrInvalid) {
			t.Errorf("err = %v, want ErrInvalid -- an empty list must not come to mean "+
				"every host, which is the most expensive query the hub can run", err)
		}
	})

	t.Run("an unknown family", func(t *testing.T) {
		_, err := svc.FleetMetrics(ctx, read.FleetMetricsQuery{
			HostIDs: []int32{id}, Family: "cpu"}, now)
		if !errors.Is(err, read.ErrInvalid) {
			t.Errorf("err = %v, want ErrInvalid", err)
		}
	})

	// An id nobody registered is NOT an error here, unlike the per-host route.
	// It contributes no rows and comes back empty, which is exactly what a
	// registered but silent host looks like -- and the fleet page only ever
	// names hosts /api/v1/hosts just handed it.
	t.Run("an unknown host", func(t *testing.T) {
		res, err := svc.FleetMetrics(ctx, read.FleetMetricsQuery{
			HostIDs: []int32{4242}, Family: "host",
			From: now.Add(-time.Hour), To: now,
		}, now)
		if err != nil {
			t.Fatalf("FleetMetrics: %v", err)
		}
		if len(res.Hosts) != 1 || len(res.Hosts[0].Series) != 0 {
			t.Errorf("hosts = %+v, want one entry with no series", res.Hosts)
		}
	})
}
