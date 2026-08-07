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

// cpuTimes holds the fields of the aggregate "cpu" line in /proc/stat.
type cpuTimes struct {
	user, nice, system, idle, iowait, irq, softirq, steal uint64
	guest, guestNice                                      uint64
}

// total excludes guest and guestNice: the kernel already counts guest time
// inside user, and nice guest inside nice, so adding them double-counts.
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
	interval time.Duration
	prev     *cpuTimes
}

// NewCPU builds a CPU collector reading from procRoot (normally "/proc").
func NewCPU(procRoot string, interval time.Duration) *CPU {
	return &CPU{procRoot: procRoot, interval: interval}
}

// Name implements Collector.
func (c *CPU) Name() string { return "cpu" }

// Interval implements Collector.
func (c *CPU) Interval() time.Duration { return c.interval }

// SetProcRootForTest repoints the collector at a different fixture tree so a
// test can simulate the passage of time between two scrapes.
func (c *CPU) SetProcRootForTest(root string) { c.procRoot = root }

// Collect implements Collector.
func (c *CPU) Collect(_ context.Context, sample *netrav1.HostSample) error {
	cur, err := c.read()
	if err != nil {
		return err
	}

	prev := c.prev
	c.prev = &cur

	if prev == nil {
		// No baseline yet: report nothing rather than invent a value.
		return nil
	}

	totalDelta := cur.total() - prev.total()
	if cur.total() < prev.total() || totalDelta == 0 {
		// Counters went backwards (reboot) or did not move. Either way there
		// is no meaningful percentage to report.
		return nil
	}

	pct := func(a, b uint64) *float64 {
		if a < b {
			return nil
		}
		v := float64(a-b) / float64(totalDelta) * 100
		return &v
	}

	busyDelta := float64(cur.busy() - prev.busy())
	if cur.busy() < prev.busy() {
		return nil
	}
	totalPct := busyDelta / float64(totalDelta) * 100

	sample.CpuTotal = &totalPct
	sample.CpuUser = pct(cur.user, prev.user)
	sample.CpuSystem = pct(cur.system, prev.system)
	sample.CpuIowait = pct(cur.iowait, prev.iowait)
	sample.CpuSteal = pct(cur.steal, prev.steal)
	sample.CpuIdle = pct(cur.idle, prev.idle)

	return nil
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
		if len(fields) < 8 || fields[0] != "cpu" {
			continue
		}

		values := make([]uint64, 0, 10)
		for _, raw := range fields[1:] {
			v, err := strconv.ParseUint(raw, 10, 64)
			if err != nil {
				return cpuTimes{}, fmt.Errorf("parse %s: %w", path, err)
			}
			values = append(values, v)
		}
		for len(values) < 10 {
			values = append(values, 0)
		}

		return cpuTimes{
			user: values[0], nice: values[1], system: values[2], idle: values[3],
			iowait: values[4], irq: values[5], softirq: values[6], steal: values[7],
			guest: values[8], guestNice: values[9],
		}, nil
	}
	if err := scanner.Err(); err != nil {
		return cpuTimes{}, fmt.Errorf("read %s: %w", path, err)
	}

	return cpuTimes{}, fmt.Errorf("no aggregate cpu line in %s", path)
}
