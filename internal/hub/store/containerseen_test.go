package store_test

import (
	"context"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
)

// containerSample builds one sample for a container key at an instant. Only
// the fields resolveContainerIDs reads are set: this file is about the
// containers ROW, not about the metrics beside it.
func containerSample(key string, at time.Time) *netrav1.ContainerSample {
	return &netrav1.ContainerSample{
		TsMs:         at.UnixMilli(),
		ContainerKey: key,
		Name:         key,
		Image:        "nginx:1.27",
		CpuPct:       proto.Float64(1),
	}
}

// last_seen is stamped from the SAMPLE's timestamp, not from the hub's clock.
//
// The agent's ring buffer replays buffered scrapes after a hub outage, so a
// batch landing now can carry samples taken overnight. now() would date them
// to arrival and report a container as seen "just now" -- the exact opposite
// of the staleness cue the Containers table reads this column for.
func TestIntegrationContainerLastSeenComesFromTheSampleNotTheClock(t *testing.T) {
	ctx := context.Background()
	s := openMigrated(t)
	id := seedInterfaceHost(t, s, "replayed-containers")

	sixHoursAgo := time.Now().Add(-6 * time.Hour)
	if _, err := s.InsertContainerSamples(ctx, id, []*netrav1.ContainerSample{
		containerSample("shop/web", sixHoursAgo),
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	var lastSeen time.Time
	if err := s.Pool().QueryRow(ctx,
		`SELECT last_seen FROM containers WHERE host_id = $1`, id).Scan(&lastSeen); err != nil {
		t.Fatalf("query: %v", err)
	}
	if age := time.Since(lastSeen); age < 5*time.Hour {
		t.Errorf("last_seen is only %s old for a sample taken six hours ago; "+
			"a replayed scrape must not report the container as seen just now", age)
	}
}

// A later scrape moves it forward -- otherwise every container would drift
// into "gone" while it is running.
func TestIntegrationContainerLastSeenAdvancesOnTheNextScrape(t *testing.T) {
	ctx := context.Background()
	s := openMigrated(t)
	id := seedInterfaceHost(t, s, "advancing")

	if _, err := s.InsertContainerSamples(ctx, id, []*netrav1.ContainerSample{
		containerSample("shop/web", time.Now().Add(-2*time.Hour)),
	}); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if _, err := s.InsertContainerSamples(ctx, id, []*netrav1.ContainerSample{
		containerSample("shop/web", time.Now()),
	}); err != nil {
		t.Fatalf("second insert: %v", err)
	}

	var lastSeen time.Time
	if err := s.Pool().QueryRow(ctx,
		`SELECT last_seen FROM containers WHERE host_id = $1`, id).Scan(&lastSeen); err != nil {
		t.Fatalf("query: %v", err)
	}
	if age := time.Since(lastSeen); age > time.Minute {
		t.Errorf("last_seen is %s old after the container was reported again; "+
			"a running container would render as gone", age)
	}
}

// And a replay of OLDER samples must not walk it backwards, which would mark a
// running container gone in the UI and offer to purge its history.
func TestIntegrationContainerLastSeenNeverMovesBackwards(t *testing.T) {
	ctx := context.Background()
	s := openMigrated(t)
	id := seedInterfaceHost(t, s, "out-of-order-containers")

	current := time.Now()
	if _, err := s.InsertContainerSamples(ctx, id, []*netrav1.ContainerSample{
		containerSample("shop/web", current),
	}); err != nil {
		t.Fatalf("current insert: %v", err)
	}
	// A late batch carrying week-old samples.
	if _, err := s.InsertContainerSamples(ctx, id, []*netrav1.ContainerSample{
		containerSample("shop/web", current.Add(-7*24*time.Hour)),
	}); err != nil {
		t.Fatalf("late insert: %v", err)
	}

	var lastSeen time.Time
	if err := s.Pool().QueryRow(ctx,
		`SELECT last_seen FROM containers WHERE host_id = $1`, id).Scan(&lastSeen); err != nil {
		t.Fatalf("query: %v", err)
	}
	if age := time.Since(lastSeen); age > time.Minute {
		t.Errorf("last_seen moved back to %s old after a late batch of older "+
			"samples; the newest sample is current", age)
	}
}

// A batch spanning several scrapes stamps the NEWEST of them, not whichever
// row the loop happened to reach first.
func TestIntegrationContainerLastSeenTakesTheNewestRowInTheBatch(t *testing.T) {
	ctx := context.Background()
	s := openMigrated(t)
	id := seedInterfaceHost(t, s, "batched")

	now := time.Now()
	if _, err := s.InsertContainerSamples(ctx, id, []*netrav1.ContainerSample{
		containerSample("shop/web", now.Add(-10*time.Minute)),
		containerSample("shop/web", now.Add(-5*time.Minute)),
		containerSample("shop/web", now),
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	var lastSeen time.Time
	if err := s.Pool().QueryRow(ctx,
		`SELECT last_seen FROM containers WHERE host_id = $1`, id).Scan(&lastSeen); err != nil {
		t.Fatalf("query: %v", err)
	}
	if age := time.Since(lastSeen); age > time.Minute {
		t.Errorf("last_seen is %s old after a batch whose newest sample is now", age)
	}
}

// The prune itself, called directly rather than waited on: it is registered as
// a daily job, and a test that slept for one would be a test nobody runs.
func TestIntegrationStaleContainersArePrunedAndLiveOnesAreNot(t *testing.T) {
	ctx := context.Background()
	s := openMigrated(t)
	id := seedInterfaceHost(t, s, "mixed-containers")

	now := time.Now()
	if _, err := s.InsertContainerSamples(ctx, id, []*netrav1.ContainerSample{
		containerSample("shop/web", now),
		containerSample("shop/old", now),
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	// shop/old was removed from the machine most of a year ago.
	//
	// BOTH columns, because the prune requires both: last_seen is the agent's
	// clock and first_seen is the hub's, and a row is only stale when it is
	// stale by both. Ageing last_seen alone leaves it alive, which is the
	// guard doing its job rather than the fixture being wrong.
	if _, err := s.Pool().Exec(ctx,
		`UPDATE containers
		    SET last_seen = now() - INTERVAL '200 days',
		        first_seen = now() - INTERVAL '400 days'
		  WHERE host_id = $1 AND container_key = 'shop/old'`, id); err != nil {
		t.Fatalf("age shop/old: %v", err)
	}

	if _, err := s.Pool().Exec(ctx,
		`CALL netra_prune_stale_containers(0, '{"retention": "120 days"}'::jsonb)`); err != nil {
		t.Fatalf("prune: %v", err)
	}

	var keys []string
	rows, err := s.Pool().Query(ctx,
		`SELECT container_key FROM containers WHERE host_id = $1 ORDER BY container_key`, id)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			t.Fatalf("scan: %v", err)
		}
		keys = append(keys, k)
	}
	if len(keys) != 1 || keys[0] != "shop/web" {
		t.Errorf("containers = %v, want only shop/web -- the removed one is "+
			"past the horizon and the live one is not", keys)
	}
}

// A host whose clock is months behind reports samples stamped before the
// cutoff on their very first scrape. Keyed on last_seen alone the prune would
// delete those containers the same night, the next scrape would recreate them,
// and the cycle would never end. first_seen is the hub's own clock and is what
// stops it.
func TestIntegrationContainerFirstSeenFloorSurvivesABadAgentClock(t *testing.T) {
	ctx := context.Background()
	s := openMigrated(t)
	id := seedInterfaceHost(t, s, "bad-clock")

	// Well past the 120-day horizon, but inside what the ingest API accepts.
	if _, err := s.InsertContainerSamples(ctx, id, []*netrav1.ContainerSample{
		containerSample("shop/web", time.Now().Add(-300*24*time.Hour)),
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	if _, err := s.Pool().Exec(ctx,
		`CALL netra_prune_stale_containers(0, '{"retention": "120 days"}'::jsonb)`); err != nil {
		t.Fatalf("prune: %v", err)
	}

	var count int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM containers WHERE host_id = $1`, id).Scan(&count); err != nil {
		t.Fatalf("query: %v", err)
	}
	if count != 1 {
		t.Errorf("containers = %d, want 1: a host with a slow clock must not "+
			"have its containers deleted and re-created for ever", count)
	}
}
