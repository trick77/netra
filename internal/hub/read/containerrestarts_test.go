package read_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/trick77/netra/internal/hub/read"
)

// The restart figures that ride the container listing row. They are what the
// fleet list prints beside a container's name, and they are SUMMED from the
// event log rather than differenced from a counter -- see migration 0019.

// seedRestart writes one restart event straight into the log, so these tests
// pin the READ rather than re-testing the ingest derivation.
func seedRestart(t *testing.T, pool *pgxpool.Pool, hostID int32, subject string, at time.Time, delta int) {
	t.Helper()
	exec(t, pool, `
		INSERT INTO events (host_id, ts, type, subject, detail, severity)
		VALUES ($1, $2, 'container_restart', $3,
		        jsonb_build_object('delta', $4::bigint, 'ts_source', 'observed'), 'warning')`,
		hostID, at, subject, delta)
}

func TestIntegrationContainersReportRestartsInTheWindow(t *testing.T) {
	ctx := context.Background()
	svc, pool := newService(t)
	id := seedHost(t, pool, "restarts-window")

	exec(t, pool, `
		INSERT INTO containers (host_id, container_key, name, last_seen)
		VALUES ($1, 'shop/web', 'web-1', now()), ($1, 'shop/api', 'api-1', now())`, id)

	now := time.Now()
	// One event carrying three restarts, and one carrying one. Counting rows
	// would report 2; the reading is 4.
	seedRestart(t, pool, id, "shop/web", now.Add(-2*time.Hour), 3)
	seedRestart(t, pool, id, "shop/web", now.Add(-30*time.Minute), 1)
	// Older than the window, so it must not be counted.
	seedRestart(t, pool, id, "shop/web", now.Add(-72*time.Hour), 5)
	// A different container's restart must not leak across.
	seedRestart(t, pool, id, "shop/api", now.Add(-time.Hour), 2)

	got, err := svc.Containers(ctx, id)
	if err != nil {
		t.Fatalf("Containers: %v", err)
	}

	byKey := map[string]read.Container{}
	for _, c := range got {
		byKey[c.Key] = c
	}

	web, ok := byKey["shop/web"]
	if !ok {
		t.Fatal("shop/web missing from the listing")
	}
	if web.RestartsWindow != 4 {
		t.Errorf("restarts_window = %d, want 4 (3 + 1, and never the row count)", web.RestartsWindow)
	}
	if web.LastRestart == nil {
		t.Error("last_restart is null; there were restarts in the window")
	}
	if web.RestartsWindowSeconds != int64(read.RestartWindow.Seconds()) {
		t.Errorf("restarts_window_seconds = %d, want %d",
			web.RestartsWindowSeconds, int64(read.RestartWindow.Seconds()))
	}

	api := byKey["shop/api"]
	if api.RestartsWindow != 2 {
		t.Errorf("shop/api restarts_window = %d, want 2 -- one container's log must not reach another",
			api.RestartsWindow)
	}
}

// Zero is a real answer: the log was read and there were none. It must not be
// confused with restart_count being null, which is nobody having looked.
func TestIntegrationAQuietContainerReportsZeroRestartsNotNull(t *testing.T) {
	ctx := context.Background()
	svc, pool := newService(t)
	id := seedHost(t, pool, "restarts-quiet")

	exec(t, pool, `
		INSERT INTO containers (host_id, container_key, name, last_seen)
		VALUES ($1, 'shop/quiet', 'quiet-1', now())`, id)

	got, err := svc.Containers(ctx, id)
	if err != nil {
		t.Fatalf("Containers: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("listed %d containers, want 1", len(got))
	}
	if got[0].RestartsWindow != 0 || got[0].RecreatesWindow != 0 {
		t.Errorf("restarts = %d, recreates = %d, want 0 and 0",
			got[0].RestartsWindow, got[0].RecreatesWindow)
	}
	if got[0].LastRestart != nil {
		t.Errorf("last_restart = %v, want null for a container that never restarted", got[0].LastRestart)
	}
}

// Redeploys are counted apart from faults: mixing them would make every deploy
// look like an incident.
func TestIntegrationRecreatesAreCountedApartFromRestarts(t *testing.T) {
	ctx := context.Background()
	svc, pool := newService(t)
	id := seedHost(t, pool, "restarts-recreate")

	exec(t, pool, `
		INSERT INTO containers (host_id, container_key, name, last_seen)
		VALUES ($1, 'web/caddy', 'caddy-1', now())`, id)

	now := time.Now()
	seedRestart(t, pool, id, "web/caddy", now.Add(-time.Hour), 2)
	exec(t, pool, `
		INSERT INTO events (host_id, ts, type, subject, detail, severity)
		VALUES ($1, $2, 'container_recreate', 'web/caddy',
		        '{"delta":0,"ts_source":"observed"}'::jsonb, 'info')`,
		id, now.Add(-90*time.Minute))

	got, err := svc.Containers(ctx, id)
	if err != nil {
		t.Fatalf("Containers: %v", err)
	}
	if got[0].RestartsWindow != 2 {
		t.Errorf("restarts_window = %d, want 2", got[0].RestartsWindow)
	}
	if got[0].RecreatesWindow != 1 {
		t.Errorf("recreates_window = %d, want 1", got[0].RecreatesWindow)
	}
}

// The fleet answer carries the same figures, so the fleet list and a host page
// cannot disagree about one container.
func TestIntegrationFleetContainersReportRestartsPerHost(t *testing.T) {
	ctx := context.Background()
	svc, pool := newService(t)
	one := seedHost(t, pool, "fleet-restarts-1")
	two := seedHost(t, pool, "fleet-restarts-2")

	for _, id := range []int32{one, two} {
		exec(t, pool, `
			INSERT INTO containers (host_id, container_key, name, last_seen)
			VALUES ($1, 'shop/web', 'web-1', now())`, id)
	}
	now := time.Now()
	seedRestart(t, pool, one, "shop/web", now.Add(-time.Hour), 3)

	res, err := svc.FleetContainers(ctx, []int32{one, two})
	if err != nil {
		t.Fatalf("FleetContainers: %v", err)
	}
	if len(res.Hosts) != 2 {
		t.Fatalf("hosts = %d, want 2", len(res.Hosts))
	}
	if got := res.Hosts[0].Containers[0].RestartsWindow; got != 3 {
		t.Errorf("host one restarts = %d, want 3", got)
	}
	// Two hosts can run the same container_key, and the log is scoped by host.
	if got := res.Hosts[1].Containers[0].RestartsWindow; got != 0 {
		t.Errorf("host two restarts = %d, want 0 -- the key is shared, the log is not", got)
	}
}
