package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/trick77/netra/internal/hub/conditions"
	"github.com/trick77/netra/internal/hub/store"
)

// seedSensorLimits inserts one sensor and returns its id. Limits are optional:
// pass nil for a chip that publishes none.
func seedSensorLimits(t *testing.T, ctx context.Context, s *store.Store,
	host int32, chip, label, instance string, high, crit *float64) int32 {
	t.Helper()
	var id int32
	if err := s.Pool().QueryRow(ctx, `
		INSERT INTO sensors (host_id, chip, label, kind, instance, limit_high, limit_high_crit)
		VALUES ($1, $2, $3, 'temperature', $4, $5, $6) RETURNING id`,
		host, chip, label, instance, high, crit).Scan(&id); err != nil {
		t.Fatalf("insert sensor: %v", err)
	}
	return id
}

// seedSensorSeries writes n samples one scrape apart ENDING at `end`, all at the
// same temperature.
//
// CopyFrom rather than n inserts: these tests seed thousands of samples to build
// a span, and one statement at a time turns a five-second test into a
// ninety-second one.
func seedSensorSeries(t *testing.T, ctx context.Context, s *store.Store,
	host, sensorID int32, end time.Time, n int, temp float64) {
	t.Helper()
	rows := make([][]any, 0, n)
	for i := range n {
		ts := end.Add(-time.Duration(n-1-i) * conditions.ScrapeInterval)
		rows = append(rows, []any{host, ts, sensorID, temp, temp})
	}
	if _, err := s.Pool().CopyFrom(ctx,
		[]string{"sensor_samples"},
		[]string{"host_id", "ts", "sensor_id", "temp", "value"},
		pgx.CopyFromRows(rows)); err != nil {
		t.Fatalf("sensor_samples: %v", err)
	}
}

func seedHostSeries(t *testing.T, ctx context.Context, s *store.Store,
	host int32, end time.Time, n int, procs int32, load float64) {
	t.Helper()
	rows := make([][]any, 0, n)
	for i := range n {
		ts := end.Add(-time.Duration(n-1-i) * conditions.ScrapeInterval)
		rows = append(rows, []any{host, ts, procs, load})
	}
	if _, err := s.Pool().CopyFrom(ctx,
		[]string{"host_samples"},
		[]string{"host_id", "ts", "processes_total", "load5"},
		pgx.CopyFromRows(rows)); err != nil {
		t.Fatalf("host_samples: %v", err)
	}
}

// ewmaRow reads one subject's stored state.
func ewmaRow(t *testing.T, ctx context.Context, s *store.Store,
	host int32, kind, subject string) (slow, fast float64, span time.Duration, ok bool) {
	t.Helper()
	var first, updated time.Time
	err := s.Pool().QueryRow(ctx, `
		SELECT coalesce(max(eh.slow), 0), e.fast, e.first_ts, e.updated_ts
		  FROM metric_ewma e
		  LEFT JOIN metric_ewma_hour eh
		         ON eh.host_id = e.host_id AND eh.kind = e.kind AND eh.subject = e.subject
		 WHERE e.host_id = $1 AND e.kind = $2 AND e.subject = $3
		 GROUP BY e.fast, e.first_ts, e.updated_ts`,
		host, kind, subject).Scan(&slow, &fast, &first, &updated)
	if err != nil {
		return 0, 0, 0, false
	}
	return slow, fast, updated.Sub(first), true
}

// bucketCount is how many hour buckets a subject has learned.
func bucketCount(t *testing.T, ctx context.Context, s *store.Store,
	host int32, kind, subject string) int {
	t.Helper()
	var n int
	if err := s.Pool().QueryRow(ctx, `
		SELECT count(*) FROM metric_ewma_hour
		 WHERE host_id = $1 AND kind = $2 AND subject = $3`,
		host, kind, subject).Scan(&n); err != nil {
		t.Fatalf("count buckets: %v", err)
	}
	return n
}

// enoughSpan is comfortably past the temperature warm-up.
const enoughSpan = 2000

