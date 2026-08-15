package store

import (
	"context"
	"testing"
)

// applyMarkerPrefixMigration re-runs 0002 against rows seeded after Migrate has
// already recorded it. The statements are idempotent by construction -- they
// match on a prefix they then remove -- so running them a second time is the
// only way to test them against data an old agent would have written.
func applyMarkerPrefixMigration(t *testing.T, ctx context.Context, s *Store) {
	t.Helper()
	sql, err := migrationFS.ReadFile("migrations/0002_strip_marker_prefix.sql")
	if err != nil {
		t.Fatalf("read 0002: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, string(sql)); err != nil {
		t.Fatalf("apply 0002: %v", err)
	}
}

// The rename must keep fs_id, because fs_id is what every sample and both
// rollups are keyed by. A new row would be a new series, and every disk graph
// on every existing install would restart from empty.
func TestIntegrationMarkerPrefixIsStrippedInPlace(t *testing.T) {
	ctx := context.Background()
	s := OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// Given: a host whose filesystems were named by an agent that reported its
	// own bind targets, with a sample against one of them.
	var hostID int32
	if err := s.Pool().QueryRow(ctx,
		`INSERT INTO hosts (hostname) VALUES ('marker-prefix') RETURNING id`).Scan(&hostID); err != nil {
		t.Fatalf("insert host: %v", err)
	}
	var arkID int32
	if err := s.Pool().QueryRow(ctx,
		`INSERT INTO filesystems (host_id, label, mountpoint, device_id)
		 VALUES ($1, '/netra/fs/ark', '/netra/fs/ark', 42) RETURNING id`, hostID).Scan(&arkID); err != nil {
		t.Fatalf("insert filesystem: %v", err)
	}
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO filesystem_samples (host_id, ts, fs_id, total, used, free)
		 VALUES ($1, now(), $2, 2000, 1900, 100)`, hostID, arkID); err != nil {
		t.Fatalf("insert sample: %v", err)
	}

	// When: the migration runs.
	applyMarkerPrefixMigration(t, ctx, s)

	// Then: the row is renamed, not replaced -- exactly "ark", never "rk" from
	// an off-by-one offset, and never the container path in either column.
	var label, mountpoint string
	if err := s.Pool().QueryRow(ctx,
		`SELECT label, mountpoint FROM filesystems WHERE id = $1`, arkID).Scan(&label, &mountpoint); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if label != "ark" {
		t.Errorf("label = %q, want %q", label, "ark")
	}
	if mountpoint != "ark" {
		t.Errorf("mountpoint = %q, want %q until the agent reports the real one", mountpoint, "ark")
	}

	// And: the history is still attached to it.
	var samples int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM filesystem_samples WHERE fs_id = $1`, arkID).Scan(&samples); err != nil {
		t.Fatalf("count samples: %v", err)
	}
	if samples != 1 {
		t.Errorf("samples on the renamed filesystem = %d, want 1", samples)
	}
}

// filesystems is unique on (host_id, label), so a host that already carries the
// stripped name would make the rename violate the index and fail the whole
// migration -- taking the hub's startup with it.
func TestIntegrationMarkerPrefixCollisionDropsThePrefixedRow(t *testing.T) {
	ctx := context.Background()
	s := OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// Given: one host holding both names for the same filesystem, and another
	// host holding only the prefixed one -- the collision must be scoped per
	// host, not applied to every row that shares a label.
	var hostA, hostB int32
	if err := s.Pool().QueryRow(ctx,
		`INSERT INTO hosts (hostname) VALUES ('marker-collision-a') RETURNING id`).Scan(&hostA); err != nil {
		t.Fatalf("insert host a: %v", err)
	}
	if err := s.Pool().QueryRow(ctx,
		`INSERT INTO hosts (hostname) VALUES ('marker-collision-b') RETURNING id`).Scan(&hostB); err != nil {
		t.Fatalf("insert host b: %v", err)
	}
	var survivorID, prefixedID int32
	if err := s.Pool().QueryRow(ctx,
		`INSERT INTO filesystems (host_id, label, mountpoint) VALUES ($1, 'ark', '/mnt/ark')
		 RETURNING id`, hostA).Scan(&survivorID); err != nil {
		t.Fatalf("insert survivor: %v", err)
	}
	if err := s.Pool().QueryRow(ctx,
		`INSERT INTO filesystems (host_id, label, mountpoint) VALUES ($1, '/netra/fs/ark', '/netra/fs/ark')
		 RETURNING id`, hostA).Scan(&prefixedID); err != nil {
		t.Fatalf("insert prefixed: %v", err)
	}
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO filesystems (host_id, label, mountpoint) VALUES ($1, '/netra/fs/ark', '/netra/fs/ark')`,
		hostB); err != nil {
		t.Fatalf("insert host b filesystem: %v", err)
	}

	// When: the migration runs.
	applyMarkerPrefixMigration(t, ctx, s)

	// Then: host A keeps the row that already had the right name, and the
	// duplicate is gone rather than merged -- repointing its samples would
	// collide on PRIMARY KEY (host_id, ts, fs_id).
	var gone bool
	if err := s.Pool().QueryRow(ctx,
		`SELECT NOT EXISTS (SELECT 1 FROM filesystems WHERE id = $1)`, prefixedID).Scan(&gone); err != nil {
		t.Fatalf("check prefixed row: %v", err)
	}
	if !gone {
		t.Error("the prefixed duplicate survived, so the rename would have hit the unique index")
	}
	var mountpoint string
	if err := s.Pool().QueryRow(ctx,
		`SELECT mountpoint FROM filesystems WHERE id = $1`, survivorID).Scan(&mountpoint); err != nil {
		t.Fatalf("read survivor: %v", err)
	}
	if mountpoint != "/mnt/ark" {
		t.Errorf("survivor mountpoint = %q, want /mnt/ark untouched", mountpoint)
	}

	// And: host B, which had no collision, is renamed rather than deleted.
	var labelB string
	if err := s.Pool().QueryRow(ctx,
		`SELECT label FROM filesystems WHERE host_id = $1`, hostB).Scan(&labelB); err != nil {
		t.Fatalf("read host b: %v", err)
	}
	if labelB != "ark" {
		t.Errorf("host b label = %q, want ark", labelB)
	}
}

// Nothing that leaves the hub may carry the agent's container path. The whole
// bug was an operator being shown "/netra/fs/ark is 94 % full" for a host with
// no netra anywhere on it.
func TestIntegrationNoFilesystemKeepsTheContainerPath(t *testing.T) {
	ctx := context.Background()
	s := OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	var hostID int32
	if err := s.Pool().QueryRow(ctx,
		`INSERT INTO hosts (hostname) VALUES ('marker-sweep') RETURNING id`).Scan(&hostID); err != nil {
		t.Fatalf("insert host: %v", err)
	}
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO filesystems (host_id, label, mountpoint) VALUES
		 ($1, '/netra/fs/root', '/netra/fs/root'),
		 ($1, '/netra/fs/var-log', '/netra/fs/var-log'),
		 ($1, '/netra/fs/fs8-1', NULL)`, hostID); err != nil {
		t.Fatalf("insert filesystems: %v", err)
	}

	applyMarkerPrefixMigration(t, ctx, s)

	var left int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM filesystems
		  WHERE label LIKE '/netra/%' OR mountpoint LIKE '/netra/%'`).Scan(&left); err != nil {
		t.Fatalf("count: %v", err)
	}
	if left != 0 {
		t.Errorf("%d filesystems still carry the container path", left)
	}
}
