package collector_test

import (
	"context"
	"errors"
	"testing"

	"github.com/trick77/netra/internal/agent/collector"
	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

// fakeStatfs answers from a table keyed by mountpoint, so the test controls
// st_dev and usage rather than reporting whatever the machine running it
// happens to have mounted.
func fakeStatfs(table map[string]collector.FsStat) collector.StatfsFunc {
	return func(mountpoint string) (collector.FsStat, error) {
		st, ok := table[mountpoint]
		if !ok {
			return collector.FsStat{}, errors.New("no such mountpoint")
		}
		return st, nil
	}
}

func fsRow(t *testing.T, rows []*netrav1.FilesystemSample, label string) *netrav1.FilesystemSample {
	t.Helper()
	for _, r := range rows {
		if r.GetLabel() == label {
			return r
		}
	}
	t.Fatalf("no row for %q in %d rows", label, len(rows))
	return nil
}

func TestFilesystemsReportsUsagePerMount(t *testing.T) {
	testee := collector.NewFilesystems("testdata/mounts", fakeStatfs(map[string]collector.FsStat{
		"/":            {Total: 1000, Free: 400, Used: 600, InodesTotal: 100, InodesFree: 60, DeviceID: 1},
		"/data":        {Total: 2000, Free: 500, Used: 1500, InodesTotal: 200, InodesFree: 50, DeviceID: 2},
		"/run":         {Total: 500, Free: 250, Used: 250, InodesTotal: 50, InodesFree: 25, DeviceID: 3},
		"/mnt/my disk": {Total: 300, Free: 100, Used: 200, InodesTotal: 30, InodesFree: 10, DeviceID: 4},
	}))

	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	root := fsRow(t, res.Filesystems, "/")
	if got := root.GetTotal(); got != 1000 {
		t.Errorf("/ total = %d, want 1000", got)
	}
	if got := root.GetUsed(); got != 600 {
		t.Errorf("/ used = %d, want 600 (total - free)", got)
	}
	if got := root.GetFree(); got != 400 {
		t.Errorf("/ free = %d, want 400", got)
	}
	if got := root.GetInodesUsed(); got != 40 {
		t.Errorf("/ inodes_used = %d, want 40", got)
	}
	if root.GetTsMs() == 0 {
		t.Error("row carries no ts_ms")
	}
}

// The same filesystem mounted twice must produce ONE row.
//
// /dev/sda1 is mounted at both / and /var/lib/docker in the fixture, sharing
// an st_dev. Counting both would overstate the host's disk usage by the size
// of the bind mount -- and a container host has many.
func TestFilesystemsDedupesBindMountsByDeviceID(t *testing.T) {
	testee := collector.NewFilesystems("testdata/mounts", fakeStatfs(map[string]collector.FsStat{
		"/":               {Total: 1000, Free: 400, Used: 600, DeviceID: 1},
		"/var/lib/docker": {Total: 1000, Free: 400, Used: 600, DeviceID: 1}, // same device
		"/data":           {Total: 2000, Free: 500, Used: 1500, DeviceID: 2},
	}))

	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	var total uint64
	for _, r := range res.Filesystems {
		total += r.GetTotal()
	}
	if total != 3000 {
		t.Errorf("summed total = %d, want 3000 -- the bind mount must not be counted twice", total)
	}
	if len(res.Filesystems) != 2 {
		t.Errorf("rows = %d, want 2", len(res.Filesystems))
	}
}

// Pseudo-filesystems have no storage behind them, so their "usage" is
// meaningless. tmpfs is deliberately NOT excluded: a full tmpfs really does
// break things.
func TestFilesystemsExcludesPseudoFilesystemsButKeepsTmpfs(t *testing.T) {
	testee := collector.NewFilesystems("testdata/mounts", fakeStatfs(map[string]collector.FsStat{
		"/":    {Total: 1000, Free: 400, Used: 600, DeviceID: 1},
		"/run": {Total: 500, Free: 250, Used: 250, DeviceID: 3},
	}))

	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	for _, r := range res.Filesystems {
		switch r.GetLabel() {
		case "/proc", "/sys", "/sys/fs/cgroup":
			t.Errorf("%s reported; pseudo-filesystems have no storage", r.GetLabel())
		}
	}
	if fsRow(t, res.Filesystems, "/run").GetTotal() != 500 {
		t.Error("tmpfs at /run was dropped; a full tmpfs really does break things")
	}
}

// Per-filesystem I/O needs the st_dev to block device mapping, which this
// collector does not have. The columns stay NULL rather than zero: "could not
// attribute I/O to this filesystem" is a different fact from "did no I/O", and
// a rollup averaging zeros in would understate every host.
func TestFilesystemsLeavesIOUnsetRatherThanZero(t *testing.T) {
	testee := collector.NewFilesystems("testdata/mounts", fakeStatfs(map[string]collector.FsStat{
		"/": {Total: 1000, Free: 400, Used: 600, DeviceID: 1},
	}))

	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	row := fsRow(t, res.Filesystems, "/")
	if row.ReadBytes != nil {
		t.Errorf("read_bytes = %v, want unset", row.GetReadBytes())
	}
	if row.WriteBytes != nil {
		t.Errorf("write_bytes = %v, want unset", row.GetWriteBytes())
	}
}

// A mountpoint that cannot be stat'd -- an unreachable NFS server, a mount
// that vanished between reading the table and the call -- must not cost the
// other filesystems their reading.
func TestFilesystemsSkipsAnUnstatableMount(t *testing.T) {
	testee := collector.NewFilesystems("testdata/mounts", fakeStatfs(map[string]collector.FsStat{
		"/": {Total: 1000, Free: 400, Used: 600, DeviceID: 1},
		// /broken deliberately absent from the table: statfs will error.
	}))

	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v, want the readable filesystems reported anyway", err)
	}
	if len(res.Filesystems) != 1 {
		t.Fatalf("rows = %d, want 1", len(res.Filesystems))
	}
	if res.Filesystems[0].GetLabel() != "/" {
		t.Errorf("label = %q, want /", res.Filesystems[0].GetLabel())
	}
}

