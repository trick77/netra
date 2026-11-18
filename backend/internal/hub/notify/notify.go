// Package notify tells people about what the conditions evaluator and the
// agents' critical events found: one message per host per tick, through
// whatever channels BACKEND_NOTIFY_URLS names.
//
// Conditions are driven by state in host_conditions (notified_ts and
// friends, 0027), not by a hook on the evaluator: a send that fails stays
// pending and is retried on the next tick, and a restart neither repeats nor
// forgets a message. Critical events are occurrences with no state to hang
// that on, so they are read from a short freshness window instead and
// de-duplicated in memory.
//
// Every message is also written to the events log (EventType), listing the
// alerts it carried: the record of what went out, readable in the UI.
//
// No flap damping beyond the evaluator's own: a condition opens on the first
// bad observation and clears after two good ones (conditions.clearAfter), the
// deviation kinds need three minutes out of band, and disk has free-bytes
// floors. A predicate that still flaps sends an OPEN/CLEARED pair per cycle;
// muting the kind is the answer the UI offers.
package notify

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"time"
)

// Interval is how often the notifier looks. The evaluator's own tick: nothing
// it reads changes faster.
const Interval = 60 * time.Second

// Freshness is how old a critical event may be and still notify.
//
// An agent that lost the hub buffers up to an hour of scrapes and replays
// them on reconnect. Without this window every disk error from that hour
// would arrive as news. The price is that an event delivered later than this
// never notifies; it is still in the events log.
//
// Measured on the event's own ts, the agent's clock: events carry no receipt
// time, and correcting by host_current's received_at - last_seen does not
// work either, because during a replay last_seen is the replayed (old) sample
// and the "offset" would make the whole buffered hour look fresh. A host
// whose clock runs more than this slow never notifies events; NTP is assumed.
const Freshness = 15 * time.Minute

// Cooldown is the minimum gap between two messages about the same
// (host, type, subject) event. A dying disk logs errors for hours; one
// message an hour says so without burying everything else.
const Cooldown = time.Hour

// parallelSends bounds how many hosts' messages are in flight at once. A
// channel that hangs costs SendTimeout per message; one after another, a
// fleet-wide outage (every host silent) would stretch a pass far past its
// minute and age events out of Freshness before they were sent.
const parallelSends = 8

// What is the reason a condition is in a message.
type What string

// The reasons a condition is notified about.
const (
	Opened    What = "opened"
	Escalated What = "escalated"
	Reminder  What = "reminder"
	Cleared   What = "cleared"
)

// Store is what the notifier needs from the database.
type Store interface {
	// PendingConditions returns every condition with something owed:
	// unannounced or escalated open ones, open ones due a reminder (repeat
	// zero means never), and announced ones that have since resolved.
	PendingConditions(ctx context.Context, now time.Time, repeat time.Duration) ([]Condition, error)
	// MarkNotified records that these conditions have been dealt with.
	MarkNotified(ctx context.Context, items []Condition, now time.Time) error
	// CriticalEventsSince returns the critical events newer than since,
	// condition transitions excluded (those are announced from the condition
	// itself).
	CriticalEventsSince(ctx context.Context, now, since time.Time) ([]Event, error)
	// PruneMutes deletes mutes that have expired.
	PruneMutes(ctx context.Context, now time.Time) error
	// RecordNotification writes one message's EventType row.
	RecordNotification(ctx context.Context, hostID int32, ts time.Time, severity string, n Notification) error
}

// Sender delivers one message to every configured channel.
type Sender interface {
	Send(title, body string) error
}

type eventKey struct {
	hostID  int32
	typ     string
	subject string
}

// Notifier turns pending conditions and fresh critical events into messages.
type Notifier struct {
	store  Store
	sender Sender
	hubURL string
	repeat time.Duration

	now      func() time.Time
	interval time.Duration

	// seen holds the events already dealt with, by id, until they leave the
	// freshness window. In memory only: after a restart the window is primed
	// (see Prime), not replayed.
	seen map[int32]time.Time
	// primed is false until Prime has succeeded. Events are not handled
	// before that: an empty seen map would resend the previous process's
	// last fifteen minutes.
	primed bool
	// startedAt is when Prime was first attempted: the moment this process
	// took over. A prime that only succeeds later marks no event after it.
	startedAt time.Time
	// lastSent is when each (host, type, subject) last went out, for Cooldown.
	lastSent map[eventKey]time.Time
	// failing holds, per host whose last message reached no channel, the
	// alerts already recorded as not delivered. A retry carrying nothing new
	// is not recorded again: one row per outage, not one a minute, plus one
	// for each alert that joins the message while it lasts.
	failing map[int32]map[Alert]bool
}