// The fold runs, the state lands, and a flat series produces a normal equal to
// it. This is also the migration's smoke test: the table, the dropped procedure
// and the retired job all have to be right for it to get this far.
func TestIntegrationFoldBuildsTheMovingAverage(t *testing.T) {
	ctx, s := condCtx(t)
	host := newHost(t, ctx, s, "ewma-fold")
	now := time.Now().UTC().Truncate(time.Minute)

	sensorID := seedSensorLimits(t, ctx, s, host, "drivetemp", "temp1", "sda", nil, nil)
	seedSensorSeries(t, ctx, s, host, sensorID, now, enoughSpan, 44)

	if err := s.FoldSamples(ctx); err != nil {
		t.Fatalf("FoldSamples: %v", err)
	}

	slow, fast, span, ok := ewmaRow(t, ctx, s, host, conditions.KindTemperature, "drivetemp/temp1/sda")
	if !ok {
		t.Fatal("no metric_ewma row")
	}
	if slow < 43.9 || slow > 44.1 {
		t.Errorf("slow = %v, want 44 for a flat series", slow)
	}
	if fast < 43.9 || fast > 44.1 {
		t.Errorf("fast = %v, want 44 for a flat series", fast)
	}
	if span < 24*time.Hour {
		t.Errorf("span = %v, want at least a day from %d samples", span, enoughSpan)
	}

	// The seasonal half landed too, and only for the hours the series actually
	// covered: enoughSpan minutes is about 33 h, so every hour is touched, but
	// a shorter series must not conjure buckets it never observed.
	if n := bucketCount(t, ctx, s, host, conditions.KindTemperature, "drivetemp/temp1/sda"); n == 0 {
		t.Error("no metric_ewma_hour rows: the seasonal half was not written")
	}
}

// A series shorter than a day leaves the hours it never covered unlearned, and
// a reading in one of them is UNJUDGED rather than judged against nothing.
//
// The alternative -- seeding an unseen bucket from a neighbour -- would import
// the wrong mode at exactly the boundary where the modes differ, which is the
// thing the buckets exist to separate.
func TestIntegrationOnlyObservedHoursAreLearned(t *testing.T) {
	ctx, s := condCtx(t)
	host := newHost(t, ctx, s, "ewma-partial-day")
	now := time.Now().UTC().Truncate(time.Minute)

	sensorID := seedSensorLimits(t, ctx, s, host, "drivetemp", "temp1", "sda", nil, nil)
	// Three hours of history: three or four buckets, not twenty-four.
	seedSensorSeries(t, ctx, s, host, sensorID, now, 3*60, 44)

	if err := s.FoldSamples(ctx); err != nil {
		t.Fatalf("FoldSamples: %v", err)
	}

	n := bucketCount(t, ctx, s, host, conditions.KindTemperature, "drivetemp/temp1/sda")
	if n == 0 || n > 4 {
		t.Errorf("buckets = %d, want the three or four hours the series covered", n)
	}
}

