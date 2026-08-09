// Package collector reads host metrics from kernel interfaces.
//
// Every collector is independent: one that cannot run reports why and is
// skipped, and the agent keeps posting everything else.
package collector

import (
	"context"
	"time"
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

	// Collect reads the current values and returns them.
	//
	// It must return a nil Result alongside any error: the agent discards a
	// failed collector's contribution entirely, so a non-nil Result on the
	// error path would be silently dropped -- a confusing contract to leave
	// for the next collector author.
	//
	// Having nothing to report is not an error. A delta-based collector on
	// its first scrape returns an empty Result and no error, because "no
	// baseline yet" is a normal state rather than a fault to log on every
	// restart.
	Collect(ctx context.Context) (*Result, error)
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

// BaselineEmitter is implemented by collectors whose FIRST Collect returns
// data rather than a warm-up reading.
//
// The distinction exists because the agent PRIMES its collectors at startup
// and throws that first Result away (Client.Prime). For a delta-based
// collector that is exactly right: it has no previous reading, so its first
// Collect can only establish one. For an event-based collector it is data
// loss. mdraid and systemd both report a baseline on their first Collect --
// "here is what I found on arrival" -- and priming them would consume that
// baseline into a discarded scrape, set the previous state, and leave the
// first real scrape with nothing to say. A unit or an array that was already
// failed when the agent started would then produce no event at all, which is
// the one case the baseline exists for.
//
// Reported as a method rather than inferred from the Result, because Prime
// has to decide whether to call Collect AT ALL: by the time there is a Result
// to inspect, the state it would be asked about has already been consumed.
type BaselineEmitter interface {
	// EmitsBaseline reports whether this collector's first Collect carries
	// data that must not be discarded.
	EmitsBaseline() bool
}
