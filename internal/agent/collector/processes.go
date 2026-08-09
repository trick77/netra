package collector

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

// userHZ is the kernel's clock tick, the unit /proc/PID/stat reports CPU time
// in. It is 100 on every Linux architecture netra targets.
const userHZ = 100.0

// pageSize is the unit /proc/PID/stat field 24 (rss) counts in.
const pageSize = 4096

// topN is how many processes are reported per dimension. The union of
// top-N-by-CPU and top-N-by-memory, so a process that is heavy in either shows
// up -- reporting every process would be unbounded cardinality for data whose
// PIDs are meaningless a minute later.
const topN = 10

// procKey identifies a process across scrapes.
//
// The starttime half is what makes it correct. PIDs wrap, and when the kernel
// reuses one, the new process's total CPU minus the old one's is attributed to
// whatever the new process is called -- a garbage spike large enough that an
// operator would chase it. starttime differs on reuse, so the reused PID
// simply has no previous reading and reports nothing that scrape.
type procKey struct {
	pid       int
	startTime uint64
}

// procStat is the part of /proc/PID/stat this collector reads.
type procStat struct {
	name    string
	jiffies uint64
	rssPage uint64
}

// Processes reports per-name CPU and memory, aggregated across every process
// sharing a name.
//
// Requires pid: host. Without it the agent sees only its own PID namespace, so
// the numbers would describe the container rather than the host -- wrong data
// rather than missing data, which is why it reports a capability and no rows
// instead.
type Processes struct {
	procRoot string
	pidHost  bool
	interval time.Duration

	now func() time.Time

	prev   map[procKey]procStat
	prevAt time.Time
}

// NewProcesses builds a Processes collector. pidHost reports whether the agent
// was given the host's PID namespace.
func NewProcesses(procRoot string, pidHost bool, interval time.Duration) *Processes {
	return &Processes{procRoot: procRoot, pidHost: pidHost, interval: interval, now: time.Now}
}

// Name implements Collector.
func (p *Processes) Name() string { return "processes" }

// Interval implements Collector.
func (p *Processes) Interval() time.Duration { return p.interval }

// SetProcRootForTest repoints the collector at a different fixture tree.
func (p *Processes) SetProcRootForTest(root string) { p.procRoot = root }

// SetClockForTest replaces the clock used to measure the scrape interval.
func (p *Processes) SetClockForTest(fn func() time.Time) { p.now = fn }

// Capabilities implements CapabilityReporter.
func (p *Processes) Capabilities() map[string]string {
	if !p.pidHost {
		return map[string]string{"processes": "namespaced"}
	}
	return nil
}

// Collect implements Collector.
func (p *Processes) Collect(_ context.Context) (*Result, error) {
	if !p.pidHost {
		// Reporting the container's own processes as the host's would be
		// wrong rather than incomplete. The capability says why there is
		// nothing here.
		return &Result{}, nil
	}

	cur, err := p.read()
	if err != nil {
		return nil, err
	}

	prev, prevAt := p.prev, p.prevAt
	at := p.now()
	p.prev, p.prevAt = cur, at

	if prev == nil {
		return &Result{}, nil
	}

	elapsed := at.Sub(prevAt).Seconds()
	if elapsed <= 0 {
		return &Result{}, nil
	}

	// Aggregate by name: twenty nginx workers are one row whose count is
	// twenty, not twenty rows.
	type agg struct {
		jiffies uint64
		rssPage uint64
		count   uint32
		// measurable records that at least one process under this name had a
		// baseline, so a CPU percentage means something. Without it a name
		// whose processes are all new would report 0%, which reads as an idle
		// process rather than an unmeasured one.
		measurable bool
	}
	byName := make(map[string]*agg)

	for key, c := range cur {
		a := byName[c.name]
		if a == nil {
			a = &agg{}
			byName[c.name] = a
		}

		// Memory and count are GAUGES: both are true of this instant and need
		// no previous reading. Skipping them alongside the CPU delta made a
		// churn-heavy host -- a CI runner, anything cron-driven -- systematically
		// under-report how much memory its processes held and how many there
		// were, because the newest ones contributed nothing at all.
		a.rssPage += c.rssPage
		a.count++

		q, ok := prev[key]
		if !ok {
			// New process, or a PID reused for a different one -- the
			// starttime in the key is what tells those apart from a process
			// that has simply been running. Either way there is no interval
			// to compute a CPU rate over yet.
			continue
		}
		if c.jiffies < q.jiffies {
			// CPU time cannot decrease for one process. If it appears to, the
			// reading is not comparable; skip rather than report a negative.
			continue
		}

		a.jiffies += c.jiffies - q.jiffies
		a.measurable = true
	}

	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	slices.Sort(names)

	ts := at.UnixMilli()
	rows := make([]*netrav1.ProcessSample, 0, len(names))
	for _, name := range names {
		a := byName[name]
		row := &netrav1.ProcessSample{
			TsMs:     ts,
			Name:     name,
			MemBytes: ptrTo(a.rssPage * pageSize),
			Count:    ptrTo(a.count),
		}
		if a.measurable {
			row.CpuPct = ptrTo(float64(a.jiffies) / userHZ / elapsed * 100)
		}
		rows = append(rows, row)
	}

	return &Result{Processes: topByCPUAndMemory(rows)}, nil
}

