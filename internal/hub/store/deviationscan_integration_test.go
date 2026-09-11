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

// seedSensorSeries writes n samples one minute apart ending at `end`, all at
// the same temperature. Enough of them to clear BaselineMinSamples is what a
// baseline needs before it may judge anything.
func seedSensorSeries(t *testing.T, ctx context.Context, s *store.Store,
	host, sensorID int32, end time.Time, n int, temp float64) {
	t.Helper()
	rows := make([][]any, 0, n)
	for i := range n {
		ts := end.Add(-time.Duration(n-1-i) * time.Minute)
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
		ts := end.Add(-time.Duration(n-1-i) * time.Minute)
		rows = append(rows, []any{host, ts, procs, load})
	}
	if _, err := s.Pool().CopyFrom(ctx,
		[]string{"host_samples"},
		[]string{"host_id", "ts", "processes_total", "load5"},
		pgx.CopyFromRows(rows)); err != nil {
		t.Fatalf("host_samples: %v", err)
	}
}

// CopyFrom rather than N inserts: these tests seed thousands of samples to
// clear the min-samples gate, and doing that one statement at a time turns a
// five-second test into a ninety-second one.

const enoughSamples = conditions.BaselineMinSamples + 10

// The migration runs, the function resolves, and a steady series produces a
// baseline whose percentiles bracket it.
func TestIntegrationBaselineIsComputedFromTheSeries(t *testing.T) {
	ctx, s := condCtx(t)
	host := newHost(t, ctx, s, "dev-baseline")
	now := time.Now().UTC().Truncate(time.Minute)

	sensorID := seedSensorLimits(t, ctx, s, host, "drivetemp", "temp1", "sda", nil, nil)
	seedSensorSeries(t, ctx, s, host, sensorID, now, enoughSamples, 44)

	if err := s.RecomputeBaselines(ctx, ""); err != nil {
		t.Fatalf("RecomputeBaselines: %v", err)
	}

	var p01, p99 float64
	var count int
	if err := s.Pool().QueryRow(ctx, `
		SELECT p01, p99, sample_count FROM metric_baselines
		 WHERE host_id = $1 AND kind = 'temperature' AND subject = 'drivetemp/temp1/sda'`,
		host).Scan(&p01, &p99, &count); err != nil {
		t.Fatalf("no baseline row: %v", err)
	}
	if p01 != 44 || p99 != 44 {
		t.Errorf("percentiles = %v/%v, want 44/44 for a flat series", p01, p99)
	}
	if count != enoughSamples {
		t.Errorf("sample_count = %d, want %d", count, enoughSamples)
	}
}

// THE BUG THE EXCLUSION EXISTS FOR, end to end.
//
// A drive runs at 44 C for a week, then jumps to 58 and stays. The condition
// opens. Without the exclusion, the next daily recompute sees a day of 58 C --
// fourteen per cent of the window, against the one per cent p99 discards -- so
// p99 climbs, the threshold climbs past the reading, and the hub records a
// recovery for a drive that is still at 58 C.
func TestIntegrationASustainedFaultDoesNotRaiseItsOwnBaseline(t *testing.T) {
	ctx, s := condCtx(t)
	host := newHost(t, ctx, s, "dev-selferase")
	now := time.Now().UTC().Truncate(time.Minute)

	sensorID := seedSensorLimits(t, ctx, s, host, "drivetemp", "temp1", "sda", nil, nil)

	// A week of normal, then a day of fault, in one continuous series.
	faultMinutes := 24 * 60
	seedSensorSeries(t, ctx, s, host, sensorID,
		now.Add(-time.Duration(faultMinutes)*time.Minute), enoughSamples, 44)
	seedSensorSeries(t, ctx, s, host, sensorID, now, faultMinutes, 58)

	// The condition the hub opened when the drive first crossed.
	opened := now.Add(-time.Duration(faultMinutes) * time.Minute)
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO host_conditions (host_id, kind, subject, severity, opened_ts)
		VALUES ($1, 'temperature', 'drivetemp/temp1/sda', 'critical', $2)`,
		host, opened); err != nil {
		t.Fatalf("insert condition: %v", err)
	}

	if err := s.RecomputeBaselines(ctx, ""); err != nil {
		t.Fatalf("RecomputeBaselines: %v", err)
	}

	var p99 float64
	if err := s.Pool().QueryRow(ctx, `
		SELECT p99 FROM metric_baselines
		 WHERE host_id = $1 AND kind = 'temperature' AND subject = 'drivetemp/temp1/sda'`,
		host).Scan(&p99); err != nil {
		t.Fatalf("no baseline row: %v", err)
	}

	if p99 > 50 {
		t.Errorf("p99 = %v -- the fault raised its own baseline, "+
			"which is how a still-hot drive gets reported as recovered", p99)
	}
}

// The second-order case. A condition open across the WHOLE window leaves too
// few samples behind, and the recompute must then leave the existing baseline
// alone rather than delete it or write a low sample_count -- either of which
// would push the subject into unjudged and strand a live condition.
func TestIntegrationAnAllWindowFaultLeavesTheBaselineIntact(t *testing.T) {
	ctx, s := condCtx(t)
	host := newHost(t, ctx, s, "dev-stranded")
	now := time.Now().UTC().Truncate(time.Minute)

	sensorID := seedSensorLimits(t, ctx, s, host, "drivetemp", "temp1", "sdb", nil, nil)
	seedSensorSeries(t, ctx, s, host, sensorID, now, enoughSamples, 44)

	if err := s.RecomputeBaselines(ctx, ""); err != nil {
		t.Fatalf("first recompute: %v", err)
	}

	// Now a condition that has been open since before every one of those
	// samples was taken.
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO host_conditions (host_id, kind, subject, severity, opened_ts)
		VALUES ($1, 'temperature', 'drivetemp/temp1/sdb', 'critical', $2)`,
		host, now.Add(-30*24*time.Hour)); err != nil {
		t.Fatalf("insert condition: %v", err)
	}

	if err := s.RecomputeBaselines(ctx, ""); err != nil {
		t.Fatalf("second recompute: %v", err)
	}

	var count int
	if err := s.Pool().QueryRow(ctx, `
		SELECT sample_count FROM metric_baselines
		 WHERE host_id = $1 AND kind = 'temperature' AND subject = 'drivetemp/temp1/sdb'`,
		host).Scan(&count); err != nil {
		t.Fatalf("the baseline was deleted, stranding an open condition: %v", err)
	}
	if count != enoughSamples {
		t.Errorf("sample_count = %d, want the previous %d kept", count, enoughSamples)
	}
}

