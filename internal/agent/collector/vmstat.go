package collector

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

// vmCounters holds the /proc/vmstat counters netra reports.
//
// Pointers for the same reason as kernelCounters: a kernel that does not
// publish one of these keys must not read as a counter pinned at 0, or the
// first scrape after it appeared would report the whole since-boot total as
// though it happened in one interval.
type vmCounters struct {
	pgmajfault *uint64
	pswpin     *uint64
	pswpout    *uint64
	oomKill    *uint64
}

// VMStat reports memory-pressure counters from /proc/vmstat.
//
// These answer a question the memory gauges cannot. mem_available says how
// much memory is free; it says nothing about what the machine had to DO to
// keep it that way. A host can sit at a comfortable-looking 40% used while
// thrashing: major faults mean pages are being fetched from disk to satisfy
// reads, and swap in/out means the kernel is paying for that memory with I/O.
//
// oom_kill_total is deliberately a counter rather than a rate. A single OOM
// kill is a discrete event an operator needs to see happened at all, and a
// per-second rate of one kill in a 60s interval rounds to a number that reads
// like noise.
type VMStat struct {
	procRoot string

	// now is a seam so tests can drive elapsed time exactly.
	now func() time.Time

	prev   *vmCounters
	prevAt time.Time
}

// NewVMStat builds a VMStat collector reading from procRoot.
func NewVMStat(procRoot string) *VMStat {
	return &VMStat{procRoot: procRoot, now: time.Now}
}

// Name implements Collector.
func (v *VMStat) Name() string { return "vmstat" }

// SetProcRootForTest repoints the collector at a different fixture tree so a
// test can simulate the passage of time between two scrapes.
func (v *VMStat) SetProcRootForTest(root string) { v.procRoot = root }

// SetClockForTest replaces the clock used to measure the interval.
func (v *VMStat) SetClockForTest(fn func() time.Time) { v.now = fn }

// Collect implements Collector.
func (v *VMStat) Collect(_ context.Context) (*Result, error) {
	sample := &netrav1.HostSample{}

	cur, err := v.read()
	if err != nil {
		return nil, err
	}

	// A cumulative total, not a rate: reported on the first scrape too,
	// because "has this host ever OOM-killed" is answerable without a
	// baseline and is worth answering immediately.
	sample.OomKillTotal = cur.oomKill

	at := v.now()
	prev, prevAt := v.prev, v.prevAt
	v.prev, v.prevAt = &cur, at

	if prev == nil {
		return &Result{Host: sample}, nil
	}

	elapsed := at.Sub(prevAt).Seconds()
	if elapsed <= 0 {
		return &Result{Host: sample}, nil
	}

	sample.PgmajfaultPerS = rate(cur.pgmajfault, prev.pgmajfault, elapsed)
	sample.PswpinPerS = rate(cur.pswpin, prev.pswpin, elapsed)
	sample.PswpoutPerS = rate(cur.pswpout, prev.pswpout, elapsed)

	return &Result{Host: sample}, nil
}

// read parses the "key value" lines of /proc/vmstat.
func (v *VMStat) read() (vmCounters, error) {
	path := filepath.Join(v.procRoot, "vmstat")
	f, err := os.Open(path)
	if err != nil {
		return vmCounters{}, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	var out vmCounters

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			continue
		}
		n, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}
		val := n
		switch fields[0] {
		case "pgmajfault":
			out.pgmajfault = &val
		case "pswpin":
			out.pswpin = &val
		case "pswpout":
			out.pswpout = &val
		case "oom_kill":
			// Absent on kernels before 4.13, and absent inside some
			// containers. Unset, not zero.
			out.oomKill = &val
		}
	}
	if err := scanner.Err(); err != nil {
		return vmCounters{}, fmt.Errorf("read %s: %w", path, err)
	}

	return out, nil
}
