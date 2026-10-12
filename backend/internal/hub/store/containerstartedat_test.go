package store_test

import (
	"context"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
)

// containers.started_at is Docker's own State.StartedAt for the container's
// current incarnation, and it is the only column that answers "how long has
// this been up".
//
// The defect it fixes is state_ts being mistaken for that answer. state_ts is
// hub-derived from OBSERVED transitions and is stamped with the first sample's
// ts, so a container that had been running for a year read as "since two
// minutes ago" from the moment netra first heard of it -- and nothing else on
// the row contradicted it.
//
// It shares the agent's inspect cache entry with restart_count, so the write
// rule is the same as that column's: OVERWRITE, including with NULL.

// startedSample is a container sample carrying the two fields that ride one
// inspect response.
func startedSample(key string, at time.Time, restarts *uint64, started *time.Time) *netrav1.ContainerSample {
	row := &netrav1.ContainerSample{
		TsMs:         at.UnixMilli(),
		ContainerKey: key,
		Name:         key,
		Image:        "nginx:1.27",
		CpuPct:       proto.Float64(1),
		DockerState:  proto.String("running"),
		RestartCount: restarts,
	}
	if started != nil {
		row.StartedAtMs = proto.Int64(started.UnixMilli())
	}
	return row
}

func TestIntegrationContainerCarriesWhenItStarted(t *testing.T) {
	ctx := context.Background()
	s := openMigrated(t)
	id := seedInterfaceHost(t, s, "started-at")

	at := time.Now().Add(-time.Minute).UTC()
	started := at.Add(-90 * 24 * time.Hour).Truncate(time.Millisecond)
	restarts := uint64(2)
	if _, err := s.InsertContainerSamples(ctx, id, []*netrav1.ContainerSample{
		startedSample("shop/web", at, &restarts, &started),
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	var got *time.Time
	if err := s.Pool().QueryRow(ctx,
		`SELECT started_at FROM containers WHERE host_id = $1`, id).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got == nil {
		t.Fatal("started_at is null; the agent reported one")
	}
	if !got.Equal(started) {
		t.Errorf("started_at = %v, want %v", got.UTC(), started)
	}
}

// The defect, pinned. A container running long before netra saw it must report
// a start time far older than the state timestamp the hub stamped on first
// sight -- and state_ts must not overwrite it.
func TestIntegrationStartedAtIsNotTheStateTimestamp(t *testing.T) {
	ctx := context.Background()
	s := openMigrated(t)
	id := seedInterfaceHost(t, s, "started-vs-state")

	at := time.Now().Add(-time.Minute).UTC()
	// Up for a year before this hub ever heard of it.
	started := at.Add(-365 * 24 * time.Hour).Truncate(time.Millisecond)
	restarts := uint64(0)
	if _, err := s.InsertContainerSamples(ctx, id, []*netrav1.ContainerSample{
		startedSample("shop/web", at, &restarts, &started),
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	var stateTS, startedAt *time.Time
	if err := s.Pool().QueryRow(ctx,
		`SELECT state_ts, started_at FROM containers WHERE host_id = $1`,
		id).Scan(&stateTS, &startedAt); err != nil {
		t.Fatalf("query: %v", err)
	}
	if stateTS == nil || startedAt == nil {
		t.Fatalf("state_ts = %v, started_at = %v; both should be set", stateTS, startedAt)
	}
	if !startedAt.Equal(started) {
		t.Errorf("started_at = %v, want %v", startedAt.UTC(), started)
	}
	// state_ts is the sample's ts -- first sighting -- and says nothing about
	// how long the container had been up. That gap is the whole point.
	if !stateTS.Equal(at.Truncate(time.Millisecond)) {
		t.Errorf("state_ts = %v, want the sample ts %v", stateTS.UTC(), at)
	}
	if !startedAt.Before(*stateTS) {
		t.Error("started_at is not older than state_ts; the two must not be the same reading")
	}
}

// The shared-cache invariant, at the hub end. The agent drops both fields the
// moment an inspect is refused, so a sample carrying neither means "cannot
// answer" -- and holding the last start time would put an uptime on a
// container whose restart count the hub has just correctly forgotten.
func TestIntegrationStartedAtIsForgottenWhenTheAgentCanNoLongerInspect(t *testing.T) {
	ctx := context.Background()
	s := openMigrated(t)
	id := seedInterfaceHost(t, s, "started-forgotten")

	at := time.Now().Add(-2 * time.Minute).UTC()
	started := at.Add(-time.Hour).Truncate(time.Millisecond)
	restarts := uint64(3)
	if _, err := s.InsertContainerSamples(ctx, id, []*netrav1.ContainerSample{
		startedSample("shop/web", at, &restarts, &started),
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	// A later scrape from an agent that can no longer inspect: neither field.
	if _, err := s.InsertContainerSamples(ctx, id, []*netrav1.ContainerSample{
		startedSample("shop/web", at.Add(time.Minute), nil, nil),
	}); err != nil {
		t.Fatalf("insert second: %v", err)
	}

	var startedAt *time.Time
	var count *int64
	if err := s.Pool().QueryRow(ctx,
		`SELECT started_at, restart_count FROM containers WHERE host_id = $1`,
		id).Scan(&startedAt, &count); err != nil {
		t.Fatalf("query: %v", err)
	}
	if startedAt != nil {
		t.Errorf("started_at = %v after the agent stopped reporting it; it must be forgotten", startedAt)
	}
	if count != nil {
		t.Errorf("restart_count = %v; it must be forgotten alongside started_at", count)
	}
}

// A scrape that changed nothing else must not disturb it. The value is
// constant for the life of an incarnation, so every scrape re-sends the same
// instant from the agent's cache.
func TestIntegrationStartedAtSurvivesAScrapeThatChangedNothing(t *testing.T) {
	ctx := context.Background()
	s := openMigrated(t)
	id := seedInterfaceHost(t, s, "started-steady")

	at := time.Now().Add(-3 * time.Minute).UTC()
	started := at.Add(-6 * time.Hour).Truncate(time.Millisecond)
	restarts := uint64(1)
	for i := range 3 {
		if _, err := s.InsertContainerSamples(ctx, id, []*netrav1.ContainerSample{
			startedSample("shop/web", at.Add(time.Duration(i)*time.Minute), &restarts, &started),
		}); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	var got *time.Time
	if err := s.Pool().QueryRow(ctx,
		`SELECT started_at FROM containers WHERE host_id = $1`, id).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got == nil || !got.Equal(started) {
		t.Errorf("started_at = %v, want %v across steady scrapes", got, started)
	}
}

// Zero is not an instant. An agent that sent 0 -- or any non-positive value --
// would otherwise be stored as 1970 and read as a container up for fifty-odd
// years. The agent already omits the field; the hub refuses it too, because
// only one of the two has to be wrong for the reading to be absurd.
func TestIntegrationANonPositiveStartTimeIsRefused(t *testing.T) {
	ctx := context.Background()
	s := openMigrated(t)
	id := seedInterfaceHost(t, s, "started-zero")

	at := time.Now().Add(-time.Minute).UTC()
	restarts := uint64(0)
	row := startedSample("shop/web", at, &restarts, nil)
	row.StartedAtMs = proto.Int64(0)
	if _, err := s.InsertContainerSamples(ctx, id, []*netrav1.ContainerSample{row}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	var got *time.Time
	if err := s.Pool().QueryRow(ctx,
		`SELECT started_at FROM containers WHERE host_id = $1`, id).Scan(&got); err != nil {
		t.Fatalf("query: %v", err)
	}
	if got != nil {
		t.Errorf("started_at = %v, want null for a non-positive epoch", got)
	}
}
