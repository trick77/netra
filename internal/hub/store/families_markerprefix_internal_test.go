package store

import (
	"context"
	"testing"

	"google.golang.org/protobuf/proto"

	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
)

// Ingest is the only thing that holds here, now that the one-time repair
// migration is gone with the squashed schema.
//
// An agent still on the older image scrapes every 60s and keeps sending the
// bind target it measures through. Without this normalisation each scrape
// inserts a second row for a disk that already has one, so the operator sees
// one disk twice -- "/netra/fs/ark is 94 % full" beside "/mnt/ark is 94 %
// full" -- and nothing ever cleans it up, because the agent is the half
// upgraded last.
func TestIntegrationPrefixedLabelLandsOnTheExistingRow(t *testing.T) {
	ctx := context.Background()
	s := OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// Given: a host whose filesystem already carries the host-side label, with
	// the mount point a current agent taught it and the history that is the
	// whole reason its identity has to be preserved.
	var hostID int32
	if err := s.Pool().QueryRow(ctx,
		`INSERT INTO hosts (hostname) VALUES ('ingest-prefix') RETURNING id`).Scan(&hostID); err != nil {
		t.Fatalf("insert host: %v", err)
	}
	var arkID int32
	if err := s.Pool().QueryRow(ctx,
		`INSERT INTO filesystems (host_id, label, mountpoint, device_id)
		 VALUES ($1, 'ark', '/mnt/ark', 42) RETURNING id`, hostID).Scan(&arkID); err != nil {
		t.Fatalf("insert filesystem: %v", err)
	}

	// When: an agent that has not been upgraded reports the same disk under
	// its own bind target.
	n, err := s.InsertFilesystemSamples(ctx, hostID, []*netrav1.FilesystemSample{{
		TsMs:       1_700_000_000_000,
		Label:      "/netra/fs/ark",
		Mountpoint: "/netra/fs/ark",
		DeviceId:   proto.Uint64(42),
		Total:      proto.Uint64(2000),
		Used:       proto.Uint64(1880),
		Free:       proto.Uint64(120),
	}})
	if err != nil {
		t.Fatalf("InsertFilesystemSamples: %v", err)
	}
	if n != 1 {
		t.Errorf("rows written = %d, want 1", n)
	}

	// Then: no second filesystem exists for the host -- the prefix is not a
	// name this table can hold.
	var labels []string
	rows, err := s.Pool().Query(ctx,
		`SELECT label FROM filesystems WHERE host_id = $1 ORDER BY label`, hostID)
	if err != nil {
		t.Fatalf("query filesystems: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var l string
		if err := rows.Scan(&l); err != nil {
			t.Fatalf("scan label: %v", err)
		}
		labels = append(labels, l)
	}
	if len(labels) != 1 || labels[0] != "ark" {
		t.Fatalf("labels = %v, want [ark]", labels)
	}

	// And: the sample is attached to the row that carries the history, so the
	// disk graph continues rather than restarting under a second series.
	var samples int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM filesystem_samples WHERE fs_id = $1`, arkID).Scan(&samples); err != nil {
		t.Fatalf("count samples: %v", err)
	}
	if samples != 1 {
		t.Errorf("samples on the existing row = %d, want 1", samples)
	}
}

// A mount point the hub already knows is never replaced by no mount point.
//
// An agent installed before NETRA_FS_MOUNTS existed sends the label and an
// empty mount point. A bare EXCLUDED.mountpoint let that blank the /mnt/ark a
// better-informed agent had established, once per scrape, and the page fell
// back to displaying the bare label for a disk whose real name the hub was
// holding all along.
func TestIntegrationEmptyMountpointDoesNotDemoteAKnownOne(t *testing.T) {
	ctx := context.Background()
	s := OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// Given: a host whose filesystem knows the mount point it answers to.
	var hostID int32
	if err := s.Pool().QueryRow(ctx,
		`INSERT INTO hosts (hostname) VALUES ('ingest-mountpoint') RETURNING id`).Scan(&hostID); err != nil {
		t.Fatalf("insert host: %v", err)
	}
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO filesystems (host_id, label, mountpoint, device_id)
		 VALUES ($1, 'ark', '/mnt/ark', 42)`, hostID); err != nil {
		t.Fatalf("insert filesystem: %v", err)
	}

	// When: an agent with no mapping yet reports the same filesystem.
	if _, err := s.InsertFilesystemSamples(ctx, hostID, []*netrav1.FilesystemSample{{
		TsMs:     1_700_000_000_000,
		Label:    "ark",
		DeviceId: proto.Uint64(42),
		Total:    proto.Uint64(2000),
		Used:     proto.Uint64(1880),
		Free:     proto.Uint64(120),
	}}); err != nil {
		t.Fatalf("InsertFilesystemSamples: %v", err)
	}

	// Then: the mount point survives.
	var mountpoint string
	if err := s.Pool().QueryRow(ctx,
		`SELECT mountpoint FROM filesystems WHERE host_id = $1 AND label = 'ark'`,
		hostID).Scan(&mountpoint); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if mountpoint != "/mnt/ark" {
		t.Errorf("mountpoint = %q, want %q", mountpoint, "/mnt/ark")
	}
}

// One batch, both names, one series.
//
// A hub can be handed both spellings at once -- a replayed ring buffer written
// either side of an agent upgrade. Resolving them separately would queue two
// sample rows for the same instant against the same fs_id; the primary key on
// (host_id, ts, fs_id) absorbs that, and this pins that the second name does
// not become a second filesystem on the way there.
func TestIntegrationBothNamesInOneBatchResolveToOneFilesystem(t *testing.T) {
	ctx := context.Background()
	s := OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	var hostID int32
	if err := s.Pool().QueryRow(ctx,
		`INSERT INTO hosts (hostname) VALUES ('ingest-bothnames') RETURNING id`).Scan(&hostID); err != nil {
		t.Fatalf("insert host: %v", err)
	}

	if _, err := s.InsertFilesystemSamples(ctx, hostID, []*netrav1.FilesystemSample{
		{TsMs: 1_700_000_000_000, Label: "/netra/fs/ark", Mountpoint: "/netra/fs/ark",
			Total: proto.Uint64(2000), Used: proto.Uint64(1880), Free: proto.Uint64(120)},
		{TsMs: 1_700_000_060_000, Label: "ark", Mountpoint: "/mnt/ark",
			Total: proto.Uint64(2000), Used: proto.Uint64(1885), Free: proto.Uint64(115)},
	}); err != nil {
		t.Fatalf("InsertFilesystemSamples: %v", err)
	}

	var count int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM filesystems WHERE host_id = $1`, hostID).Scan(&count); err != nil {
		t.Fatalf("count filesystems: %v", err)
	}
	if count != 1 {
		t.Errorf("filesystems = %d, want 1", count)
	}

	// The later row wins the mount point, as any upsert would: it is the one
	// spelling of the two that names something on the host.
	var label, mountpoint string
	if err := s.Pool().QueryRow(ctx,
		`SELECT label, mountpoint FROM filesystems WHERE host_id = $1`,
		hostID).Scan(&label, &mountpoint); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if label != "ark" || mountpoint != "/mnt/ark" {
		t.Errorf("label/mountpoint = %q/%q, want %q/%q", label, mountpoint, "ark", "/mnt/ark")
	}
}
