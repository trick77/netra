package collector

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
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
	prev     map[uint32]cpuTimes
}

// NewPerCoreCPU builds a per-core CPU collector reading from procRoot
// (normally "/proc").
func NewPerCoreCPU(procRoot string) *PerCoreCPU {
	return &PerCoreCPU{procRoot: procRoot}
}

// Name implements Collector.
func (p *PerCoreCPU) Name() string { return "percpu" }

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

	// Skipped lines are counted and reported ONCE per read rather than warned
	// about individually. A malformed /proc/stat -- an emulated or lxcfs tree,
	// say -- is malformed on every scrape, so a warning per line per scrape is
	// an unbounded log every 60s for the life of the agent, saying the same
	// thing each time.
	var skipped int
	var firstSkip string
	var firstSkipErr error

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 || !strings.HasPrefix(fields[0], "cpu") {
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

		times, err := parseCPUTimes(fields[1:])
		if err != nil {
			// Skipped, not fatal -- the same rule as the id parse above, and
			// for the same reason: one odd line must not cost every other
			// core its reading. This used to fail the whole read for a short
			// line, which is exactly the coupling splitting CPU and
			// PerCoreCPU into two collectors exists to avoid.
			skipped++
			if firstSkipErr == nil {
				firstSkip, firstSkipErr = fields[0], err
			}
			continue
		}
		out[uint32(id)] = times
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	if skipped > 0 {
		slog.Warn("skipped unparseable cpu lines",
			"path", path, "skipped", skipped, "cores", len(out),
			"first", firstSkip, "err", firstSkipErr)
	}

	return out, nil
}
