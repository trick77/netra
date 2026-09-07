package conditions

import (
	"context"
	"log/slog"
	"time"
)

// Interval is how often the evaluator looks.
//
// The agent's scrape cadence, because nothing it reads can change faster than
// that: host_current, filesystem_current and systemd_units are all written by
// ingest. A faster tick would re-read the same rows and decide the same thing.
const Interval = 60 * time.Second

// Store is what the evaluator needs from the database.
//
// An interface so the loop -- warm-up, ticking, error handling -- is testable
// against a fake, without a database and without waiting out a minute.
type Store interface {
	// ScanConditions is given the keys already open, and it is not an
	// optimisation.
	//
	// The disk onset is a walk back through the mount's own series to the first
	// sample over the threshold, and 0016 is explicit that it can be done ONCE:
	// raw retention is 7 days and every aggregate is materialized_only, so a
	// walk attempted later reaches a different distance and returns a different
	// answer. A scan that could not tell an already-open condition from a new
	// one would either re-walk on every tick -- computing an answer it then
	// discards, and a different one each week -- or skip the walk entirely.
	//
	// `since` is the earliest instant this hub PROCESS can vouch for. A rate
	// counted over buckets the hub was not running for is a measurement of the
	// hub's own downtime, not of the host: the agent's ring buffers an hour by
	// default (AGENT_BUFFER_WINDOW), so a longer outage leaves a hole no
	// replay fills, identically on every host. Without the clamp the first
	// pass after a two-hour outage finds a third of the window empty
	// fleet-wide and opens `sporadic` on all of it -- the same "one hub outage
	// recorded as N host outages" WarmUp exists to prevent, arriving through
	// the other door and outliving any warm-up, because the hole sits in the
	// window for as long as the window is wide.
	ScanConditions(ctx context.Context, now time.Time, open map[Key]bool, since time.Time) (Scan, error)
	OpenConditions(ctx context.Context) ([]Open, error)
	ApplyConditions(ctx context.Context, actions []Action, now time.Time) error
}

// Evaluator keeps host_conditions in step with what the hub can see.
type Evaluator struct {
	store Store
	// now is a seam so a test can drive the warm-up without sleeping.
	now func() time.Time
	// startedAt is when this process began, which the warm-up is measured
	// against.
	startedAt time.Time
	// interval is the tick. In production it is always Interval; it is a field
	// only so a test can watch the loop without waiting a minute for it, the
	// same escape hatch the agent's client keeps for its own ticker. Nothing
	// mutates it after construction.
	interval time.Duration
}

// New builds an Evaluator that began running at startedAt.
func New(store Store, startedAt time.Time) *Evaluator {
	return &Evaluator{
		store:     store,
		now:       time.Now,
		startedAt: startedAt,
		interval:  Interval,
	}
}

// SetClockForTest drives the evaluator's sense of time.
func (e *Evaluator) SetClockForTest(now func() time.Time) { e.now = now }

// SetIntervalForTest drives the loop faster than a minute.
func (e *Evaluator) SetIntervalForTest(d time.Duration) { e.interval = d }

// WarmUp is how long after start-up the evaluator declines to judge silence.
//
// THE bug this prevents, and it is not subtle. When the hub restarts, every
// host's last_seen is as old as the downtime, so the first pass would open
// `silent` for the entire fleet -- and then close it again two ticks later as
// the agents' buffered scrapes land. One hub outage would be recorded as N
// host outages, permanently, in the log alerting reads.
//
// Today's stateless UI flickers exactly the same way and records nothing,
// which is why nobody has had to think about it. Writing it down is what makes
// it matter.
//
// StaleAfter alone is exactly the wrong length, because it leaves no margin
// over the thing it is guarding.
//
// After an outage an agent does not reconnect instantly: its flush backoff
// doubles to a 60s cap and it waits backoff plus jitter, and a hub-supplied
// retry_after is adopted up to maxAdoptedRetryAfter -- ten minutes
// (agent/client/client.go). Add a scrape tick of alignment and a host can
// legitimately not post until well past three minutes, at which point a
// warm-up of exactly StaleAfter would open `silent` on it and clear it again
// once it lands. That is the same "one hub outage recorded as N host outages"
// this exists to prevent, just quieter.
//
// So: the agent's own ceiling, plus the staleness window, plus one tick of
// slack. Erring long costs a few minutes of not reporting silence after a
// restart; erring short writes false history that nothing deletes.
const WarmUp = 10*time.Minute + StaleAfter + Interval

// Run evaluates on a ticker until the context ends.
func (e *Evaluator) Run(ctx context.Context) {
	ticker := time.NewTicker(e.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := e.Once(ctx); err != nil {
				// Logged and continued: a pass that failed is one missed
				// evaluation, and the next is a minute away. Stopping the loop
				// would mean a transient database error silently ends
				// condition tracking for the life of the process.
				slog.Error("condition evaluation failed", "err", err)
			}
		}
	}
}

// Once runs a single evaluation pass.
func (e *Evaluator) Once(ctx context.Context) error {
	now := e.now()

	// Open first, so the scan knows which conditions already have an onset and
	// does not walk one back a second time -- see Store.ScanConditions.
	open, err := e.store.OpenConditions(ctx)
	if err != nil {
		return err
	}
	openKeys := make(map[Key]bool, len(open))
	for _, o := range open {
		openKeys[o.Key] = true
	}

	scan, err := e.store.ScanConditions(ctx, now, openKeys, e.startedAt)
	if err != nil {
		return err
	}

	// During warm-up the hub cannot tell a host that stopped talking from a
	// host whose backlog has not arrived yet, so it declines to say. The kind
	// is dropped from BOTH halves: out of Bad so nothing opens, and out of
	// Evaluated so nothing already open is resolved either.
	//
	// `sporadic` is guarded differently and not here -- see the `since`
	// argument to ScanConditions. Dropping it for a fixed warm-up would only
	// DELAY the same damage: its window is three hours, so a hub outage leaves
	// a hole that is still there long after any warm-up expires. Clamping the
	// window to what this process can vouch for fixes it at the source, and
	// the span floor then declines to judge until there is enough window to
	// judge over.
	if now.Sub(e.startedAt) < WarmUp {
		delete(scan.Evaluated, KindSilent)
		for key := range scan.Bad {
			if key.Kind == KindSilent {
				delete(scan.Bad, key)
			}
		}
	}

	actions := Diff(open, scan, now)
	if len(actions) == 0 {
		return nil
	}
	return e.store.ApplyConditions(ctx, actions, now)
}
