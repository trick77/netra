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
	ScanConditions(ctx context.Context, now time.Time) (Scan, error)
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

	scan, err := e.store.ScanConditions(ctx, now)
	if err != nil {
		return err
	}

	// During warm-up the hub cannot tell a host that stopped talking from a
	// host whose backlog has not arrived yet, so it declines to say. The kind
	// is dropped from BOTH halves: out of Bad so nothing opens, and out of
	// Evaluated so nothing already open is resolved either.
	if now.Sub(e.startedAt) < WarmUp {
		delete(scan.Evaluated, KindSilent)
		for key := range scan.Bad {
			if key.Kind == KindSilent {
				delete(scan.Bad, key)
			}
		}
	}

	open, err := e.store.OpenConditions(ctx)
	if err != nil {
		return err
	}

	actions := Diff(open, scan, now)
	if len(actions) == 0 {
		return nil
	}
	return e.store.ApplyConditions(ctx, actions, now)
}
