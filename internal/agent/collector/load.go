package collector

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

// Load reports the 1/5/15 minute load averages and host uptime.
//
// Host uptime is deliberately here rather than in the agent's self-telemetry:
// host uptime and agent uptime are different facts, and conflating them hides
// an agent that is crash-looping on a machine that never rebooted.
type Load struct {
	procRoot string
	interval time.Duration
}

// NewLoad builds a Load collector reading from procRoot.
func NewLoad(procRoot string, interval time.Duration) *Load {
	return &Load{procRoot: procRoot, interval: interval}
}

// Name implements Collector.
func (l *Load) Name() string { return "load" }

// Interval implements Collector.
func (l *Load) Interval() time.Duration { return l.interval }

// Collect implements Collector.
func (l *Load) Collect(_ context.Context) (*Result, error) {
	sample := &netrav1.HostSample{}

	path := filepath.Join(l.procRoot, "loadavg")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	fields := strings.Fields(string(raw))
	if len(fields) < 3 {
		return nil, fmt.Errorf("malformed %s: %q", path, string(raw))
	}

	targets := []**float64{&sample.Load1, &sample.Load5, &sample.Load15}
	for i, target := range targets {
		v, err := strconv.ParseFloat(fields[i], 64)
		if err != nil {
			return nil, fmt.Errorf("parse %s field %d: %w", path, i, err)
		}
		value := v
		*target = &value
	}

	if up, ok := l.readUptime(); ok {
		sample.UptimeS = &up
	}

	return &Result{Host: sample}, nil
}

// readUptime returns whole seconds of host uptime, and false if unreadable.
func (l *Load) readUptime() (uint64, bool) {
	raw, err := os.ReadFile(filepath.Join(l.procRoot, "uptime"))
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(string(raw))
	if len(fields) == 0 {
		return 0, false
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || v < 0 {
		return 0, false
	}
	return uint64(v), true
}
