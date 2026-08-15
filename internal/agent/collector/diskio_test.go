package collector_test

import (
	"context"
	"testing"
	"time"

	"github.com/trick77/netra/internal/agent/collector"
	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
)

// diskioAt runs one scrape with the clock pinned, so the rates below are exact
// rather than dependent on how long the test itself took.
func diskioAt(t *testing.T, c *collector.DiskIO, at time.Time) *collector.Result {
	t.Helper()
	c.SetClockForTest(func() time.Time { return at })
	res, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	return res
}

func diskRow(t *testing.T, rows []*netrav1.DiskIoSample, device string) *netrav1.DiskIoSample {
	t.Helper()
	for _, r := range rows {
		if r.GetDevice() == device {
			return r
		}
	}
	t.Fatalf("no row for %q in %d rows", device, len(rows))
	return nil
}

// Fixture arithmetic for sda over ten seconds:
//
//	reads   1000 -> 1100  =  100 ops / 10s =  10 ops/s
//	sectors 2000 -> 6000  = 4000 * 512 B   = 204800 B/s
//	ms read 3000 -> 3200  =  200 ms / 100  = 2 ms per read
//	writes  4000 -> 4050  =   50 ops / 10s =  5 ops/s
//	written 5000 -> 7000  = 2000 * 512 B   = 102400 B/s
//	ms writ 6000 -> 6250  =  250 ms / 50   = 5 ms per write
//	io ms   7000 -> 12000 = 5000 / 10000   = 50%
//	weight  8000 -> 28000 = 20000 / 10000  = 200%
//
// Every expected value is distinct, so a transposed field lands on a number
// the test is not looking for rather than passing by coincidence.
func TestDiskIOComputesRatesPerDevice(t *testing.T) {
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	testee := collector.NewDiskIO("testdata/diskstats/first")

	// The first scrape is the baseline: a rate needs a previous reading.
	res := diskioAt(t, testee, base)
	if len(res.Disks) != 0 {
		t.Fatalf("first scrape produced %d rows, want 0", len(res.Disks))
	}

	testee.SetProcRootForTest("testdata/diskstats/second")
	res = diskioAt(t, testee, base.Add(10*time.Second))

	sda := diskRow(t, res.Disks, "sda")
	for _, c := range []struct {
		name string
		got  float64
		want float64
	}{
		{"read_bytes", sda.GetReadBytes(), 204800},
		{"write_bytes", sda.GetWriteBytes(), 102400},
		{"read_ops", sda.GetReadOps(), 10},
		{"write_ops", sda.GetWriteOps(), 5},
		{"io_util_pct", sda.GetIoUtilPct(), 50},
		{"r_await_ms", sda.GetRAwaitMs(), 2},
		{"w_await_ms", sda.GetWAwaitMs(), 5},
		{"weighted_io_pct", sda.GetWeightedIoPct(), 200},
	} {
		if c.got != c.want {
			t.Errorf("sda %s = %v, want %v", c.name, c.got, c.want)
		}
	}

	if sda.GetTsMs() == 0 {
		t.Error("row carries no ts_ms")
	}
}

// A partition's I/O is already counted in its parent device, so reporting both
// double-counts the host's throughput. Loop devices are not physical storage
// and would add a series per mounted snap or image.
func TestDiskIOReportsWholeDevicesOnly(t *testing.T) {
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	testee := collector.NewDiskIO("testdata/diskstats/first")
	diskioAt(t, testee, base)

	testee.SetProcRootForTest("testdata/diskstats/second")
	res := diskioAt(t, testee, base.Add(10*time.Second))

	seen := map[string]bool{}
	for _, r := range res.Disks {
		seen[r.GetDevice()] = true
		switch r.GetDevice() {
		case "sda1":
			t.Error("sda1 reported; a partition's I/O is already counted in sda")
		case "loop0":
			t.Error("loop0 reported; loop devices are not physical storage")
		case "mmcblk0p1":
			t.Error("mmcblk0p1 reported; a partition's I/O is already counted in mmcblk0")
		case "nbd0p1":
			t.Error("nbd0p1 reported; a partition's I/O is already counted in nbd0")
		// Stacked virtual devices. Every byte they move also crosses a member
		// device that IS reported, so counting them doubles the host.
		case "md0":
			t.Error("md0 reported; an array's I/O is already counted in its members")
		case "zd0":
			t.Error("zd0 reported; a zvol's I/O is already counted in the pool's vdevs")
		}
	}
	// mmcblk0 is a WHOLE device whose name ends in a digit, like nvme0n1.
	// Dropping it would leave every Raspberry Pi and ARM board -- whose only
	// disk it is -- with no disk I/O at all.
	if !seen["mmcblk0"] {
		t.Error("mmcblk0 not reported; it is a whole device, not a partition")
	}
	// nbd0 ends in a digit too, and unlike md0 and zd0 it is real remote
	// storage stacked on nothing -- so it double-counts no one and must be
	// reported.
	if !seen["nbd0"] {
		t.Error("nbd0 not reported; a network block device is real storage, not a stacked layer")
	}
	if len(res.Disks) != 4 {
		t.Errorf("devices = %d, want 4 (sda, nvme0n1, mmcblk0 and nbd0)", len(res.Disks))
	}
}