// topByCPUAndMemory returns the union of the top-N by CPU and the top-N by
// memory, so a process heavy in either dimension is reported. Taking the top-N
// by one alone would hide a memory hog that uses no CPU, which is the case an
// operator most often goes looking for.
func topByCPUAndMemory(rows []*netrav1.ProcessSample) []*netrav1.ProcessSample {
	if len(rows) <= topN {
		return rows
	}

	keep := make(map[string]bool, topN*2)

	byCPU := slices.Clone(rows)
	sort.SliceStable(byCPU, func(i, j int) bool {
		return byCPU[i].GetCpuPct() > byCPU[j].GetCpuPct()
	})
	for _, r := range byCPU[:topN] {
		keep[r.GetName()] = true
	}

	byMem := slices.Clone(rows)
	sort.SliceStable(byMem, func(i, j int) bool {
		return byMem[i].GetMemBytes() > byMem[j].GetMemBytes()
	})
	for _, r := range byMem[:topN] {
		keep[r.GetName()] = true
	}

	// Preserve the original (name-sorted) order so the output is stable.
	out := make([]*netrav1.ProcessSample, 0, len(keep))
	for _, r := range rows {
		if keep[r.GetName()] {
			out = append(out, r)
		}
	}
	return out
}

// read walks the numeric directories of /proc and returns each process's
// stat fields, keyed by (pid, starttime).
func (p *Processes) read() (map[procKey]procStat, error) {
	entries, err := os.ReadDir(p.procRoot)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", p.procRoot, err)
	}

	out := make(map[procKey]procStat)

	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue // not a process directory
		}

		// NEVER cmdline or environ: both carry secrets passed on command
		// lines, and argv_guard_test.go fails the build if either name
		// appears in a Go string literal. stat's comm is the accepted cost --
		// truncated to 15 characters, and "python3" rather than the script.
		data, err := os.ReadFile(filepath.Join(p.procRoot, e.Name(), "stat"))
		if err != nil {
			// A process that exited between the listing and this read. Normal
			// at any moment on a busy host, and not worth failing the scrape.
			continue
		}

		st, startTime, ok := parseProcStat(string(data))
		if !ok {
			continue
		}
		out[procKey{pid: pid, startTime: startTime}] = st
	}

	return out, nil
}

// parseProcStat pulls comm, utime+stime, starttime and rss out of a
// /proc/PID/stat line.
//
// The comm field is parenthesised and may itself contain spaces and
// parentheses, so the fields after it are found from the LAST ')' rather than
// by splitting the whole line -- a process named "(evil) (thing)" would
// otherwise shift every subsequent field.
func parseProcStat(line string) (procStat, uint64, bool) {
	open := strings.IndexByte(line, '(')
	close := strings.LastIndexByte(line, ')')
	if open < 0 || close < 0 || close < open {
		return procStat{}, 0, false
	}

	name := line[open+1 : close]
	fields := strings.Fields(line[close+1:])
	// state(0) .. rss(21): 22 fields follow comm.
	if len(fields) < 22 {
		return procStat{}, 0, false
	}

	utime, err1 := strconv.ParseUint(fields[11], 10, 64)
	stime, err2 := strconv.ParseUint(fields[12], 10, 64)
	startTime, err3 := strconv.ParseUint(fields[19], 10, 64)
	rss, err4 := strconv.ParseUint(fields[21], 10, 64)
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
		return procStat{}, 0, false
	}

	return procStat{name: name, jiffies: utime + stime, rssPage: rss}, startTime, true
}
