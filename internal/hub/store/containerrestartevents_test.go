package store_test

import (
	"context"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
)

// Restarts reach the events table at ingest, in the same transaction as the
// upsert that moves the counter past them. See migration 0019 for why they are
// events rather than a column.

// restartSample is a container sample carrying the inspect-derived pair.
func restartSample(key string, at time.Time, image string, restarts *uint64, started *time.Time) *netrav1.ContainerSample {
	row := &netrav1.ContainerSample{
		TsMs:         at.UnixMilli(),
		ContainerKey: key,
		Name:         key,
		Image:        image,
		CpuPct:       proto.Float64(1),
		DockerState:  proto.String("running"),
		RestartCount: restarts,
	}
	if started != nil {
		row.StartedAtMs = proto.Int64(started.UnixMilli())
	}
	return row
}

func u64(v uint64) *uint64 { return &v }

func TestIntegrationARisingRestartCountBecomesAnEvent(t *testing.T) {
	ctx := context.Background()
	st := openMigrated(t)
	id := seedInterfaceHost(t, st, "restart-rise")

	base := time.Now().Add(-time.Hour).UTC().Truncate(time.Millisecond)

	// A first sighting establishes the counter without claiming a restart.
	if _, err := st.InsertContainerSamples(ctx, id, []*netrav1.ContainerSample{
		restartSample("shop/web", base, "nginx:1.27", u64(2), nil),
	}); err != nil {
		t.Fatalf("insert first: %v", err)
	}
	// Then it climbs.
	if _, err := st.InsertContainerSamples(ctx, id, []*netrav1.ContainerSample{
		restartSample("shop/web", base.Add(time.Minute), "nginx:1.27", u64(3), nil),
	}); err != nil {
		t.Fatalf("insert second: %v", err)
	}

	var typ, subject, severity string
	var delta int64
	if err := st.Pool().QueryRow(ctx, `
		SELECT type, subject, severity, (detail->>'delta')::bigint
		  FROM events WHERE host_id = $1 AND type LIKE 'container_%'`, id).
		Scan(&typ, &subject, &severity, &delta); err != nil {
		t.Fatalf("query events: %v", err)
	}
	if typ != "container_restart" || subject != "shop/web" {
		t.Errorf("event = %s/%s, want container_restart/shop/web", typ, subject)
	}
	if severity != "warning" {
		t.Errorf("severity = %q, want warning", severity)
	}
	if delta != 1 {
		t.Errorf("delta = %d, want 1", delta)
	}
}

// The corrective test for the whole design. A crash-looping container advances
// the counter by more than one between two observations, and the unique index
// could not hold one row per restart at a single ts even if it wanted to -- so
// every window query sums the delta and never counts rows.
func TestIntegrationARestartCountJumpingByThreeIsThreeRestarts(t *testing.T) {
	ctx := context.Background()
	st := openMigrated(t)
	id := seedInterfaceHost(t, st, "restart-jump")

	base := time.Now().Add(-time.Hour).UTC().Truncate(time.Millisecond)
	if _, err := st.InsertContainerSamples(ctx, id, []*netrav1.ContainerSample{
		restartSample("shop/web", base, "nginx:1", u64(4), nil),
	}); err != nil {
		t.Fatalf("insert first: %v", err)
	}
	if _, err := st.InsertContainerSamples(ctx, id, []*netrav1.ContainerSample{
		restartSample("shop/web", base.Add(time.Minute), "nginx:1", u64(7), nil),
	}); err != nil {
		t.Fatalf("insert second: %v", err)
	}

	var rows, summed int64
	if err := st.Pool().QueryRow(ctx, `
		SELECT count(*), coalesce(sum((detail->>'delta')::bigint), 0)
		  FROM events WHERE host_id = $1 AND type = 'container_restart'`, id).
		Scan(&rows, &summed); err != nil {
		t.Fatalf("query: %v", err)
	}
	if rows != 1 {
		t.Errorf("rows = %d, want 1: one step is one event", rows)
	}
	if summed != 3 {
		t.Errorf("sum(delta) = %d, want 3: counting rows would report 1", summed)
	}
}

