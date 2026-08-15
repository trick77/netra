package collector_test

import (
	"context"
	"strings"
	"testing"

	"github.com/trick77/netra/internal/agent/collector"
)

// The container-internal marker path must never reach the hub.
//
// setup-agent.sh bind-mounts one empty .netra marker per host filesystem to
// /netra/fs/<label>. statfs on that target reports the host filesystem, but the
// path itself names nothing on the host -- an operator reading "/netra/fs/ark
// is 94 % full" is being shown the inside of the agent. The label is the
// marker's own name; the mountpoint is what the host calls the filesystem.
func TestFilesystemsReportsHostNamesForMarkerMounts(t *testing.T) {
	// Given: a containerised mount table, and the mapping setup rendered.
	testee := collector.NewFilesystems("testdata/mounts-markers", map[string]string{
		"root":    "/",
		"ark":     "/mnt/ark",
		"var-log": "/var/log",
		"backup":  "/mnt/backup",
	}, fakeStatfs(map[string]collector.FsStat{
		"/netra/fs/root":    {Total: 1000, Free: 400, Used: 600, DeviceID: 1},
		"/netra/fs/ark":     {Total: 2000, Free: 100, Used: 1900, DeviceID: 2},
		"/netra/fs/var-log": {Total: 500, Free: 250, Used: 250, DeviceID: 3},
		"/netra/fs/backup":  {Total: 9000, Free: 4000, Used: 5000, DeviceID: 5},
		"/":                 {Total: 60, Free: 10, Used: 50, DeviceID: 99},
		"/run":              {Total: 500, Free: 250, Used: 250, DeviceID: 4},
		"/etc/hostname":     {Total: 1000, Free: 400, Used: 600, DeviceID: 1},
	}))

	// When: it is collected.
	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	// Then: four rows, named the way the host names them.
	if len(res.Filesystems) != 4 {
		var got []string
		for _, r := range res.Filesystems {
			got = append(got, r.GetLabel())
		}
		t.Fatalf("labels = %v, want exactly the four marker filesystems", got)
	}
	ark := fsRow(t, res.Filesystems, "ark")
	if got := ark.GetMountpoint(); got != "/mnt/ark" {
		t.Errorf("ark mountpoint = %q, want /mnt/ark", got)
	}
	if got := ark.GetUsed(); got != 1900 {
		t.Errorf("ark used = %d, want 1900 -- statfs still reads the marker target", got)
	}
	if got := fsRow(t, res.Filesystems, "root").GetMountpoint(); got != "/" {
		t.Errorf("root mountpoint = %q, want /", got)
	}

	// And: the nfs4 marker is reported. Its /proc/mounts line carries the
	// underlying filesystem's type, but a marker only exists because setup
	// accepted the mount -- an operator who passed --include-network-fs was
	// told it would be measured, and dropping it here on the fstype would
	// second-guess a decision this collector cannot see the inputs to.
	backup := fsRow(t, res.Filesystems, "backup")
	if got := backup.GetMountpoint(); got != "/mnt/backup" {
		t.Errorf("backup mountpoint = %q, want /mnt/backup", got)
	}

	// And: nothing carries the container-internal prefix, in either field.
	for _, r := range res.Filesystems {
		if strings.HasPrefix(r.GetLabel(), "/netra/") || strings.HasPrefix(r.GetMountpoint(), "/netra/") {
			t.Errorf("row leaks the container path: label=%q mountpoint=%q",
				r.GetLabel(), r.GetMountpoint())
		}
	}
}

// The agent container's own overlay root is not a host filesystem. It carries
// the size of Docker's storage driver, and its anonymous st_dev dedupes against
// nothing, so before markers narrowed discovery it was reported as a filesystem
// called "/" beside the real host root.
func TestFilesystemsDropsTheContainersOwnRoot(t *testing.T) {
	// Given: a table whose "/" is the container's overlay.
	testee := collector.NewFilesystems("testdata/mounts-markers", map[string]string{"root": "/"},
		fakeStatfs(map[string]collector.FsStat{
			"/netra/fs/root":    {Total: 1000, Free: 400, Used: 600, DeviceID: 1},
			"/netra/fs/ark":     {Total: 2000, Free: 100, Used: 1900, DeviceID: 2},
			"/netra/fs/var-log": {Total: 500, Free: 250, Used: 250, DeviceID: 3},
			"/netra/fs/backup":  {Total: 9000, Free: 4000, Used: 5000, DeviceID: 5},
			"/":                 {Total: 60, Free: 10, Used: 50, DeviceID: 99},
		}))

	// When: it is collected.
	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	// Then: the host root is reported, and the image layer is not.
	root := fsRow(t, res.Filesystems, "root")
	if got := root.GetTotal(); got != 1000 {
		t.Errorf("root total = %d, want 1000 -- the host root, not the 60-byte image layer", got)
	}
	for _, r := range res.Filesystems {
		if r.GetTotal() == 60 {
			t.Errorf("the container's own overlay layer was reported as %q", r.GetLabel())
		}
	}
}

// An agent upgraded ahead of its .env has no NETRA_FS_MOUNTS yet. It must still
// not report the container path: the bare label is a name the operator
// recognises, and the next setup run replaces it with the real mountpoint.
func TestFilesystemsFallsBackToTheLabelWithoutAMapping(t *testing.T) {
	// Given: marker mounts and no mapping at all.
	testee := collector.NewFilesystems("testdata/mounts-markers", nil,
		fakeStatfs(map[string]collector.FsStat{
			"/netra/fs/root":    {Total: 1000, Free: 400, Used: 600, DeviceID: 1},
			"/netra/fs/ark":     {Total: 2000, Free: 100, Used: 1900, DeviceID: 2},
			"/netra/fs/var-log": {Total: 500, Free: 250, Used: 250, DeviceID: 3},
			"/netra/fs/backup":  {Total: 9000, Free: 4000, Used: 5000, DeviceID: 5},
		}))

	// When: it is collected.
	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	// Then: label and mountpoint are both the bare label, prefix stripped.
	ark := fsRow(t, res.Filesystems, "ark")
	if got := ark.GetMountpoint(); got != "ark" {
		t.Errorf("ark mountpoint = %q, want the bare label, never the container path", got)
	}
}
