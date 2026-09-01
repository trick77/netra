package store_test

import (
	"context"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
)

// What the Docker socket says lands on the containers ROW, next to name and
// image, rather than in the hypertable beside the metrics. These are
// low-cardinality strings that change rarely; a column per sample would store
// the word "running" 1440 times a day per container.
//
// The write rule is the thing worth pinning, and it is deliberately NOT
// uniform: state, health and labels overwrite -- including with NULL -- while
// restart_count coalesces. See resolveContainerIDs.

// dockerSample is containerSample plus everything the socket contributes.
func dockerSample(key string, at time.Time, state, health string, restarts *uint64, labels map[string]string) *netrav1.ContainerSample {
	row := &netrav1.ContainerSample{
		TsMs:         at.UnixMilli(),
		ContainerKey: key,
		Name:         key,
		Image:        "nginx:1.27",
		CpuPct:       proto.Float64(1),
		RestartCount: restarts,
	}
	if state != "" {
		row.DockerState = proto.String(state)
	}
	if health != "" {
		row.Health = proto.String(health)
	}
	if labels != nil {
		row.Labels = &netrav1.ContainerLabels{Values: labels}
	}
	return row
}

func TestIntegrationContainerCarriesWhatDockerSaid(t *testing.T) {
	ctx := context.Background()
	s := openMigrated(t)
	id := seedInterfaceHost(t, s, "docker-meta")

	at := time.Now().Add(-time.Minute)
	restarts := uint64(4)
	if _, err := s.InsertContainerSamples(ctx, id, []*netrav1.ContainerSample{
		dockerSample("shop/web", at, "running", "healthy", &restarts,
			map[string]string{"traefik.enable": "true"}),
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	var state, health *string
	var count *int64
	var labels map[string]string
	if err := s.Pool().QueryRow(ctx,
		`SELECT docker_state, health, restart_count, labels FROM containers WHERE host_id = $1`,
		id).Scan(&state, &health, &count, &labels); err != nil {
		t.Fatalf("query: %v", err)
	}

	if state == nil || *state != "running" {
		t.Errorf("docker_state = %v, want running", state)
	}
	if health == nil || *health != "healthy" {
		t.Errorf("health = %v, want healthy", health)
	}
	if count == nil || *count != 4 {
		t.Errorf("restart_count = %v, want 4", count)
	}
	if labels["traefik.enable"] != "true" {
		t.Errorf("labels = %v, want traefik.enable=true", labels)
	}

	// And the per-sample counter, which is what makes a restart chartable and
	// lets a hole in the series be attributed.
	var sampled *int64
	if err := s.Pool().QueryRow(ctx,
		`SELECT restart_count FROM container_samples WHERE host_id = $1`, id).Scan(&sampled); err != nil {
		t.Fatalf("query sample: %v", err)
	}
	if sampled == nil || *sampled != 4 {
		t.Errorf("container_samples.restart_count = %v, want 4", sampled)
	}
}

// An agent whose socket went away is no longer in a position to assert that a
// container is healthy. Keeping the last "healthy" the hub happened to hear
// would put a green badge on a container nobody can see -- the worst failure
// available here, and the same bargain name and image already make.
func TestIntegrationContainerStateIsForgottenWhenTheAgentStopsSendingIt(t *testing.T) {
	ctx := context.Background()
	s := openMigrated(t)
	id := seedInterfaceHost(t, s, "docker-meta-forget")

	at := time.Now().Add(-2 * time.Minute)
	if _, err := s.InsertContainerSamples(ctx, id, []*netrav1.ContainerSample{
		dockerSample("shop/web", at, "running", "healthy", nil, map[string]string{"a": "b"}),
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	// The socket goes away: the next scrape carries metrics and nothing else.
	if _, err := s.InsertContainerSamples(ctx, id, []*netrav1.ContainerSample{
		containerSample("shop/web", at.Add(time.Minute)),
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	var state, health *string
	var labels map[string]string
	if err := s.Pool().QueryRow(ctx,
		`SELECT docker_state, health, labels FROM containers WHERE host_id = $1`,
		id).Scan(&state, &health, &labels); err != nil {
		t.Fatalf("query: %v", err)
	}
	if state != nil {
		t.Errorf("docker_state = %q, want NULL once the agent stopped asserting it", *state)
	}
	if health != nil {
		t.Errorf("health = %q, want NULL once the agent stopped asserting it", *health)
	}
	if labels != nil {
		t.Errorf("labels = %v, want NULL once the agent stopped asserting them", labels)
	}
}

// The one exception, and it exists because unset does NOT mean "could not
// look" for this field: the agent rations inspect calls, so a perfectly
// healthy agent sends no restart count on most scrapes. Overwriting would
// blank the number nine times out of ten.
func TestIntegrationRestartCountSurvivesAScrapeThatDidNotInspect(t *testing.T) {
	ctx := context.Background()
	s := openMigrated(t)
	id := seedInterfaceHost(t, s, "docker-meta-restarts")

	at := time.Now().Add(-2 * time.Minute)
	restarts := uint64(7)
	if _, err := s.InsertContainerSamples(ctx, id, []*netrav1.ContainerSample{
		dockerSample("shop/web", at, "running", "healthy", &restarts, nil),
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := s.InsertContainerSamples(ctx, id, []*netrav1.ContainerSample{
		dockerSample("shop/web", at.Add(time.Minute), "running", "healthy", nil, nil),
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	var count *int64
	if err := s.Pool().QueryRow(ctx,
		`SELECT restart_count FROM containers WHERE host_id = $1`, id).Scan(&count); err != nil {
		t.Fatalf("query: %v", err)
	}
	if count == nil || *count != 7 {
		t.Errorf("restart_count = %v, want 7 kept across a scrape that did not inspect", count)
	}
}

// state_ts is when the state was ENTERED, not when the hub last heard about
// it -- the rule read.Unit.Since already documents for systemd. A container
// sitting in "running" for a week must not report that it entered that state
// a minute ago.
func TestIntegrationContainerStateTimestampOnlyMovesOnAChange(t *testing.T) {
	ctx := context.Background()
	s := openMigrated(t)
	id := seedInterfaceHost(t, s, "docker-meta-state-ts")

	at := time.Now().Add(-10 * time.Minute)
	if _, err := s.InsertContainerSamples(ctx, id, []*netrav1.ContainerSample{
		dockerSample("shop/web", at, "running", "healthy", nil, nil),
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	var first time.Time
	if err := s.Pool().QueryRow(ctx,
		`SELECT state_ts FROM containers WHERE host_id = $1`, id).Scan(&first); err != nil {
		t.Fatalf("query: %v", err)
	}

	// Same state, later scrape: the timestamp holds.
	if _, err := s.InsertContainerSamples(ctx, id, []*netrav1.ContainerSample{
		dockerSample("shop/web", at.Add(5*time.Minute), "running", "healthy", nil, nil),
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	var held time.Time
	if err := s.Pool().QueryRow(ctx,
		`SELECT state_ts FROM containers WHERE host_id = $1`, id).Scan(&held); err != nil {
		t.Fatalf("query: %v", err)
	}
	if !held.Equal(first) {
		t.Errorf("state_ts moved from %s to %s without the state changing", first, held)
	}

	// A different state: it advances.
	if _, err := s.InsertContainerSamples(ctx, id, []*netrav1.ContainerSample{
		dockerSample("shop/web", at.Add(8*time.Minute), "restarting", "healthy", nil, nil),
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	var moved time.Time
	if err := s.Pool().QueryRow(ctx,
		`SELECT state_ts FROM containers WHERE host_id = $1`, id).Scan(&moved); err != nil {
		t.Fatalf("query: %v", err)
	}
	if !moved.After(first) {
		t.Errorf("state_ts stayed at %s after the state changed", moved)
	}
}

// The distinction the wrapper message on the wire exists to carry: a container
// with no labels is not a container nobody could ask about. An empty map must
// reach the database as `{}` rather than as NULL, or the UI cannot tell the
// two apart and would say "not reported" about a container it read.
func TestIntegrationEmptyLabelsAreStoredAsEmptyNotNull(t *testing.T) {
	ctx := context.Background()
	s := openMigrated(t)
	id := seedInterfaceHost(t, s, "docker-meta-labels")

	at := time.Now().Add(-time.Minute)
	if _, err := s.InsertContainerSamples(ctx, id, []*netrav1.ContainerSample{
		dockerSample("shop/web", at, "running", "none", nil, map[string]string{}),
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	var labels map[string]string
	if err := s.Pool().QueryRow(ctx,
		`SELECT labels FROM containers WHERE host_id = $1`, id).Scan(&labels); err != nil {
		t.Fatalf("query: %v", err)
	}
	if labels == nil {
		t.Fatal("labels stored as NULL for a container the agent read and found no labels on")
	}
	if len(labels) != 0 {
		t.Errorf("labels = %v, want empty", labels)
	}
}
