package collector

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

// sectorBytes is the unit /proc/diskstats counts sectors in.
//
// It is always 512, regardless of the device's logical or physical block size:
// the kernel normalises to 512-byte sectors in this file specifically, so
// reading the device's real block size and using that would overstate an
// advanced-format drive's throughput eightfold.
const sectorBytes = 512

// diskCounters is one device's line of /proc/diskstats, keeping only the
// fields netra reports. Every one is monotonic since boot and resets on
// reboot.
type diskCounters struct {
	readsCompleted  uint64
	sectorsRead     uint64
	msReading       uint64
	writesCompleted uint64
	sectorsWritten  uint64
	msWriting       uint64
	msDoingIO       uint64
	weightedMsIO    uint64
}

// DiskIO reports per-device block I/O rates from /proc/diskstats.
//
// Every value is a rate over the interval between two scrapes, computed here
// rather than hub-side: the counters reset on reboot, and only the agent holds
// the previous reading needed to notice. A device whose counters went
// backwards emits no row at all -- a negative rate is impossible, and clamping
// to zero would report the disk as idle during the busiest moment it had.
type DiskIO struct {
	procRoot string
	interval time.Duration

	now func() time.Time

	prev   map[string]diskCounters
	prevAt time.Time
}

// NewDiskIO builds a DiskIO collector reading from procRoot (normally "/proc").
func NewDiskIO(procRoot string, interval time.Duration) *DiskIO {
	return &DiskIO{procRoot: procRoot, interval: interval, now: time.Now}
}

// Name implements Collector.
func (d *DiskIO) Name() string { return "diskio" }

// Interval implements Collector.
func (d *DiskIO) Interval() time.Duration { return d.interval }

// SetProcRootForTest repoints the collector at a different fixture tree.
func (d *DiskIO) SetProcRootForTest(root string) { d.procRoot = root }

// SetClockForTest replaces the clock used to measure the scrape interval.
func (d *DiskIO) SetClockForTest(fn func() time.Time) { d.now = fn }

// Collect implements Collector.
func (d *DiskIO) Collect(_ context.Context) (*Result, error) {
	cur, err := d.read()
	if err != nil {
		return nil, err
	}

	prev, prevAt := d.prev, d.prevAt
	at := d.now()
	d.prev, d.prevAt = cur, at

	if prev == nil {
		// No baseline yet: report nothing rather than invent a value.
		return &Result{}, nil
	}

	elapsed := at.Sub(prevAt).Seconds()
	if elapsed <= 0 {
		// Two scrapes in the same tick. Dividing by this would produce +Inf,
		// which reaches the database as a value rather than as the absence of
		// one.
		return &Result{}, nil
	}

	names := make([]string, 0, len(cur))
	for name := range cur {
		names = append(names, name)
	}
	// Deterministic order so failures and logs are readable; the hub keys on
	// (host, ts, device) and does not care.
	slices.Sort(names)

	ts := at.UnixMilli()
	rows := make([]*netrav1.DiskIoSample, 0, len(names))

	for _, name := range names {
		c := cur[name]
		p, ok := prev[name]
		if !ok {
			// A device that appeared this scrape has no interval to average
			// over. It reports from the next scrape onwards.
			continue
		}
		if resetSince(p, c) {
			continue
		}

		readOps := float64(c.readsCompleted - p.readsCompleted)
		writeOps := float64(c.writesCompleted - p.writesCompleted)

		row := &netrav1.DiskIoSample{
			TsMs:          ts,
			Device:        name,
			ReadBytes:     ptrTo(float64(c.sectorsRead-p.sectorsRead) * sectorBytes / elapsed),
			WriteBytes:    ptrTo(float64(c.sectorsWritten-p.sectorsWritten) * sectorBytes / elapsed),
			ReadOps:       ptrTo(readOps / elapsed),
			WriteOps:      ptrTo(writeOps / elapsed),
			IoUtilPct:     ptrTo(float64(c.msDoingIO-p.msDoingIO) / (elapsed * 1000) * 100),
			WeightedIoPct: ptrTo(float64(c.weightedMsIO-p.weightedMsIO) / (elapsed * 1000) * 100),
		}

		// Await is milliseconds PER OPERATION, so with no operations there is
		// nothing to average. Dividing by zero would yield NaN, which is a
		// value the database would store rather than the absence of one.
		if readOps > 0 {
			row.RAwaitMs = ptrTo(float64(c.msReading-p.msReading) / readOps)
		}
		if writeOps > 0 {
			row.WAwaitMs = ptrTo(float64(c.msWriting-p.msWriting) / writeOps)
		}

		rows = append(rows, row)
	}

	return &Result{Disks: rows}, nil
}

