package store

import (
	"context"
	"testing"

	"google.golang.org/protobuf/proto"

	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
)

// An agent installed before AGENT_FS_MOUNTS existed sends the label and an
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