// Every restart in a replayed batch is recorded, at its own instant. Comparing
// only the newest row against the stored one would collapse an outage's worth
// of restarts into a single event dated at the end.
func TestIntegrationEveryRestartInAReplayedBatchIsRecorded(t *testing.T) {
	ctx := context.Background()
	st := openMigrated(t)
	id := seedInterfaceHost(t, st, "restart-replay")

	base := time.Now().Add(-2 * time.Hour).UTC().Truncate(time.Millisecond)
	if _, err := st.InsertContainerSamples(ctx, id, []*netrav1.ContainerSample{
		restartSample("shop/web", base, "nginx:1", u64(3), nil),
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// One batch carrying a whole outage: 3 -> 4 -> 4 -> 6.
	if _, err := st.InsertContainerSamples(ctx, id, []*netrav1.ContainerSample{
		restartSample("shop/web", base.Add(1*time.Minute), "nginx:1", u64(4), nil),
		restartSample("shop/web", base.Add(2*time.Minute), "nginx:1", u64(4), nil),
		restartSample("shop/web", base.Add(3*time.Minute), "nginx:1", u64(6), nil),
	}); err != nil {
		t.Fatalf("replay: %v", err)
	}

	rows, err := st.Pool().Query(ctx, `
		SELECT ts, (detail->>'delta')::bigint
		  FROM events WHERE host_id = $1 AND type = 'container_restart' ORDER BY ts`, id)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()

	type ev struct {
		ts    time.Time
		delta int64
	}
	var got []ev
	for rows.Next() {
		var e ev
		if err := rows.Scan(&e.ts, &e.delta); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, e)
	}
	if len(got) != 2 {
		t.Fatalf("recorded %d events, want 2 (3->4 and 4->6): %+v", len(got), got)
	}
	if !got[0].ts.Equal(base.Add(1*time.Minute)) || got[0].delta != 1 {
		t.Errorf("first = %v delta %d, want %v delta 1", got[0].ts, got[0].delta, base.Add(time.Minute))
	}
	if !got[1].ts.Equal(base.Add(3*time.Minute)) || got[1].delta != 2 {
		t.Errorf("second = %v delta %d, want %v delta 2", got[1].ts, got[1].delta, base.Add(3*time.Minute))
	}
}

// The agent's ring buffer re-delivers after an outage. The natural key refuses
// the duplicates, so the same batch twice is one set of rows.
func TestIntegrationAReplayedBatchDoesNotDoubleCount(t *testing.T) {
	ctx := context.Background()
	st := openMigrated(t)
	id := seedInterfaceHost(t, st, "restart-idempotent")

	base := time.Now().Add(-time.Hour).UTC().Truncate(time.Millisecond)
	seed := []*netrav1.ContainerSample{restartSample("shop/web", base, "nginx:1", u64(1), nil)}
	batch := []*netrav1.ContainerSample{
		restartSample("shop/web", base.Add(time.Minute), "nginx:1", u64(2), nil),
	}
	if _, err := st.InsertContainerSamples(ctx, id, seed); err != nil {
		t.Fatalf("seed: %v", err)
	}
	for i := range 2 {
		if _, err := st.InsertContainerSamples(ctx, id, batch); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	var rows int64
	if err := st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM events WHERE host_id = $1 AND type = 'container_restart'`, id).
		Scan(&rows); err != nil {
		t.Fatalf("query: %v", err)
	}
	if rows != 1 {
		t.Errorf("rows = %d, want 1 after the same batch twice", rows)
	}
}

// The guard that stops a delayed delivery fabricating a redeploy. An
// out-of-order batch compared against a newer stored count reads as a decrease,
// which is exactly the shape of a recreate.
func TestIntegrationAnOutOfOrderBatchIsNotARecreate(t *testing.T) {
	ctx := context.Background()
	st := openMigrated(t)
	id := seedInterfaceHost(t, st, "restart-out-of-order")

	base := time.Now().Add(-time.Hour).UTC().Truncate(time.Millisecond)
	if _, err := st.InsertContainerSamples(ctx, id, []*netrav1.ContainerSample{
		restartSample("shop/web", base.Add(10*time.Minute), "nginx:1", u64(9), nil),
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// A late delivery of much older, much lower samples.
	if _, err := st.InsertContainerSamples(ctx, id, []*netrav1.ContainerSample{
		restartSample("shop/web", base.Add(1*time.Minute), "nginx:1", u64(2), nil),
		restartSample("shop/web", base.Add(2*time.Minute), "nginx:1", u64(5), nil),
	}); err != nil {
		t.Fatalf("late batch: %v", err)
	}

	var rows int64
	if err := st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM events WHERE host_id = $1 AND type LIKE 'container_%'`, id).
		Scan(&rows); err != nil {
		t.Fatalf("query: %v", err)
	}
	if rows != 0 {
		t.Errorf("recorded %d events from samples older than last_seen, want 0", rows)
	}
}

// A decrease is a redeploy, and the images it replaced are worth naming.
func TestIntegrationAFallingRestartCountIsARecreate(t *testing.T) {
	ctx := context.Background()
	st := openMigrated(t)
	id := seedInterfaceHost(t, st, "restart-recreate")

	base := time.Now().Add(-time.Hour).UTC().Truncate(time.Millisecond)
	if _, err := st.InsertContainerSamples(ctx, id, []*netrav1.ContainerSample{
		restartSample("web/caddy", base, "caddy:2.7-alpine", u64(6), nil),
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := st.InsertContainerSamples(ctx, id, []*netrav1.ContainerSample{
		restartSample("web/caddy", base.Add(time.Minute), "caddy:2-alpine", u64(0), nil),
	}); err != nil {
		t.Fatalf("redeploy: %v", err)
	}

	var typ, severity string
	var from, to *string
	if err := st.Pool().QueryRow(ctx, `
		SELECT type, severity, detail->>'image_from', detail->>'image_to'
		  FROM events WHERE host_id = $1 AND type LIKE 'container_%'`, id).
		Scan(&typ, &severity, &from, &to); err != nil {
		t.Fatalf("query: %v", err)
	}
	if typ != "container_recreate" {
		t.Errorf("type = %q, want container_recreate", typ)
	}
	if severity != "info" {
		t.Errorf("severity = %q, want info", severity)
	}
	if from == nil || *from != "caddy:2.7-alpine" || to == nil || *to != "caddy:2-alpine" {
		t.Errorf("images = %v -> %v, want caddy:2.7-alpine -> caddy:2-alpine", from, to)
	}
}

// A container the hub has never seen has not restarted as far as anyone knows.
func TestIntegrationTheFirstSightingOfAContainerIsNotARestart(t *testing.T) {
	ctx := context.Background()
	st := openMigrated(t)
	id := seedInterfaceHost(t, st, "restart-first")

	at := time.Now().Add(-time.Minute).UTC()
	if _, err := st.InsertContainerSamples(ctx, id, []*netrav1.ContainerSample{
		restartSample("shop/web", at, "nginx:1", u64(17), nil),
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	var rows int64
	if err := st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM events WHERE host_id = $1 AND type LIKE 'container_%'`, id).
		Scan(&rows); err != nil {
		t.Fatalf("query: %v", err)
	}
	if rows != 0 {
		t.Errorf("recorded %d events on a first sighting, want 0", rows)
	}
}

// The payoff from collecting started_at: the event is dated to the second the
// container actually came up, not to the scrape that noticed minutes later.
func TestIntegrationARestartEventIsDatedFromDockersStartTime(t *testing.T) {
	ctx := context.Background()
	st := openMigrated(t)
	id := seedInterfaceHost(t, st, "restart-dated")

	base := time.Now().Add(-time.Hour).UTC().Truncate(time.Millisecond)
	oldStart := base.Add(-24 * time.Hour)
	newStart := base.Add(2 * time.Minute)

	if _, err := st.InsertContainerSamples(ctx, id, []*netrav1.ContainerSample{
		restartSample("shop/web", base, "nginx:1", u64(1), &oldStart),
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Noticed eight minutes after it happened -- the inspect cache's lag.
	if _, err := st.InsertContainerSamples(ctx, id, []*netrav1.ContainerSample{
		restartSample("shop/web", base.Add(10*time.Minute), "nginx:1", u64(2), &newStart),
	}); err != nil {
		t.Fatalf("restart: %v", err)
	}

	var ts time.Time
	var source string
	if err := st.Pool().QueryRow(ctx, `
		SELECT ts, detail->>'ts_source'
		  FROM events WHERE host_id = $1 AND type = 'container_restart'`, id).
		Scan(&ts, &source); err != nil {
		t.Fatalf("query: %v", err)
	}
	if !ts.Equal(newStart) {
		t.Errorf("ts = %v, want Docker's own %v", ts.UTC(), newStart)
	}
	if source != "docker_started_at" {
		t.Errorf("ts_source = %q, want docker_started_at", source)
	}
}

// Two restarts of the same container at different instants are two facts. This
// is the anti-0017 test: the dedup that collapses a restated STATE must never
// be applied to an occurrence.
func TestIntegrationRepeatedRestartEventsAreAllStored(t *testing.T) {
	ctx := context.Background()
	st := openMigrated(t)
	id := seedInterfaceHost(t, st, "restart-repeat")

	base := time.Now().Add(-time.Hour).UTC().Truncate(time.Millisecond)
	counts := []uint64{1, 2, 3, 4}
	for i, c := range counts {
		if _, err := st.InsertContainerSamples(ctx, id, []*netrav1.ContainerSample{
			restartSample("shop/web", base.Add(time.Duration(i)*time.Minute), "nginx:1", u64(c), nil),
		}); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	var rows int64
	if err := st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM events WHERE host_id = $1 AND type = 'container_restart'`, id).
		Scan(&rows); err != nil {
		t.Fatalf("query: %v", err)
	}
	// Three steps from four samples; the first established the counter.
	if rows != 3 {
		t.Errorf("rows = %d, want 3: how often a container restarts IS the reading", rows)
	}
}

// One container Postgres refuses must not take another container's restart down
// with it. That is why the transaction is per container rather than per batch.
func TestIntegrationAPoisonContainerDoesNotLoseAnotherContainersRestart(t *testing.T) {
	ctx := context.Background()
	st := openMigrated(t)
	id := seedInterfaceHost(t, st, "restart-poison")

	base := time.Now().Add(-time.Hour).UTC().Truncate(time.Millisecond)
	if _, err := st.InsertContainerSamples(ctx, id, []*netrav1.ContainerSample{
		restartSample("shop/web", base, "nginx:1", u64(1), nil),
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// A NUL byte is the classic unstorable text value; it must be quarantined
	// alone.
	poison := restartSample("bad\x00key", base.Add(time.Minute), "nginx:1", u64(1), nil)
	good := restartSample("shop/web", base.Add(time.Minute), "nginx:1", u64(2), nil)
	if _, err := st.InsertContainerSamples(ctx, id, []*netrav1.ContainerSample{poison, good}); err != nil {
		t.Fatalf("mixed batch: %v", err)
	}

	var rows int64
	if err := st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM events WHERE host_id = $1 AND type = 'container_restart'`, id).
		Scan(&rows); err != nil {
		t.Fatalf("query: %v", err)
	}
	if rows != 1 {
		t.Errorf("rows = %d, want 1: the good container's restart must survive", rows)
	}
}

// The count that rides the listing row is a SUM of deltas over the window, not
// a row count, and it is what the fleet list prints beside a container's name.
func TestIntegrationContainersReportRestartsInTheWindow(t *testing.T) {
	ctx := context.Background()
	st := openMigrated(t)
	id := seedInterfaceHost(t, st, "restart-window")

	base := time.Now().Add(-time.Hour).UTC().Truncate(time.Millisecond)
	// 2 -> 3 -> 6: one restart, then three more.
	for i, c := range []uint64{2, 3, 6} {
		if _, err := st.InsertContainerSamples(ctx, id, []*netrav1.ContainerSample{
			restartSample("shop/web", base.Add(time.Duration(i)*time.Minute), "nginx:1", u64(c), nil),
		}); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	var summed, rows int64
	if err := st.Pool().QueryRow(ctx, `
		SELECT coalesce(sum((detail->>'delta')::bigint), 0), count(*)
		  FROM events
		 WHERE host_id = $1 AND type = 'container_restart'
		   AND ts > now() - INTERVAL '24 hours'`, id).Scan(&summed, &rows); err != nil {
		t.Fatalf("query: %v", err)
	}
	if rows != 2 {
		t.Errorf("rows = %d, want 2 steps", rows)
	}
	if summed != 4 {
		t.Errorf("sum(delta) = %d, want 4 restarts in the window", summed)
	}
}

// Restarts older than the window are not counted -- the reading is "today",
// not "ever". containers.restart_count remains the all-time number.
func TestIntegrationRestartsOutsideTheWindowAreNotCounted(t *testing.T) {
	ctx := context.Background()
	st := openMigrated(t)
	id := seedInterfaceHost(t, st, "restart-outside")

	old := time.Now().Add(-72 * time.Hour).UTC().Truncate(time.Millisecond)
	for i, c := range []uint64{1, 2} {
		if _, err := st.InsertContainerSamples(ctx, id, []*netrav1.ContainerSample{
			restartSample("shop/web", old.Add(time.Duration(i)*time.Minute), "nginx:1", u64(c), nil),
		}); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	var summed int64
	if err := st.Pool().QueryRow(ctx, `
		SELECT coalesce(sum((detail->>'delta')::bigint), 0)
		  FROM events
		 WHERE host_id = $1 AND type = 'container_restart'
		   AND ts > now() - INTERVAL '24 hours'`, id).Scan(&summed); err != nil {
		t.Fatalf("query: %v", err)
	}
	if summed != 0 {
		t.Errorf("sum(delta) = %d inside 24h, want 0 for a restart three days old", summed)
	}
}
