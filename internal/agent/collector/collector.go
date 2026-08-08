// Package collector reads host metrics from kernel interfaces.
//
// Every collector is independent: one that cannot run reports why and is
// skipped, and the agent keeps posting everything else.
package collector

import (
	"context"
	"time"

	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

// Collector fills in the fields of a HostSample it is responsible for.
//
// A Collector must leave a field unset when the underlying subsystem is
// absent or the value cannot be computed. An unset field becomes SQL NULL,
// which is a different fact from zero.
type Collector interface {
	// Name identifies the collector in logs and in collector_samples.
	Name() string

	// Interval is how often this collector should run. Slow collectors use a
	// longer interval than the scrape loop so they never stall it.
	Interval() time.Duration

	// Collect reads the current values and writes them into sample.
	Collect(ctx context.Context, sample *netrav1.HostSample) error
}

// CapabilityReporter is implemented by collectors whose ability to run is a
// fact the hub needs, not just a reason to skip a field.
//
// "processes is NULL" is ambiguous on its own: the host could be idle, the
// mount could be missing, or the agent could be confined to a PID namespace.
// A capability turns that into a stated reason. Capabilities ride the
// metadata hash rather than the sample, because they change on the order of
// deployments rather than of scrapes.
//
// The interface is optional: a collector that does not implement it reports
// nothing and is unaffected.
type CapabilityReporter interface {
	// Capabilities returns this collector's availability facts, keyed by a
	// stable name. An empty or nil map means "nothing to report".
	Capabilities() map[string]string
}