// New builds a Notifier. hubURL may be empty; messages then carry no link.
func New(store Store, sender Sender, hubURL string, repeat time.Duration) *Notifier {
	return &Notifier{
		store:    store,
		sender:   sender,
		hubURL:   hubURL,
		repeat:   repeat,
		now:      time.Now,
		interval: Interval,
		seen:     map[int32]time.Time{},
		lastSent: map[eventKey]time.Time{},
		failing:  map[int32]map[Alert]bool{},
	}
}

// SetClockForTest drives the notifier's sense of time.
func (n *Notifier) SetClockForTest(now func() time.Time) { n.now = now }

// Run notifies on a ticker until the context ends. Call Prime first, before
// the hub accepts ingest; Once retries it while it has not succeeded.
func (n *Notifier) Run(ctx context.Context) {
	ticker := time.NewTicker(n.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := n.Once(ctx); err != nil {
				// Logged and continued, as the evaluator does: the next tick
				// is a minute away and retries whatever is still pending.
				slog.Error("notify failed", "err", err)
			}
		}
	}
}

// Prime marks the events already inside the freshness window as seen.
//
// The events in the window at start-up were either announced by the process
// before this one or arrived while it was going down, and a restart must not
// send them again. main calls it before the listener starts, so an agent
// replaying after the restart lands after it and still counts.
//
// Only events up to the first attempt count: when the database was down at
// start-up and this succeeds a few ticks later, an event that arrived in
// between is news, not a leftover.
func (n *Notifier) Prime(ctx context.Context) error {
	now := n.now()
	if n.startedAt.IsZero() {
		n.startedAt = now
	}
	events, err := n.store.CriticalEventsSince(ctx, now, now.Add(-Freshness))
	if err != nil {
		return err
	}
	for _, e := range events {
		if !e.TS.After(n.startedAt) {
			n.seen[e.ID] = e.TS
		}
	}
	n.primed = true
	return nil
}

// hostBatch is everything one message about one host will say.
type hostBatch struct {
	hostID     int32
	hostname   string
	conditions []Condition
	events     []Event
	// done are the muted conditions that need marking without a message.
	done []Condition
	// sent is set once the message went out (or there was none to send).
	sent bool
	// outcome is the message's Delivered, Partial or Failed; empty when
	// there was no message. failed names the channels it failed on.
	outcome string
	failed  []string
	// lines are the events the message carried: one per (type, subject).
	lines []Event
}

// Once runs a single pass.
//
// The two reads are independent: a failing events query must not hold back
// a host going silent, nor a failing conditions query a disk error. Each
// failure is returned, joined, after whatever the other half could send.
func (n *Notifier) Once(ctx context.Context) error {
	now := n.now()

	if err := n.store.PruneMutes(ctx, now); err != nil {
		slog.Error("notify: prune mutes", "err", err)
	}

	var errs []error
	conds, err := n.store.PendingConditions(ctx, now, n.repeat)
	if err != nil {
		errs = append(errs, err)
	}
	// Not primed (the database was down at start-up): prime now and leave
	// events for the next tick.
	var events []Event
	if !n.primed {
		if err := n.Prime(ctx); err != nil {
			errs = append(errs, err)
		}
	} else if events, err = n.store.CriticalEventsSince(ctx, now, now.Add(-Freshness)); err != nil {
		errs = append(errs, err)
	}

	batches := n.batch(conds, events, now)
	n.send(batches, now)
	n.endOutages(batches)
	for _, b := range batches {
		n.record(ctx, b, now)
	}

	n.forget(now)
	return errors.Join(errs...)
}

// batch sorts what is owed into one batch per host, in host order.
func (n *Notifier) batch(conds []Condition, events []Event, now time.Time) []*hostBatch {
	byHost := map[int32]*hostBatch{}
	batchOf := func(hostID int32, hostname string) *hostBatch {
		b := byHost[hostID]
		if b == nil {
			b = &hostBatch{hostID: hostID, hostname: hostname}
			byHost[hostID] = b
		}
		return b
	}

	for _, c := range conds {
		switch {
		case !c.Muted:
			b := batchOf(c.HostID, c.Hostname)
			b.conditions = append(b.conditions, c)
		case c.What == Cleared:
			// A muted all-clear is dropped rather than held: the problem
			// it reports on is gone, and announcing it once the mute ends
			// would be news about nothing.
			b := batchOf(c.HostID, c.Hostname)
			b.done = append(b.done, c)
		}
		// Muted and still open: left pending, announced when the mute ends.
	}

	for _, e := range events {
		if _, ok := n.seen[e.ID]; ok {
			continue
		}
		key := eventKey{e.HostID, e.Type, e.Subject}
		if e.Muted || now.Sub(n.lastSent[key]) < Cooldown {
			n.seen[e.ID] = e.TS
			continue
		}
		b := batchOf(e.HostID, e.Hostname)
		b.events = append(b.events, e)
	}

	out := make([]*hostBatch, 0, len(byHost))
	for _, b := range byHost {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].hostID < out[j].hostID })
	return out
}

