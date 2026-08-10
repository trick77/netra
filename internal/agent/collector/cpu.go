package collector

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

// cpuTimes holds the fields of a "cpu" or "cpuN" line in /proc/stat.
//
// guest and guest_nice are read past but not kept. The kernel already counts
// guest time inside user and nice guest inside nice, so they are not separate
// time -- and nothing here reports them on their own. Storing them invited a
// future reader to add them to total() and double-count.
type cpuTimes struct {
	user, nice, system, idle, iowait, irq, softirq, steal uint64
}

// cpuTimeFields is how many values a cpu line carries that this package reads:
// user through steal. Newer kernels append guest and guest_nice, and may append
// more; reading by index from the front keeps every layout working.
const cpuTimeFields = 8

// parseCPUTimes reads the counters from an already-split cpu line, given the
// fields AFTER the "cpu"/"cpuN" label.
//
// Shared by CPU and PerCoreCPU, which parse the same columns out of the same
// file. They deliberately read it separately -- an unparseable cpuN line must
// not cost the host its aggregate utilisation, which is why they are two
// collectors -- but the column layout is one fact and was written out twice.
func parseCPUTimes(values []string) (cpuTimes, error) {
	if len(values) < cpuTimeFields {
		return cpuTimes{}, fmt.Errorf("want at least %d fields, got %d", cpuTimeFields, len(values))
	}

	var n [cpuTimeFields]uint64
	for i := range n {
		v, err := strconv.ParseUint(values[i], 10, 64)
		if err != nil {
			return cpuTimes{}, err
		}
		n[i] = v
	}

	return cpuTimes{
		user: n[0], nice: n[1], system: n[2], idle: n[3],
		iowait: n[4], irq: n[5], softirq: n[6], steal: n[7],
	}, nil
}

// total is every counted jiffy. It excludes guest and guest_nice for the
// reason cpuTimes gives: the kernel has already counted them inside user and
// nice, so adding them double-counts.
func (c cpuTimes) total() uint64 {
	return c.user + c.nice + c.system + c.idle +
		c.iowait + c.irq + c.softirq + c.steal
}

func (c cpuTimes) busy() uint64 {
	return c.total() - c.idle - c.iowait
}

// CPU reports aggregate CPU utilisation from /proc/stat.
//
// Values are percentages over the interval between two scrapes, so the first
// scrape after start produces nothing.
type CPU struct {
	procRoot string
	prev     *cpuTimes
}

// NewCPU builds a CPU collector reading from procRoot (normally "/proc").
func NewCPU(procRoot string) *CPU {
	return &CPU{procRoot: procRoot}
}

// Name implements Collector.
func (c *CPU) Name() string { return "cpu" }

// SetProcRootForTest repoints the collector at a different fixture tree so a
// test can simulate the passage of time between two scrapes.
func (c *CPU) SetProcRootForTest(root string) { c.procRoot = root }

// Collect implements Collector.
func (c *CPU) Collect(_ context.Context) (*Result, error) {
	cur, err := c.read()
	if err != nil {
		return nil, err
	}

	prev := c.prev
	c.prev = &cur

	if prev == nil {
		// No baseline yet: report nothing rather than invent a value.
		return &Result{}, nil
	}

	// Ordered so the comparison happens BEFORE the subtraction. It was the
	// other way round, which was correct only because the wrapped value was
	// then discarded -- a reader had to prove that to themselves to be sure.
	if cur.total() < prev.total() {
		// Counters went backwards: a reboot between the two scrapes.
		return &Result{}, nil
	}
	totalDelta := cur.total() - prev.total()
	if totalDelta == 0 {
		// The counters did not move, so there is no interval to average over.
		return &Result{}, nil
	}

	pct := func(a, b uint64) *float64 {
		if a < b {
			return nil
		}
		v := float64(a-b) / float64(totalDelta) * 100
		return &v
	}

	if cur.busy() < prev.busy() {
		return &Result{}, nil
	}
	totalPct := float64(cur.busy()-prev.busy()) / float64(totalDelta) * 100

	return &Result{Host: &netrav1.HostSample{
		CpuTotal:  &totalPct,
		CpuUser:   pct(cur.user, prev.user),
		CpuSystem: pct(cur.system, prev.system),
		CpuIowait: pct(cur.iowait, prev.iowait),
		CpuSteal:  pct(cur.steal, prev.steal),
		CpuIdle:   pct(cur.idle, prev.idle),
	}}, nil
}

func (c *CPU) read() (cpuTimes, error) {
	path := filepath.Join(c.procRoot, "stat")
	f, err := os.Open(path)
	if err != nil {
		return cpuTimes{}, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 || fields[0] != "cpu" {
			continue
		}

		times, err := parseCPUTimes(fields[1:])
		if err != nil {
			return cpuTimes{}, fmt.Errorf("parse %s: %w", path, err)
		}
		return times, nil
	}
	if err := scanner.Err(); err != nil {
		return cpuTimes{}, fmt.Errorf("read %s: %w", path, err)
	}

	return cpuTimes{}, fmt.Errorf("no aggregate cpu line in %s", path)
}
