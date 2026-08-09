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

// Memory reports host memory and swap from /proc/meminfo, plus the ZFS ARC
// size from the SPL kstat when ZFS is loaded.
type Memory struct {
	procRoot string
	interval time.Duration
}

// NewMemory builds a Memory collector reading from procRoot.
func NewMemory(procRoot string, interval time.Duration) *Memory {
	return &Memory{procRoot: procRoot, interval: interval}
}

// Name implements Collector.
func (m *Memory) Name() string { return "memory" }

// Interval implements Collector.
func (m *Memory) Interval() time.Duration { return m.interval }

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