// A host-wide kind reaches the same table with an empty subject.
func TestIntegrationHostGaugeBaselinesAreComputed(t *testing.T) {
	ctx, s := condCtx(t)
	host := newHost(t, ctx, s, "dev-gauges")
	now := time.Now().UTC().Truncate(time.Minute)

	seedHostSeries(t, ctx, s, host, now, enoughSamples, 300, 1.5)

	if err := s.RecomputeBaselines(ctx, ""); err != nil {
		t.Fatalf("RecomputeBaselines: %v", err)
	}

	for _, tc := range []struct {
		kind string
		want float64
	}{
		{conditions.KindProcesses, 300},
		{conditions.KindLoad, 1.5},
	} {
		var p99 float64
		if err := s.Pool().QueryRow(ctx, `
			SELECT p99 FROM metric_baselines
			 WHERE host_id = $1 AND kind = $2 AND subject = ''`,
			host, tc.kind).Scan(&p99); err != nil {
			t.Fatalf("%s: no baseline row: %v", tc.kind, err)
		}
		if p99 != tc.want {
			t.Errorf("%s p99 = %v, want %v", tc.kind, p99, tc.want)
		}
	}
}

// A subject with too little history is UNJUDGED, not healthy -- a new host must
// not be declared fine by a rule that has not watched it yet.
func TestIntegrationAnUncalibratedSubjectIsUnjudged(t *testing.T) {
	ctx, s := condCtx(t)
	host := newHost(t, ctx, s, "dev-new")
	now := time.Now().UTC().Truncate(time.Minute)
	seedHostCurrent(t, ctx, s, host, now)

	sensorID := seedSensorLimits(t, ctx, s, host, "coretemp", "Package id 0", "", nil, nil)
	// One reading, and no baseline at all.
	seedSensorSeries(t, ctx, s, host, sensorID, now, 1, 92)

	if err := s.RecomputeBaselines(ctx, ""); err != nil {
		t.Fatalf("RecomputeBaselines: %v", err)
	}

	scan, err := s.ScanConditions(ctx, now, nil, hubUp)
	if err != nil {
		t.Fatalf("ScanConditions: %v", err)
	}

	key := conditions.Key{
		HostID: host, Kind: conditions.KindTemperature, Subject: "coretemp/Package id 0",
	}
	if !scan.Unjudged[key] {
		t.Error("a sensor with no baseline was not unjudged")
	}
	if scan.Seen[key] {
		t.Error("a sensor with no baseline was recorded as seen -- it would read as healthy")
	}
	if _, bad := scan.Bad[key]; bad {
		t.Error("a sensor with no baseline was judged bad")
	}
}

// The whole rule, end to end: a calibrated subject that departs from its own
// normal is raised, and one sitting inside it is not.
func TestIntegrationADepartureFromNormalIsRaised(t *testing.T) {
	ctx, s := condCtx(t)
	host := newHost(t, ctx, s, "dev-departure")
	now := time.Now().UTC().Truncate(time.Minute)
	seedHostCurrent(t, ctx, s, host, now)

	sensorID := seedSensorLimits(t, ctx, s, host, "drivetemp", "temp1", "sda", nil, nil)
	// A week at 44 C, ending an hour ago so the baseline is not built from the
	// departure itself.
	seedSensorSeries(t, ctx, s, host, sensorID, now.Add(-time.Hour), enoughSamples, 44)

	if err := s.RecomputeBaselines(ctx, ""); err != nil {
		t.Fatalf("RecomputeBaselines: %v", err)
	}

	// Now a current reading well past it.
	seedSensorSeries(t, ctx, s, host, sensorID, now, 1, 61)

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
	if !scan.Evaluated[conditions.KindTemperature] {
		t.Error("the temperature kind was not marked evaluated")
	}
}

// A busy NVMe whose own normal is 65 C must not be raised against a ceiling
// picked for spinning disks. This is the regression that would have lit up
// every NVMe host permanently red.
func TestIntegrationABusyNVMeIsNotRaisedAgainstItsOwnNormal(t *testing.T) {
	ctx, s := condCtx(t)
	host := newHost(t, ctx, s, "dev-nvme")
	now := time.Now().UTC().Truncate(time.Minute)
	seedHostCurrent(t, ctx, s, host, now)

	high, crit := 80.0, 85.0
	sensorID := seedSensorLimits(t, ctx, s, host, "nvme", "Composite", "nvme0n1", &high, &crit)
	seedSensorSeries(t, ctx, s, host, sensorID, now, enoughSamples, 65)

	if err := s.RecomputeBaselines(ctx, ""); err != nil {
		t.Fatalf("RecomputeBaselines: %v", err)
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
		t.Error("a calibrated, healthy sensor was not recorded as seen")
	}
}