// Mountpoints are octal-escaped in /proc/mounts. A mountpoint with a space
// arrives as "/mnt/my\040disk", and passing that to statfs unescaped would
// fail on every such mount.
func TestFilesystemsUnescapesMountpoints(t *testing.T) {
	testee := collector.NewFilesystems("testdata/mounts", fakeStatfs(map[string]collector.FsStat{
		"/mnt/my disk": {Total: 300, Free: 100, Used: 200, DeviceID: 4},
	}))

	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(res.Filesystems) != 1 {
		t.Fatalf("rows = %d, want 1 -- the escaped mountpoint was not decoded", len(res.Filesystems))
	}
	if got := res.Filesystems[0].GetLabel(); got != "/mnt/my disk" {
		t.Errorf("label = %q, want %q", got, "/mnt/my disk")
	}
}

// An unreadable /proc/mounts is an error, not an empty result.
func TestFilesystemsReportsAnUnreadableMountTable(t *testing.T) {
	testee := collector.NewFilesystems(t.TempDir(), fakeStatfs(nil))

	res, err := testee.Collect(context.Background())
	if err == nil {
		t.Fatal("Collect succeeded with no mount table, want an error")
	}
	if res != nil {
		t.Errorf("Collect returned %+v alongside an error; want nil", res)
	}
}

// On a filesystem with a root reserve, used and free are independent numbers
// that do NOT sum to total.
//
// ext4 reserves 5% for root by default. Those blocks hold no data, so they are
// not used; an unprivileged process cannot allocate them, so they are not
// free. Deriving used as total - free would count the reserve as occupied and
// overstate consumption on every default ext4 filesystem -- and disagree with
// the df output an operator checks the number against.
//
// 1000 total, 50 reserved: 400 available to anyone, 450 not holding data, so
// used is 550 and free is 400. Their sum is 950, and that is correct.
func TestFilesystemsDoesNotCountTheRootReserveAsUsed(t *testing.T) {
	testee := collector.NewFilesystems("testdata/mounts", fakeStatfs(map[string]collector.FsStat{
		"/": {Total: 1000, Free: 400, Used: 550, DeviceID: 1},
	}))

	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	root := fsRow(t, res.Filesystems, "/")
	if got := root.GetUsed(); got != 550 {
		t.Errorf("/ used = %d, want 550 -- total - free (600) counts the root reserve as used", got)
	}
	if got := root.GetFree(); got != 400 {
		t.Errorf("/ free = %d, want 400 -- the reserve is not available either", got)
	}
	if root.GetUsed()+root.GetFree() == root.GetTotal() {
		t.Error("used + free = total, so one of them absorbed the root reserve")
	}
}

// A filesystem reporting more free inodes than it has -- which a dynamic or
// synthetic inode count permits -- must leave inodes_used unset rather than
// wrap the unsigned subtraction into a number near 2^64 and store it as though
// it had been measured. Unset means "could not compute"; the wrapped value
// would mean "this filesystem has eighteen quintillion inodes in use".
func TestInodesUsedUnsetWhenFreeExceedsTotal(t *testing.T) {
	// Given: a mount whose statfs reports InodesFree above InodesTotal.
	testee := collector.NewFilesystems("testdata/mounts", fakeStatfs(map[string]collector.FsStat{
		"/": {Total: 1000, Free: 400, Used: 600, InodesTotal: 100, InodesFree: 250, DeviceID: 1},
	}))

	// When: it is collected.
	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	// Then: the row is still reported, but inodes_used is absent.
	root := fsRow(t, res.Filesystems, "/")
	if root.InodesUsed != nil {
		t.Errorf("inodes_used = %d, want unset when free exceeds total", root.GetInodesUsed())
	}
	if got := root.GetInodesTotal(); got != 100 {
		t.Errorf("inodes_total = %d, want 100 -- the reading itself is still a fact", got)
	}
	if got := root.GetUsed(); got != 600 {
		t.Errorf("used = %d, want 600 -- one unusable field must not cost the rest of the row", got)
	}
}
