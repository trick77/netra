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

// kernelCounters holds the monotonic counters of /proc/stat.
//
// Each is a pointer because absence must not read as zero: a kernel that does
// not publish one of these lines would otherwise look like a counter pinned
// at 0, and the first scrape after it appeared would report the entire
// since-boot total as if it had happened in one interval.
type kernelCounters struct {
	ctxt      *uint64
	intr      *uint64
	processes *uint64
}

// kernelGauges holds the values of /proc/stat that are not counters. Each is
// separately optional: an older kernel may omit procs_blocked entirely, which
// is an absent fact rather than a parse failure.
type kernelGauges struct {
	procsRunning *uint32
	procsBlocked *uint32
	bootTime     *uint64
}

// KernelStat reports the /proc/stat lines the CPU collector steps over:
// context switches, interrupts, fork rate, runnable and blocked task counts,
// and the boot timestamp.
//
// It is a separate collector rather than an extension of CPU for two reasons.
// CPU.read returns as soon as it has the aggregate "cpu" line, and these
// values all appear after the per-core lines. More importantly CPU is
// wall-clock-free -- its percentages are ratios of jiffy deltas, so a clock
// that steps cannot affect them -- whereas per-second rates need elapsed real
// time. Keeping the clock out of CPU keeps a correct, well-tested collector
// free of a failure mode it does not otherwise have.
type KernelStat struct {
	procRoot string

	// now is a seam so tests can drive elapsed time exactly.
	now func() time.Time

	prev   *kernelCounters
	prevAt time.Time
}

// NewKernelStat builds a KernelStat collector reading from procRoot.
func NewKernelStat(procRoot string) *KernelStat {
	return &KernelStat{procRoot: procRoot, now: time.Now}
}

// Name implements Collector.
func (k *KernelStat) Name() string { return "kernelstat" }

// SetProcRootForTest repoints the collector at a different fixture tree so a
// test can simulate the passage of time between two scrapes.
func (k *KernelStat) SetProcRootForTest(root string) { k.procRoot = root }

// SetClockForTest replaces the clock used to measure the interval between two
// scrapes, so rate arithmetic is exact rather than timing-dependent.
func (k *KernelStat) SetClockForTest(fn func() time.Time) { k.now = fn }

// Collect implements Collector.
func (k *KernelStat) Collect(_ context.Context) (*Result, error) {
	sample := &netrav1.HostSample{}

	cur, gauges, err := k.read()
	if err != nil {
		return nil, err
	}

	// Gauges are reported on every scrape including the first: they are
	// instantaneous readings and need no baseline.
	sample.ProcsRunning = gauges.procsRunning
	sample.ProcsBlocked = gauges.procsBlocked
	sample.BootTimeS = gauges.bootTime

	at := k.now()
	prev, prevAt := k.prev, k.prevAt
	k.prev, k.prevAt = &cur, at

	if prev == nil {
		// No baseline yet: report no rates rather than invent them.
		return &Result{Host: sample}, nil
	}

	elapsed := at.Sub(prevAt).Seconds()
	if elapsed <= 0 {
		// The clock did not advance, or stepped backwards. Dividing by that
		// produces an infinity or a negative rate, neither of which is a
		// measurement.
		return &Result{Host: sample}, nil
	}

	sample.CtxtPerS = rate(cur.ctxt, prev.ctxt, elapsed)
	sample.IntrPerS = rate(cur.intr, prev.intr, elapsed)
	sample.ForksPerS = rate(cur.processes, prev.processes, elapsed)

	return &Result{Host: sample}, nil
}

// rate converts a counter delta to a per-second value. It returns nil when
// either reading is absent or the counter went backwards. Each counter is
// judged on its own: a reset in one says nothing about the others, so one bad
// counter must not suppress the rest of the scrape.
func rate(cur, prev *uint64, elapsed float64) *float64 {
	if cur == nil || prev == nil || *cur < *prev {
		return nil
	}
	v := float64(*cur-*prev) / elapsed
	return &v
}

func (k *KernelStat) read() (kernelCounters, kernelGauges, error) {
	path := filepath.Join(k.procRoot, "stat")
	f, err := os.Open(path)
	if err != nil {
		return kernelCounters{}, kernelGauges{}, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	var (
		counters kernelCounters
		gauges   kernelGauges
	)

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}

		// intr and softirq are followed by a per-vector breakdown; only the
		// first value is the total, and the rest is ignored deliberately.
		v, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}

		switch fields[0] {
		case "ctxt":
			c := v
			counters.ctxt = &c
		case "intr":
			c := v
			counters.intr = &c
		case "processes":
			c := v
			counters.processes = &c
		case "procs_running":
			n := uint32(v)
			gauges.procsRunning = &n
		case "procs_blocked":
			n := uint32(v)
			gauges.procsBlocked = &n
		case "btime":
			b := v
			gauges.bootTime = &b
		}
	}
	if err := scanner.Err(); err != nil {
		return kernelCounters{}, kernelGauges{}, fmt.Errorf("read %s: %w", path, err)
	}

	return counters, gauges, nil
}
