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

// PerCoreCPU reports per-core busy percentage from /proc/stat's cpuN lines.
//
// It is separate from CPU on purpose. CPU owns the aggregate "cpu" line and
// writes scalar host fields; this owns the per-core lines and produces one row
// per core. Keeping them apart keeps their failures independent -- an
// unparseable cpuN line must not cost the host its aggregate utilisation --
// and keeps the merge unambiguous, since no two collectors write the same
// field.
//
// Values are percentages over the interval between two scrapes, so the first
// scrape after start produces nothing.
type PerCoreCPU struct {
	procRoot string
	interval time.Duration
	prev     map[uint32]cpuTimes
}

// NewPerCoreCPU builds a per-core CPU collector reading from procRoot
// (normally "/proc").
func NewPerCoreCPU(procRoot string, interval time.Duration) *PerCoreCPU {
	return &PerCoreCPU{procRoot: procRoot, interval: interval}
}

// Name implements Collector.
func (p *PerCoreCPU) Name() string { return "percpu" }

// Interval implements Collector.
func (p *PerCoreCPU) Interval() time.Duration { return p.interval }

// SetProcRootForTest repoints the collector at a different fixture tree so a
// test can simulate the passage of time between two scrapes.
func (p *PerCoreCPU) SetProcRootForTest(root string) { p.procRoot = root }

// Collect implements Collector.
func (p *PerCoreCPU) Collect(_ context.Context) (*Result, error) {
	cur, err := p.read()
	if err != nil {
		return nil, err
	}

	prev := p.prev
	p.prev = cur

	if prev == nil {
		// No baseline yet: report nothing rather than invent a value.
		return &Result{}, nil
	}

	// Sorted so the rows are deterministic. The hub keys on
	// (host_id, ts, core) and does not care about order, but a stable one
	// makes failures and logs readable.
	ids := make([]uint32, 0, len(cur))
	for id := range cur {
		ids = append(ids, id)
	}
	slices.Sort(ids)

	ts := time.Now().UnixMilli()
	cores := make([]*netrav1.CpuCoreSample, 0, len(ids))

	for _, id := range ids {
		c := cur[id]
		q, ok := prev[id]
		if !ok {
			// A core that appeared this scrape has no interval to average
			// over. It reports from the next scrape onwards.
			continue
		}

		totalDelta := c.total() - q.total()
		if c.total() < q.total() || totalDelta == 0 || c.busy() < q.busy() {
			// Counters went backwards (reboot, or the core was offlined and
			// brought back with its counters reset) or did not move. Emit no
			// row rather than a zero, which would read as a genuinely idle
			// core.
			continue
		}

		busy := float64(c.busy()-q.busy()) / float64(totalDelta) * 100
		cores = append(cores, &netrav1.CpuCoreSample{
			TsMs: ts,
			Core: id,
			Busy: &busy,
		})
	}

	return &Result{Cores: cores}, nil
}

// read returns the cpuN lines of /proc/stat keyed by core number, skipping the
// bare "cpu" aggregate line that the CPU collector owns.
func (p *PerCoreCPU) read() (map[uint32]cpuTimes, error) {
	path := filepath.Join(p.procRoot, "stat")
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	out := make(map[uint32]cpuTimes)

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 8 || !strings.HasPrefix(fields[0], "cpu") {
			continue
		}
		// "cpu" alone is the aggregate; only "cpuN" is a core.
		suffix := strings.TrimPrefix(fields[0], "cpu")
		if suffix == "" {
			continue
		}
		id, err := strconv.ParseUint(suffix, 10, 32)
		if err != nil {
			// Not a core line despite the prefix. Skip it rather than fail
			// the whole read: one odd line must not cost every other core.
			continue
		}

		values := make([]uint64, 0, 10)
		for _, raw := range fields[1:] {
			v, err := strconv.ParseUint(raw, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("parse %s %s: %w", path, fields[0], err)
			}
			values = append(values, v)
		}
		for len(values) < 10 {
			values = append(values, 0)
		}

		out[uint32(id)] = cpuTimes{
			user: values[0], nice: values[1], system: values[2], idle: values[3],
			iowait: values[4], irq: values[5], softirq: values[6], steal: values[7],
			guest: values[8], guestNice: values[9],
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	return out, nil
}
