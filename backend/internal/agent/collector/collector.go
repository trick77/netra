// Package collector reads host metrics from kernel interfaces.
//
// Every collector is independent: one that cannot run reports why and is
// skipped, and the agent keeps posting everything else.
//
// There is ONE cadence. The scrape loop runs every collector on every tick, at
// the fixed config.ScrapeInterval, and a collector that needs to run less often
// gates ITSELF -- Smart against its own interval, so smartctl does not spin up
// sleeping drives once a minute, and Packages against the database's mtime with
// a daily floor. The interface carried an Interval() method for a while that
// nothing ever read; it described a schedule that did not exist, which is worse
// than no schedule at all. Self-gating is only safe for a collector that writes
// its own table and leaves no host_samples column NULL by skipping.
package collector

import "context"

// Collector fills in the fields of a HostSample it is responsible for.
//
// A Collector must leave a field unset when the underlying subsystem is
// absent or the value cannot be computed. An unset field becomes SQL NULL,
// which is a different fact from zero.
type Collector interface {
	// Name identifies the collector in logs and in collector_samples.
	Name() string

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

// InventoryResender is implemented by collectors that report a WHOLE SET, or a
// state-change EVENT, only when something changed -- rather than a value every
// scrape.
//
// Those collectors advance their own "last reported" state at collect time and
// then say nothing until something actually changes. That is right while every
// scrape reaches the hub, and silently wrong when one does not: if the scrape
// carrying the set is dropped -- the ring overflowing during a long outage, or
// the whole buffer being discarded after the hub rejects the token -- the
// change is gone. The collector believes it reported it, so it will not report
// it again. Packages would wait out its daily floor; Addresses would wait for
// an address to change, which on a static host is never. Systemd and Mdraid
// are event-based precisely so that the LAST event is the state, so a dropped
// "went failed" or "went degraded" leaves the hub serving the healthy state
// forever. The hub cannot recover either: it stores inventory by replacement
// and returns early on an empty set, so it keeps serving the row it already
// had.
//
// The agent therefore tells these collectors when a buffered scrape was lost,
// and they re-arm: the next Collect reports the current set again whether or
// not it changed.
type InventoryResender interface {
	// ResendInventory discards the "already reported" state, so the next
	// Collect emits the full current set.
	ResendInventory()
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

// StartupSummarizer is implemented by collectors that can say, in one line,
// WHAT THEY ACTUALLY SAW on their first run.
//
// A capability answers "can this collector run", which is only useful once
// something already suspects it cannot. This answers the question an operator
// asks in the first ten seconds after `docker compose up -d` -- "is it seeing
// my machine?" -- and answers it with counts rather than a verdict, because a
// count is falsifiable against what the operator already knows. A host with
// thirty containers whose agent reports "0 cgroup scopes, 30 containers named
// by the docker socket" has diagnosed itself; the same host reporting nothing
// at all took months to notice.
//
// The interface is optional, and the summary is emitted ONCE, at startup, after
// priming. It is not a per-scrape channel: anything worth repeating belongs in
// a capability or a metric.
type StartupSummarizer interface {
	// StartupSummary returns a short human-readable line, or "" for nothing to
	// report. It must not repeat the collector's own name -- the caller logs
	// that alongside it.
	StartupSummary() string
}
