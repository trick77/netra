package store_test

import (
	"context"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/trick77/netra/internal/hub/store"
	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
)

// filesystem_current is the LAST reading for a mount, kept out of the
// hypertable and out of every retention policy, so a host that is switched off
// still has a disk figure to show. 0013_filesystem_current.sql carries the
// argument; these pin the write rule.

// fsSample is one filesystem reading, with the three byte columns the
// collector always sets together or not at all.
func fsSample(at time.Time, label, mount string, total, used, free uint64) *netrav1.FilesystemSample {
	return &netrav1.FilesystemSample{
		TsMs:       at.UnixMilli(),
		Label:      label,
		Mountpoint: mount,
		Total:      proto.Uint64(total),
		Used:       proto.Uint64(used),
		Free:       proto.Uint64(free),
	}
}

// currentByLabel reads the gauge for one mount, joined through filesystems so
// the test names the mount the way the agent does.
func currentByLabel(t *testing.T, s *store.Store, hostID int32, label string) (time.Time, *int64, *int64, *int64) {
	t.Helper()
	var ts time.Time
	var total, used, free *int64
	if err := s.Pool().QueryRow(context.Background(), `
		SELECT c.ts, c.total, c.used, c.free
		  FROM filesystem_current c
		  JOIN filesystems f ON f.id = c.fs_id AND f.host_id = c.host_id
		 WHERE c.host_id = $1 AND f.label = $2`, hostID, label).
		Scan(&ts, &total, &used, &free); err != nil {
		t.Fatalf("read filesystem_current for %s: %v", label, err)
	}
	return ts, total, used, free
}

func TestIntegrationFilesystemCurrentHoldsTheNewestReading(t *testing.T) {
	ctx := context.Background()
	s := openMigrated(t)
	id := seedInterfaceHost(t, s, "fs-current")

	// Given: two scrapes of the same mount, and one scrape of another.
	first := time.Now().Add(-2 * time.Minute).Truncate(time.Millisecond)
	second := first.Add(time.Minute)
	if _, err := s.InsertFilesystemSamples(ctx, id, []*netrav1.FilesystemSample{
		fsSample(first, "/", "/", 1000, 400, 560),
		fsSample(second, "/", "/", 1000, 410, 550),
		fsSample(second, "pool", "/srv/pool", 8000, 7000, 900),
	}); err != nil {
		t.Fatalf("InsertFilesystemSamples: %v", err)
	}

	// When/Then: one row per mount, carrying the newest reading.
	ts, total, used, free := currentByLabel(t, s, id, "/")
	if !ts.Equal(second) {
		t.Errorf("ts = %v, want %v", ts, second)
	}
	if *total != 1000 || *used != 410 || *free != 550 {
		t.Errorf("bytes = %d/%d/%d, want 1000/410/550", *total, *used, *free)
	}
	if _, _, used, _ = currentByLabel(t, s, id, "pool"); *used != 7000 {
		t.Errorf("pool used = %d, want 7000", *used)
	}

	var rows int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM filesystem_current WHERE host_id = $1`, id).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 2 {
		t.Errorf("filesystem_current rows = %d, want 2", rows)
	}
}

// An agent buffers scrapes while the hub is down and replays them afterwards,
// in whatever order the batches land. A gauge that took whichever row arrived
// last would walk backwards.
func TestIntegrationFilesystemCurrentDoesNotWalkBackwards(t *testing.T) {
	ctx := context.Background()
	s := openMigrated(t)
	id := seedInterfaceHost(t, s, "fs-replay")

	// Given: the newest reading has landed.
	newest := time.Now().Truncate(time.Millisecond)
	if _, err := s.InsertFilesystemSamples(ctx, id, []*netrav1.FilesystemSample{
		fsSample(newest, "/", "/", 1000, 410, 550),
	}); err != nil {
		t.Fatalf("insert newest: %v", err)
	}

	// When: a buffered older batch replays after it.
	if _, err := s.InsertFilesystemSamples(ctx, id, []*netrav1.FilesystemSample{
		fsSample(newest.Add(-time.Hour), "/", "/", 1000, 100, 860),
	}); err != nil {
		t.Fatalf("insert replay: %v", err)
	}

	// Then: the gauge is untouched.
	ts, _, used, _ := currentByLabel(t, s, id, "/")
	if !ts.Equal(newest) {
		t.Errorf("ts = %v, want %v", ts, newest)
	}
	if *used != 410 {
		t.Errorf("used = %d, want 410 -- the older replay overwrote the gauge", *used)
	}
}

// resolveFilesystemIDs collapses the marker-prefixed spelling and the bare one
// onto a single filesystem row, so the gauge has to be keyed on the RESOLVED
// id: keyed on the label as sent it would pick one spelling and could hand the
// gauge the older of two readings for the same disk.
func TestIntegrationFilesystemCurrentIsKeyedOnTheResolvedFilesystem(t *testing.T) {
	ctx := context.Background()
	s := openMigrated(t)
	id := seedInterfaceHost(t, s, "fs-bothnames")

	// Given: both spellings in one batch, the BARE one newer.
	older := time.Now().Add(-time.Minute).Truncate(time.Millisecond)
	newer := older.Add(time.Minute)
	if _, err := s.InsertFilesystemSamples(ctx, id, []*netrav1.FilesystemSample{
		fsSample(older, "/netra/fs/ark", "/netra/fs/ark", 2000, 1880, 120),
		fsSample(newer, "ark", "/mnt/ark", 2000, 1885, 115),
	}); err != nil {
		t.Fatalf("InsertFilesystemSamples: %v", err)
	}

	// Then: one row, carrying the newer reading.
	var rows int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM filesystem_current WHERE host_id = $1`, id).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 1 {
		t.Fatalf("filesystem_current rows = %d, want 1", rows)
	}
	ts, _, used, _ := currentByLabel(t, s, id, "ark")
	if !ts.Equal(newer) {
		t.Errorf("ts = %v, want %v", ts, newer)
	}
	if *used != 1885 {
		t.Errorf("used = %d, want 1885", *used)
	}
}