// send delivers every batch's message, several hosts at a time.
func (n *Notifier) send(batches []*hostBatch, now time.Time) {
	var wg sync.WaitGroup
	slots := make(chan struct{}, parallelSends)
	for _, b := range batches {
		b.lines = distinctEvents(b.events)
		if len(b.conditions) == 0 && len(b.lines) == 0 {
			b.sent = true
			continue
		}
		title, body := Render(b.hostname, b.hostID, b.conditions, b.lines, n.hubURL, now)
		wg.Add(1)
		slots <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			err := n.sender.Send(title, body)
			var sendErr *SendError
			if errors.As(err, &sendErr) {
				b.failed = sendErr.Failed
			}
			switch {
			case err == nil:
				b.sent, b.outcome = true, Delivered
			case sendErr != nil && sendErr.Partial:
				slog.Warn("notify: send", "host", b.hostname, "err", err)
				b.sent, b.outcome = true, Partial
			default:
				// Everything stays pending for the next tick.
				slog.Error("notify: send", "host", b.hostname, "err", err)
				b.outcome = Failed
			}
		}()
	}
	wg.Wait()
}

// record writes down what a sent batch covered, and logs the message.
func (n *Notifier) record(ctx context.Context, b *hostBatch, now time.Time) {
	n.logMessage(ctx, b, now)
	if !b.sent {
		return
	}
	marked := append(append([]Condition{}, b.conditions...), b.done...)
	if len(marked) > 0 {
		if err := n.store.MarkNotified(ctx, marked, now); err != nil {
			// The message went out and the mark did not stick, so the next
			// tick repeats it. Rare, and a duplicate beats a silence.
			slog.Error("notify: mark notified", "host", b.hostname, "err", err)
		}
	}
	for _, e := range b.events {
		n.seen[e.ID] = e.TS
		n.lastSent[eventKey{e.HostID, e.Type, e.Subject}] = now
	}
}

// logMessage writes the batch's message to the events log: every delivered
// one, and the first of a run of failures.
//
// Independent of MarkNotified, and its own failure only logged: a message
// that went out is not sent again because its record could not be written.
//
// Info when the message reached anyone, even with a channel down: a channel
// problem is not a problem with the host the record sits on. Warning only
// when nobody was told, which is.
func (n *Notifier) logMessage(ctx context.Context, b *hostBatch, now time.Time) {
	if b.outcome == "" {
		return
	}
	alerts := alertsOf(b.conditions, b.lines)
	severity := "info"
	if b.outcome == Failed {
		if !n.newlyUndelivered(b.hostID, alerts) {
			return
		}
		severity = "warning"
	}
	rec := notification(alerts, b.outcome, b.failed)
	if err := n.store.RecordNotification(ctx, b.hostID, now, severity, rec); err != nil {
		slog.Error("notify: record message", "host", b.hostname, "err", err)
	}
}

// newlyUndelivered reports whether a failed message carries an alert not yet
// recorded as undelivered, and remembers every alert it carries.
func (n *Notifier) newlyUndelivered(hostID int32, alerts []Alert) bool {
	seen := n.failing[hostID]
	if seen == nil {
		seen = map[Alert]bool{}
		n.failing[hostID] = seen
	}
	fresh := false
	for _, a := range alerts {
		if !seen[a] {
			seen[a] = true
			fresh = true
		}
	}
	return fresh
}

// endOutages forgets the failures of every host whose message did not fail
// this pass: it got through, or there was nothing left to send (the event
// aged out, the condition was muted). Either way the next failure there is a
// new outage and is recorded.
func (n *Notifier) endOutages(batches []*hostBatch) {
	failed := map[int32]bool{}
	for _, b := range batches {
		if b.outcome == Failed {
			failed[b.hostID] = true
		}
	}
	for id := range n.failing {
		if !failed[id] {
			delete(n.failing, id)
		}
	}
}

// distinctEvents keeps the first event per key: several events about one
// disk in one tick are one line.
func distinctEvents(events []Event) []Event {
	out := events[:0:0]
	seen := map[eventKey]bool{}
	for _, e := range events {
		key := eventKey{e.HostID, e.Type, e.Subject}
		if !seen[key] {
			seen[key] = true
			out = append(out, e)
		}
	}
	return out
}

// forget drops the in-memory state nothing can match any more.
func (n *Notifier) forget(now time.Time) {
	for id, ts := range n.seen {
		if now.Sub(ts) > Freshness {
			delete(n.seen, id)
		}
	}
	for key, at := range n.lastSent {
		if now.Sub(at) >= Cooldown {
			delete(n.lastSent, key)
		}
	}
}