// ptrTo is the local spelling of "take a pointer to this computed value", for
// the optional protobuf scalars that distinguish "not measured" from zero.
func ptrTo[T any](v T) *T { return &v }

// resetSince reports whether any counter went backwards, which means the host
// rebooted (or the device was removed and re-added) between the two readings.
// One counter is enough: they all reset together.
func resetSince(p, c diskCounters) bool {
	return c.readsCompleted < p.readsCompleted ||
		c.sectorsRead < p.sectorsRead ||
		c.msReading < p.msReading ||
		c.writesCompleted < p.writesCompleted ||
		c.sectorsWritten < p.sectorsWritten ||
		c.msWriting < p.msWriting ||
		c.msDoingIO < p.msDoingIO ||
		c.weightedMsIO < p.weightedMsIO
}

// reportable reports whether a device name is a whole physical device worth
// reporting.
//
// Partitions are excluded because their I/O is already counted in the parent
// device: reporting both double-counts the host's throughput, and an operator
// summing the series gets twice the real number. Loop and ram devices are not
// physical storage and would add a series per mounted snap or image.
func reportable(name string) bool {
	switch {
	case strings.HasPrefix(name, "loop"), strings.HasPrefix(name, "ram"),
		strings.HasPrefix(name, "zram"), strings.HasPrefix(name, "dm-"):
		return false
	}

	// nvme0n1 and mmcblk0 are whole devices whose names END in a digit;
	// their partitions add a "p<N>" suffix (nvme0n1p1, mmcblk0p1). The
	// trailing-digit rule below would drop the device itself, which on an SD
	// card or eMMC host is the only disk it has -- leaving disk_io_samples
	// empty for every Raspberry Pi and ARM board in the fleet.
	if strings.HasPrefix(name, "nvme") || strings.HasPrefix(name, "mmcblk") {
		if i := strings.LastIndex(name, "p"); i > 0 {
			if _, err := strconv.Atoi(name[i+1:]); err == nil {
				return false
			}
		}
		return true
	}

	// sda1, vdb2: a trailing digit on an otherwise alphabetic name.
	if n := len(name); n > 0 && name[n-1] >= '0' && name[n-1] <= '9' {
		return false
	}
	return true
}

// read parses /proc/diskstats into per-device counters.
func (d *DiskIO) read() (map[string]diskCounters, error) {
	path := filepath.Join(d.procRoot, "diskstats")
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	out := make(map[string]diskCounters)

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		// major, minor, name, then the 11 fields of the pre-4.18 layout. Newer
		// kernels append discard and flush counters, which netra does not
		// report -- reading by index from the front keeps both layouts working.
		if len(fields) < 14 {
			continue
		}
		name := fields[2]
		if !reportable(name) {
			continue
		}

		values := make([]uint64, 0, 11)
		for _, raw := range fields[3:14] {
			v, err := strconv.ParseUint(raw, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("parse %s %s: %w", path, name, err)
			}
			values = append(values, v)
		}

		out[name] = diskCounters{
			readsCompleted:  values[0],
			sectorsRead:     values[2],
			msReading:       values[3],
			writesCompleted: values[4],
			sectorsWritten:  values[6],
			msWriting:       values[7],
			msDoingIO:       values[9],
			weightedMsIO:    values[10],
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	return out, nil
}
