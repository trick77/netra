package collector

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
)

// Memory reports host memory and swap from /proc/meminfo, plus the ZFS ARC
// size from the SPL kstat when ZFS is loaded.
type Memory struct {
	procRoot string
}

// NewMemory builds a Memory collector reading from procRoot.
func NewMemory(procRoot string) *Memory {
	return &Memory{procRoot: procRoot}
}

// Name implements Collector.
func (m *Memory) Name() string { return "memory" }

// Collect implements Collector.
func (m *Memory) Collect(_ context.Context) (*Result, error) {
	sample := &netrav1.HostSample{}

	values, err := m.readMeminfo()
	if err != nil {
		return nil, err
	}

	total, hasTotal := values["MemTotal"]
	available, hasAvailable := values["MemAvailable"]
	buffers := values["Buffers"]
	cached := values["Cached"]

	if hasTotal {
		v := total
		sample.MemTotal = &v
	}
	if hasAvailable {
		v := available
		sample.MemAvailable = &v
	}
	if hasTotal && hasAvailable && total >= available {
		v := total - available
		sample.MemUsed = &v
	}
	if buffers+cached > 0 {
		v := buffers + cached
		sample.MemBuffcache = &v
	}

	// The fields a stacked memory chart needs. mem_used above is
	// MemTotal - MemAvailable, which already contains the ZFS ARC and the
	// unreclaimable shmem pages, so bands cannot be stacked on top of it
	// without counting the same bytes twice. Reported separately, they let a
	// chart partition MemTotal and derive "used" as the remainder instead.
	//
	// Each is read with the two-value form: an absent key means the kernel
	// does not report that field, which must reach the database as NULL. The
	// zero-value reads above are the older, looser style and are left alone
	// because mem_buffcache's meaning depends on them summing.
	if free, ok := values["MemFree"]; ok {
		sample.MemFree = &free
	}
	if v, ok := values["Buffers"]; ok {
		sample.MemBuffers = &v
	}
	// Shmem is counted inside Cached, so the two are separated here rather
	// than in the reader: storing both verbatim would double-count tmpfs in
	// any stack built from them. Guarded the same way mem_used guards
	// total >= available -- an unexpected ordering reports nothing rather
	// than an underflowed number.
	if shmem, ok := values["Shmem"]; ok {
		sample.MemShared = &shmem
		if cached >= shmem {
			v := cached - shmem
			sample.MemCached = &v
		}
	} else if cached > 0 {
		// No Shmem line (very old kernels): Cached is already the whole
		// cache, with nothing to subtract.
		v := cached
		sample.MemCached = &v
	}
	if v, ok := values["SReclaimable"]; ok {
		sample.MemSreclaimable = &v
	}

	// Swap absent is not swap empty. A SwapTotal of zero means the host has
	// no swap configured, so both fields stay unset and reach the hub as NULL.
	if swapTotal, ok := values["SwapTotal"]; ok && swapTotal > 0 {
		v := swapTotal
		sample.SwapTotal = &v
		if swapFree, ok := values["SwapFree"]; ok && swapTotal >= swapFree {
			used := swapTotal - swapFree
			sample.SwapUsed = &used
		}
	}

	if arc, ok := m.readZfsArc(); ok {
		sample.MemZfsArc = &arc
	}

	return &Result{Host: sample}, nil
}

// readMeminfo returns every "Key: value kB" line converted to bytes.
func (m *Memory) readMeminfo() (map[string]uint64, error) {
	path := filepath.Join(m.procRoot, "meminfo")
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	out := make(map[string]uint64, 16)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		key, rest, ok := strings.Cut(scanner.Text(), ":")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		v, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			continue
		}
		// meminfo reports kB for everything except a few counters; the keys
		// netra reads are all kB.
		out[key] = v * 1024
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	return out, nil
}

// readZfsArc returns the ARC size in bytes, and false when ZFS is not loaded.
func (m *Memory) readZfsArc() (uint64, bool) {
	path := filepath.Join(m.procRoot, "spl", "kstat", "zfs", "arcstats")
	f, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 3 || fields[0] != "size" {
			continue
		}
		v, err := strconv.ParseUint(fields[2], 10, 64)
		if err != nil {
			return 0, false
		}
		return v, true
	}
	return 0, false
}
