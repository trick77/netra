package read_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/trick77/netra/internal/hub/read"
)

// The disk gauge, and the one trap around it.
//
// The fleet's Disk cell reads its figure from filesystem_current rather than
// from the last slot of the answered metrics window, so a host that is
// switched off still has a figure to show. That means the gauge has to reach
// BOTH the host list and the host detail: HostDetail embeds HostSummary, so a
// column selected on one side only comes back as a confident empty array on
// the other. filesystemsLateral is one string for exactly that reason, and
// the second test here is what proves it.

// seedMount registers a filesystem and, when at is non-nil, its last reading.
func seedMount(t *testing.T, pool *pgxpool.Pool, hostID int32, label, mount string, at *time.Time, used, free int64) int32 {
	t.Helper()

	var fsID int32
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO filesystems (host_id, label, mountpoint)
		VALUES ($1, $2, $3) RETURNING id`, hostID, label, mount).Scan(&fsID); err != nil {
		t.Fatalf("insert filesystem %q: %v", label, err)
	}
	if at != nil {
		exec(t, pool, `
			INSERT INTO filesystem_current (host_id, fs_id, ts, total, used, free)
			VALUES ($1, $2, $3, $4, $5, $6)`,
			hostID, fsID, *at, used+free, used, free)
	}
	return fsID
}

// byLabel finds one mount in a response, so a test names it rather than
// indexing into an order.
func byLabel(rows []read.Filesystem, label string) *read.Filesystem {
	for i := range rows {
		if rows[i].Label == label {
			return &rows[i]
		}
	}
	return nil
}

// The inventory endpoint lists what the host HAS, so a mount that has never
// reported bytes still belongs in it -- with nulls, which say "netra has never
// had a figure for this" rather than "this disk is empty".
func TestIntegrationFilesystemsCarryTheirLastReading(t *testing.T) {
	ctx := context.Background()
	svc, pool := newService(t)
	id := seedHost(t, pool, "ark")

	at := time.Now().Add(-3 * 24 * time.Hour).UTC().Truncate(time.Second)
	seedMount(t, pool, id, "pool", "/srv/pool", &at, 7000, 1000)
	seedMount(t, pool, id, "unread", "/mnt/unread", nil, 0, 0)

	got, err := svc.Filesystems(ctx, id)
	if err != nil {
		t.Fatalf("Filesystems: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("filesystems = %d, want 2", len(got))
	}

	pool2 := byLabel(got, "pool")
	if pool2 == nil || pool2.TS == nil {
		t.Fatalf("pool = %+v, want a reading", pool2)
	}
	if !pool2.TS.Equal(at) {
		t.Errorf("pool ts = %v, want %v", *pool2.TS, at)
	}
	if *pool2.Used != 7000 || *pool2.Free != 1000 || *pool2.Total != 8000 {
		t.Errorf("pool bytes = %d/%d/%d, want 8000/7000/1000",
			*pool2.Total, *pool2.Used, *pool2.Free)
	}

	unread := byLabel(got, "unread")
	if unread == nil {
		t.Fatalf("the mount with no reading is missing from the inventory")
	}
	if unread.TS != nil || unread.Used != nil || unread.Free != nil {
		t.Errorf("unread = %+v, want nulls -- netra has never had a figure", unread)
	}
}

// The embed trap. A host page and a fleet row must not disagree about the same
// machine's disks.
func TestIntegrationHostListAndDetailCarryTheSameFilesystems(t *testing.T) {
	ctx := context.Background()
	svc, pool := newService(t)
	id := seedHost(t, pool, "nas")

	at := time.Now().Add(-3 * 24 * time.Hour).UTC().Truncate(time.Second)
	seedMount(t, pool, id, "pool", "/srv/pool", &at, 7000, 1000)
	// Never reported bytes: it is inventory, not a reading, so it must NOT
	// inflate the "+N others" the fleet cell prints beside the mount it names.
	seedMount(t, pool, id, "unread", "/mnt/unread", nil, 0, 0)

	list, err := svc.ListHosts(ctx)
	if err != nil {
		t.Fatalf("ListHosts: %v", err)
	}
	var summary *read.HostSummary
	for i := range list {
		if list[i].ID == id {
			summary = &list[i]
		}
	}
	if summary == nil {
		t.Fatalf("host %d missing from the list", id)
	}

	detail, err := svc.Host(ctx, id)
	if err != nil {
		t.Fatalf("Host: %v", err)
	}

	for _, tc := range []struct {
		side string
		rows []read.Filesystem
	}{{"list", summary.Filesystems}, {"detail", detail.Filesystems}} {
		if len(tc.rows) != 1 {
			t.Errorf("%s filesystems = %d, want 1 -- only the mount that reported bytes",
				tc.side, len(tc.rows))
			continue
		}
		got := tc.rows[0]
		if got.Label != "pool" || got.Mountpoint == nil || *got.Mountpoint != "/srv/pool" {
			t.Errorf("%s mount = %+v, want pool at /srv/pool", tc.side, got)
		}
		if got.TS == nil || !got.TS.Equal(at) {
			t.Errorf("%s ts = %v, want %v", tc.side, got.TS, at)
		}
		if got.Used == nil || *got.Used != 7000 || got.Free == nil || *got.Free != 1000 {
			t.Errorf("%s bytes = %v/%v, want 7000/1000", tc.side, got.Used, got.Free)
		}
	}
}

// Empty rather than null: "asked, and this host has no reporting mounts" is
// not the same fact as "not asked", and the UI falls back to its old
// window-derived reading on the second one.
func TestIntegrationHostWithNoReportingMountsCarriesAnEmptyList(t *testing.T) {
	ctx := context.Background()
	svc, pool := newService(t)
	id := seedHost(t, pool, "diskless")

	list, err := svc.ListHosts(ctx)
	if err != nil {
		t.Fatalf("ListHosts: %v", err)
	}
	for _, h := range list {
		if h.ID == id && h.Filesystems == nil {
			t.Error("list filesystems = null, want an empty array")
		}
	}

	detail, err := svc.Host(ctx, id)
	if err != nil {
		t.Fatalf("Host: %v", err)
	}
	if detail.Filesystems == nil {
		t.Error("detail filesystems = null, want an empty array")
	}
}