// A filesystem the agent could not measure has no value, and NULL is exactly
// that. Carrying the previous bytes forward under a fresh timestamp would be
// the frozen reading this table exists to avoid.
func TestIntegrationFilesystemCurrentStoresUnmeasuredAsNull(t *testing.T) {
	ctx := context.Background()
	s := openMigrated(t)
	id := seedInterfaceHost(t, s, "fs-null")

	// Given: a measured reading.
	at := time.Now().Add(-time.Minute).Truncate(time.Millisecond)
	if _, err := s.InsertFilesystemSamples(ctx, id, []*netrav1.FilesystemSample{
		fsSample(at, "/", "/", 1000, 410, 550),
	}); err != nil {
		t.Fatalf("insert measured: %v", err)
	}

	// When: a later scrape carries the mount with no bytes.
	if _, err := s.InsertFilesystemSamples(ctx, id, []*netrav1.FilesystemSample{
		{TsMs: at.Add(time.Minute).UnixMilli(), Label: "/", Mountpoint: "/"},
	}); err != nil {
		t.Fatalf("insert unmeasured: %v", err)
	}

	// Then: NULL, not the previous figures.
	_, total, used, free := currentByLabel(t, s, id, "/")
	if total != nil || used != nil || free != nil {
		t.Errorf("bytes = %v/%v/%v, want all null", total, used, free)
	}
}

func TestIntegrationFilesystemCurrentCascadesWithTheHost(t *testing.T) {
	ctx := context.Background()
	s := openMigrated(t)
	id := seedInterfaceHost(t, s, "fs-cascade")

	if _, err := s.InsertFilesystemSamples(ctx, id, []*netrav1.FilesystemSample{
		fsSample(time.Now(), "/", "/", 1000, 410, 550),
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, `DELETE FROM hosts WHERE id = $1`, id); err != nil {
		t.Fatalf("delete host: %v", err)
	}

	var rows int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM filesystem_current WHERE host_id = $1`, id).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 0 {
		t.Errorf("filesystem_current rows = %d after host delete, want 0", rows)
	}
}