// A reboot resets every counter. A device whose counters went backwards emits
// no row at all: a negative rate is impossible, and clamping to zero would
// report the disk as idle during the busiest moment it had.
func TestDiskIOEmitsNoRowAfterACounterReset(t *testing.T) {
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	testee := collector.NewDiskIO("testdata/diskstats/second")
	diskioAt(t, testee, base)

	testee.SetProcRootForTest("testdata/diskstats/reset")
	res := diskioAt(t, testee, base.Add(10*time.Second))

	if len(res.Disks) != 0 {
		t.Errorf("rows after a counter reset = %d, want 0; got %+v", len(res.Disks), res.Disks)
	}
}

// The next scrape after a reset reports normally: the reset scrape re-baselines
// rather than poisoning the collector permanently.
func TestDiskIORecoversAfterAReset(t *testing.T) {
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	testee := collector.NewDiskIO("testdata/diskstats/second")
	diskioAt(t, testee, base)

	testee.SetProcRootForTest("testdata/diskstats/reset")
	diskioAt(t, testee, base.Add(10*time.Second))

	// Counters now advance from the reset values.
	testee.SetProcRootForTest("testdata/diskstats/first")
	res := diskioAt(t, testee, base.Add(20*time.Second))

	if len(res.Disks) == 0 {
		t.Fatal("no rows after recovery; a reset must re-baseline, not disable the collector")
	}
}

// Elapsed time of zero would divide by zero. It happens when two scrapes land
// in the same clock tick, which a millisecond-interval test does routinely.
func TestDiskIOEmitsNoRowWhenNoTimePassed(t *testing.T) {
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	testee := collector.NewDiskIO("testdata/diskstats/first")
	diskioAt(t, testee, base)

	testee.SetProcRootForTest("testdata/diskstats/second")
	res := diskioAt(t, testee, base)

	if len(res.Disks) != 0 {
		t.Errorf("rows with zero elapsed = %d, want 0", len(res.Disks))
	}
}

// A device with no completed reads has no per-read latency to report. Dividing
// by a zero op count would produce NaN, which reaches the database as a value
// rather than as the absence of one.
func TestDiskIOLeavesAwaitUnsetWithNoOperations(t *testing.T) {
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	testee := collector.NewDiskIO("testdata/diskstats/first")
	diskioAt(t, testee, base)

	// nvme0n1 advances reads by 10 and writes by 5 in the second fixture, so
	// use a tree where one device does not move at all.
	testee.SetProcRootForTest("testdata/diskstats/first")
	res := diskioAt(t, testee, base.Add(10*time.Second))

	for _, r := range res.Disks {
		if r.RAwaitMs != nil {
			t.Errorf("%s r_await_ms = %v with no reads completed; want unset", r.GetDevice(), r.GetRAwaitMs())
		}
	}
}

// An unreadable /proc/diskstats is an error, not an empty result: the caller
// must be able to tell "no block devices" from "could not read the file".
func TestDiskIOReportsAnUnreadableDiskstats(t *testing.T) {
	testee := collector.NewDiskIO(t.TempDir())

	res, err := testee.Collect(context.Background())
	if err == nil {
		t.Fatal("Collect succeeded with no diskstats, want an error")
	}
	if res != nil {
		t.Errorf("Collect returned %+v alongside an error; want nil", res)
	}
}