// The old design was dropped, not left alongside. A migration that added the
// new table and forgot to retire the job would leave a procedure running
// nightly against a table that no longer exists.
func TestIntegrationTheBaselineWindowIsGone(t *testing.T) {
	ctx, s := condCtx(t)

	var tables int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM information_schema.tables WHERE table_name = 'metric_baselines'`).
		Scan(&tables); err != nil {
		t.Fatalf("query tables: %v", err)
	}
	if tables != 0 {
		t.Error("metric_baselines still exists")
	}

	var jobs int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM timescaledb_information.jobs
		  WHERE proc_name = 'netra_recompute_baselines'`).Scan(&jobs); err != nil {
		t.Fatalf("query jobs: %v", err)
	}
	if jobs != 0 {
		t.Errorf("%d recompute jobs still registered", jobs)
	}

	// 0022 moved the seasonal half out, so the single-average columns must be
	// gone from metric_ewma as well: leaving them would let a reader take the
	// value that sits between a subject's two modes for its normal.
	var cols int
	if err := s.Pool().QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		 WHERE table_name = 'metric_ewma' AND column_name IN ('slow', 'var')`).
		Scan(&cols); err != nil {
		t.Fatalf("query columns: %v", err)
	}
	if cols != 0 {
		t.Errorf("metric_ewma still carries %d of the un-bucketed columns", cols)
	}
}

// THE OBJECTION TO A TICK-DRIVEN UPDATE, settled against a real database.
//
// An agent buffers an hour and posts sixty samples at once. The fold reads
// forward from its own high-water mark, so those sixty move the average as
// sixty readings a minute apart. A fold that took the newest sample would
// absorb one of them, and the average would track how often the hub looked
// rather than what the host did.
func TestIntegrationABufferedReplayFoldsEverySample(t *testing.T) {
	ctx, s := condCtx(t)
	host := newHost(t, ctx, s, "ewma-replay")
	now := time.Now().UTC().Truncate(time.Minute)

	sensorID := seedSensorLimits(t, ctx, s, host, "drivetemp", "temp1", "sda", nil, nil)

	// A settled history, folded.
	seedSensorSeries(t, ctx, s, host, sensorID, now.Add(-time.Hour), enoughSpan, 44)
	if err := s.FoldSamples(ctx); err != nil {
		t.Fatalf("first fold: %v", err)
	}
	_, _, spanBefore, _ := ewmaRow(t, ctx, s, host, conditions.KindTemperature, "drivetemp/temp1/sda")

	// Then an hour of buffered samples arriving at once, all warmer.
	seedSensorSeries(t, ctx, s, host, sensorID, now, 60, 52)
	if err := s.FoldSamples(ctx); err != nil {
		t.Fatalf("second fold: %v", err)
	}

	_, fast, spanAfter, ok := ewmaRow(t, ctx, s, host, conditions.KindTemperature, "drivetemp/temp1/sda")
	if !ok {
		t.Fatal("no metric_ewma row")
	}

	// Sixty minutes is thirty TauFast, so fast has effectively saturated at the
	// new level. Absorbing one sample would have left it near 44.
	if fast < 51 {
		t.Errorf("fast = %v after an hour of buffered samples at 52: "+
			"the replay was folded as one reading, not sixty", fast)
	}
	if spanAfter <= spanBefore {
		t.Errorf("span did not advance: %v then %v", spanBefore, spanAfter)
	}
}

// A subject with no state, or too little span, is UNJUDGED rather than healthy.
// A new host must not be declared fine by a rule that has not watched it yet.
func TestIntegrationAnUnwatchedSubjectIsUnjudged(t *testing.T) {
	ctx, s := condCtx(t)
	host := newHost(t, ctx, s, "ewma-new")
	now := time.Now().UTC().Truncate(time.Minute)
	seedHostCurrent(t, ctx, s, host, now)

	sensorID := seedSensorLimits(t, ctx, s, host, "coretemp", "Package id 0", "", nil, nil)
	// Ten minutes of history: seeded, nowhere near the 24-hour warm-up.
	seedSensorSeries(t, ctx, s, host, sensorID, now, 10, 92)

	if err := s.FoldSamples(ctx); err != nil {
		t.Fatalf("FoldSamples: %v", err)
	}
	scan, err := s.ScanConditions(ctx, now, nil, hubUp)
	if err != nil {
		t.Fatalf("ScanConditions: %v", err)
	}

	key := conditions.Key{
		HostID: host, Kind: conditions.KindTemperature, Subject: "coretemp/Package id 0",
	}
	if !scan.Unjudged[key] {
		t.Error("a sensor inside its warm-up was not unjudged")
	}
	if scan.Seen[key] {
		t.Error("a sensor inside its warm-up was recorded as seen -- it would read as healthy")
	}
	if _, bad := scan.Bad[key]; bad {
		t.Error("a sensor inside its warm-up was judged bad")
	}
}

// The host kinds wait for a weekly cycle where temperature waits a day, so a
// netra installed over a weekend does not greet Monday by calling every host
// abnormal.
func TestIntegrationHostKindsWaitLongerThanTemperature(t *testing.T) {
	ctx, s := condCtx(t)
	host := newHost(t, ctx, s, "ewma-firstmonday")
	now := time.Now().UTC().Truncate(time.Minute)
	seedHostCurrent(t, ctx, s, host, now)

	// Two days: past temperature's warm-up, well inside the host kinds'.
	const twoDays = 2 * 24 * 60
	sensorID := seedSensorLimits(t, ctx, s, host, "drivetemp", "temp1", "sda", nil, nil)
	seedSensorSeries(t, ctx, s, host, sensorID, now, twoDays, 44)
	seedHostSeries(t, ctx, s, host, now, twoDays, 300, 1.5)

	if err := s.FoldSamples(ctx); err != nil {
		t.Fatalf("FoldSamples: %v", err)
	}
	scan, err := s.ScanConditions(ctx, now, nil, hubUp)
	if err != nil {
		t.Fatalf("ScanConditions: %v", err)
	}

	temp := conditions.Key{
		HostID: host, Kind: conditions.KindTemperature, Subject: "drivetemp/temp1/sda",
	}
	if !scan.Seen[temp] {
		t.Errorf("temperature was not judged after two days; unjudged=%v", scan.Unjudged[temp])
	}

	for _, kind := range []string{conditions.KindProcesses, conditions.KindLoad} {
		key := conditions.Key{HostID: host, Kind: kind}
		if !scan.Unjudged[key] {
			t.Errorf("%s was judged on two days of history", kind)
		}
		if scan.Seen[key] {
			t.Errorf("%s was recorded as seen -- it would read as healthy", kind)
		}
		// The state is nonetheless accumulating, so nothing has to start over
		// when the span is finally long enough.
		if _, _, _, ok := ewmaRow(t, ctx, s, host, kind, ""); !ok {
			t.Errorf("%s has no state row: the history is not accumulating", kind)
		}
	}
}

// The whole rule end to end: a subject that departs from its own normal for
// long enough is raised, with the numbers a reader needs in the detail.
func TestIntegrationADepartureFromNormalIsRaised(t *testing.T) {
	ctx, s := condCtx(t)
	host := newHost(t, ctx, s, "ewma-departure")
	now := time.Now().UTC().Truncate(time.Minute)
	seedHostCurrent(t, ctx, s, host, now)

	sensorID := seedSensorLimits(t, ctx, s, host, "drivetemp", "temp1", "sda", nil, nil)
	// Two days at 44, then fifteen minutes at 61 -- past OpenFor.
	seedSensorSeries(t, ctx, s, host, sensorID, now.Add(-15*time.Minute), 2*24*60, 44)
	seedSensorSeries(t, ctx, s, host, sensorID, now, 15, 61)

	if err := s.FoldSamples(ctx); err != nil {
		t.Fatalf("FoldSamples: %v", err)
	}
	scan, err := s.ScanConditions(ctx, now, nil, hubUp)
	if err != nil {
		t.Fatalf("ScanConditions: %v", err)
	}

	key := conditions.Key{
		HostID: host, Kind: conditions.KindTemperature, Subject: "drivetemp/temp1/sda",
	}
	finding, bad := scan.Bad[key]
	if !bad {
		t.Fatalf("61 C on a drive that normally runs at 44 was not raised; unjudged=%v seen=%v",
			scan.Unjudged[key], scan.Seen[key])
	}
	if finding.Detail["value"] != 61.0 {
		t.Errorf("detail value = %v, want 61", finding.Detail["value"])
	}
	if finding.Detail["source"] != conditions.SourceBaseline {
		t.Errorf("detail source = %v, want %q", finding.Detail["source"], conditions.SourceBaseline)
	}
	if normal, ok := finding.Detail["normal"].(float64); !ok || normal > 48 {
		t.Errorf("detail normal = %v, want the pre-excursion 44", finding.Detail["normal"])
	}
	if !scan.Evaluated[conditions.KindTemperature] {
		t.Error("the temperature kind was not marked evaluated")
	}

	// THE ONSET IS WHEN IT LEFT THE BAND, not when the hub looked.
	//
	// The state already knows: excursion_since was stamped by the fold at the
	// moment fast crossed. Reporting now() instead put every open at least
	// OpenFor late, and arbitrarily late behind a fold working through a
	// backlog -- an excursion stamped twenty hours ago opening as though it had
	// just started.
	if finding.OpenedTS.After(now.Add(-2 * time.Minute)) {
		t.Errorf("onset = %v against a reading at %v: the excursion began "+
			"minutes earlier and the state recorded when", finding.OpenedTS, now)
	}
	if finding.OpenedAtLeast {
		t.Error("an onset taken from the state is exact, not a floor")
	}
}

// A brief excursion is not raised, and is not filed as healthy either: a
// nightly cron burst must write nothing, and must not count as a miss against
// a condition already open on that subject.
func TestIntegrationABriefExcursionIsUnjudgedNotRaised(t *testing.T) {
	ctx, s := condCtx(t)
	host := newHost(t, ctx, s, "ewma-burst")
	now := time.Now().UTC().Truncate(time.Minute)
	seedHostCurrent(t, ctx, s, host, now)

	sensorID := seedSensorLimits(t, ctx, s, host, "drivetemp", "temp1", "sda", nil, nil)
	seedSensorSeries(t, ctx, s, host, sensorID, now.Add(-4*time.Minute), 2*24*60, 44)
	// Four minutes at 70. `fast` needs about two of them to cross the band, so
	// the EXCURSION is only about two minutes old against OpenFor's three --
	// which is the window this test is about.
	//
	// A single sample would not do: fast only reaches 54 from 44 in one minute,
	// under the 56 warn, so the subject reads healthy and the suppression is
	// never exercised at all. CI caught exactly that.
	seedSensorSeries(t, ctx, s, host, sensorID, now, 4, 70)

	if err := s.FoldSamples(ctx); err != nil {
		t.Fatalf("FoldSamples: %v", err)
	}
	scan, err := s.ScanConditions(ctx, now, nil, hubUp)
	if err != nil {
		t.Fatalf("ScanConditions: %v", err)
	}

	key := conditions.Key{
		HostID: host, Kind: conditions.KindTemperature, Subject: "drivetemp/temp1/sda",
	}
	if _, bad := scan.Bad[key]; bad {
		t.Error("a one-minute excursion was raised")
	}
	if scan.Seen[key] {
		t.Error("a brief excursion was filed as healthy, which counts as a miss " +
			"against any open condition on this subject")
	}
	if !scan.Unjudged[key] {
		t.Error("a brief excursion was neither raised nor unjudged")
	}
}

// A busy NVMe whose own normal is 65 C must not be raised against a ceiling
// picked for spinning disks.
func TestIntegrationABusyNVMeIsNotRaisedAgainstItsOwnNormal(t *testing.T) {
	ctx, s := condCtx(t)
	host := newHost(t, ctx, s, "ewma-nvme")
	now := time.Now().UTC().Truncate(time.Minute)
	seedHostCurrent(t, ctx, s, host, now)

	high, crit := 80.0, 85.0
	sensorID := seedSensorLimits(t, ctx, s, host, "nvme", "Composite", "nvme0n1", &high, &crit)
	seedSensorSeries(t, ctx, s, host, sensorID, now, 2*24*60, 65)

	if err := s.FoldSamples(ctx); err != nil {
		t.Fatalf("FoldSamples: %v", err)
	}
	scan, err := s.ScanConditions(ctx, now, nil, hubUp)
	if err != nil {
		t.Fatalf("ScanConditions: %v", err)
	}

	key := conditions.Key{
		HostID: host, Kind: conditions.KindTemperature, Subject: "nvme/Composite/nvme0n1",
	}
	if _, bad := scan.Bad[key]; bad {
		t.Error("an NVMe sitting at its own normal 65 C was raised")
	}
	if !scan.Seen[key] {
		t.Error("a watched, healthy sensor was not recorded as seen")
	}
}
