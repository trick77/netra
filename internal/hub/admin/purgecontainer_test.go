package admin_test

import (
	"context"
	"errors"
	"testing"

	"github.com/trick77/netra/internal/hub/admin"
)

// Purging a container takes its restart log with it.
//
// Nothing enforces this: events are keyed on (host_id, subject) as TEXT rather
// than by a foreign key to containers, so container_samples' ON DELETE CASCADE
// leaves them standing. Without the explicit delete an operator who
// deliberately made a container disappear would keep reading "restarted" lines
// about it in the fleet log for the ninety days the prune keeps.
func TestIntegrationPurgingAContainerRemovesItsRestartEvents(t *testing.T) {
	svc, s := newService(t)
	ctx := context.Background()

	host, _, err := svc.CreateHost(ctx, "purge-restarts")
	if err != nil {
		t.Fatalf("CreateHost: %v", err)
	}

	var containerID int32
	if err := s.Pool().QueryRow(ctx, `
		INSERT INTO containers (host_id, container_key, name, last_seen)
		VALUES ($1, 'shop/web', 'web-1', now()) RETURNING id`, host.ID).
		Scan(&containerID); err != nil {
		t.Fatalf("seed container: %v", err)
	}
	// One restart of this container, and one belonging to a different one that
	// must survive: the delete is scoped by subject, not by host.
	for _, subject := range []string{"shop/web", "shop/api"} {
		if _, err := s.Pool().Exec(ctx, `
			INSERT INTO events (host_id, ts, type, subject, detail, severity)
			VALUES ($1, now(), 'container_restart', $2, '{"delta":1}'::jsonb, 'warning')`,
			host.ID, subject); err != nil {
			t.Fatalf("seed event %s: %v", subject, err)
		}
	}

	if err := svc.DeleteContainer(ctx, host.ID, containerID); err != nil {
		t.Fatalf("DeleteContainer: %v", err)
	}

	rows, err := s.Pool().Query(ctx,
		`SELECT subject FROM events WHERE host_id = $1 AND type = 'container_restart'`, host.ID)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()

	var remaining []string
	for rows.Next() {
		var subject string
		if err := rows.Scan(&subject); err != nil {
			t.Fatalf("scan: %v", err)
		}
		remaining = append(remaining, subject)
	}
	if len(remaining) != 1 || remaining[0] != "shop/api" {
		t.Errorf("remaining restart events = %v, want only shop/api", remaining)
	}
}

// A container with no restarts still deletes.
//
// The statement's RowsAffected now counts EVENT rows, so reading it the way the
// old single DELETE did would report ErrNotFound for a container that was
// successfully removed -- which is why the existence check reads the CTE's own
// count instead.
func TestIntegrationPurgingAContainerWithNoRestartsSucceeds(t *testing.T) {
	svc, s := newService(t)
	ctx := context.Background()

	host, _, err := svc.CreateHost(ctx, "purge-quiet")
	if err != nil {
		t.Fatalf("CreateHost: %v", err)
	}
	var containerID int32
	if err := s.Pool().QueryRow(ctx, `
		INSERT INTO containers (host_id, container_key, name, last_seen)
		VALUES ($1, 'shop/quiet', 'quiet-1', now()) RETURNING id`, host.ID).
		Scan(&containerID); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := svc.DeleteContainer(ctx, host.ID, containerID); err != nil {
		t.Fatalf("DeleteContainer on a container with no restarts: %v", err)
	}

	var left int64
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM containers WHERE id = $1`, containerID).Scan(&left); err != nil {
		t.Fatalf("query: %v", err)
	}
	if left != 0 {
		t.Error("the container is still there after a successful purge")
	}
}

// And a container that was never there is still ErrNotFound.
func TestIntegrationPurgingAMissingContainerIsNotFound(t *testing.T) {
	svc, _ := newService(t)
	ctx := context.Background()

	host, _, err := svc.CreateHost(ctx, "purge-missing")
	if err != nil {
		t.Fatalf("CreateHost: %v", err)
	}
	if err := svc.DeleteContainer(ctx, host.ID, 999999); !errors.Is(err, admin.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}
