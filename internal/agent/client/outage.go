package client

import (
	"encoding/json"
	"time"

	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
)

// EventTypeHub is the event type an agent uses to report its own delivery.
const EventTypeHub = "hub"

// outage is one run of consecutive failed flushes.
//
// A failed delivery used to be a WARNING on the fleet and host pages, derived
// from post_failures_total's increase across whatever range the reader had
// selected. That was wrong twice over. The agent buffers what it could not
// send and replays it the moment the hub returns, so in the ordinary case
// nothing is lost and there is nothing to act on -- the UI comment saying so
// sat directly above the code raising the warning. And a condition computed
// from a windowed counter delta appears and disappears as the reader changes
// the range, which makes it a statement about the reader rather than about the
// host.
//
// What actually happened is an event: the hub was unreachable from here, for
// this long, and this is whether it cost anything.
type outage struct {
	// startedAt is when the first flush of this run failed, and the zero value
	// when there is no outage in progress.
	startedAt time.Time
	// failures counts flush attempts that failed during it.
	failures uint64
	// droppedAtStart is ring.Dropped() when the outage began, so overflow
	// during it is a delta rather than a lifetime total.
	droppedAtStart uint64
	// discarded counts scrapes thrown away by a path that gives up on them
	// rather than by the ring overflowing: a revoked token dumping the buffer,
	// or a body the hub will never accept. The ring's own counter cannot see
	// those -- they leave through AckThrough, which is the same call a success
	// makes.
	discarded uint64
	// reason is what this run should be CALLED, once it ends.
	//
	// Empty means an ordinary unreachable hub, which is the common case and
	// needs nothing recorded. The two paths that give up on samples set it,
	// because "the hub was away and came back" is a different sentence with a
	// different fix -- and because a run that says "unreachable" about a hub
	// that answered every request is simply wrong.
	reason string
}

// Reasons a run of failed deliveries ends. Ordered by how much they need a
// human: a rejected token outranks a refused body, which outranks a hub that
// was merely away, and a run reports the worst thing that happened in it.
const (
	reasonUnreachable   = "unreachable"
	reasonRejectedBody  = "rejected"
	reasonTokenRejected = "token-rejected"
)

func reasonRank(reason string) int {
	switch reason {
	case reasonTokenRejected:
		return 2
	case reasonRejectedBody:
		return 1
	default:
		return 0
	}
}

// noteReason records why this run is worse than an ordinary outage, keeping
// whichever reason needs a human most.
func (c *Client) noteReason(reason string) {
	if reasonRank(reason) > reasonRank(c.outage.reason) {
		c.outage.reason = reason
	}
}

func (c *Client) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// beginOutage records the first failure of a run. Later failures only count.
func (c *Client) beginOutage() {
	if c.outage.startedAt.IsZero() {
		c.outage.startedAt = c.clock()
		c.outage.droppedAtStart = c.ring.Dropped()
	}
	c.outage.failures++
}

// hubDetail is what lands in a hub event's detail.
type hubDetail struct {
	// Severity is read by both event views before anything else, which is what
	// makes this render correctly with no new severity logic in either.
	Severity string `json:"severity"`
	// Reason distinguishes the two ways deliveries stop, which need different
	// sentences: "unreachable" is the hub being away, "token-rejected" is a
	// configuration error that will not fix itself.
	Reason string `json:"reason"`
	// OutageMs is how long deliveries were failing.
	OutageMs int64 `json:"outage_ms"`
	// Failures is how many flush attempts failed in that time.
	Failures uint64 `json:"failures"`
	// Lost is scrapes that will never reach the hub -- ring overflow plus
	// anything a give-up path discarded. Omitted when none, so the ordinary
	// recovery carries no evidence of machinery.
	Lost uint64 `json:"lost,omitempty"`
}

// endOutage emits one event for the run that just finished and clears it.
//
// Called ONLY from the successful-flush path, and that is not a convenience --
// it is the only place an event can be written and survive. A 401 dumps the
// whole ring (AckThrough(math.MaxUint64)), so an event emitted at that moment
// goes into the next scrape and is thrown away by the next 401 five minutes
// later. The comment in that branch already said as much about inventory: "a
// set emitted here would go straight into the ring and be dumped again on the
// next attempt". The same is true of this event, which is why the reason is
// LATCHED there and reported here.
//
// The cost is honest and small: a token that is never fixed produces no event,
// because the agent has no way to deliver one. That host goes silent, and the
// hub says so on its own.
//
// Severity is decided by outcome, not by the fact of failing: an outage the
// ring absorbed entirely is `info`, because the samples arrived and the only
// thing that happened is that they arrived late. One that cost samples is
// `critical`, because that is a hole in this host's history that nothing can
// fill. Since the events page opens at warning and above, a hub restart during
// a deploy sits quietly below the fold and a lossy outage does not.
func (c *Client) endOutage() {
	if c.outage.startedAt.IsZero() {
		return
	}

	reason := c.outage.reason
	if reason == "" {
		reason = reasonUnreachable
	}

	lost := c.ring.Dropped() - c.outage.droppedAtStart + c.outage.discarded
	severity := "info"
	if lost > 0 {
		severity = "critical"
	}
	// A rejected token is never merely late: the buffer was dumped and an
	// operator has to act, so it says so even when the arithmetic above found
	// nothing to count.
	if reason == reasonTokenRejected && severity == "info" {
		severity = "critical"
	}

	now := c.clock()
	detail := hubDetail{
		Severity: severity,
		Reason:   reason,
		OutageMs: now.Sub(c.outage.startedAt).Milliseconds(),
		Failures: c.outage.failures,
		Lost:     lost,
	}
	c.outage = outage{}

	body, err := json.Marshal(detail)
	if err != nil {
		// A struct of strings and integers cannot fail to marshal; if it
		// somehow does, that the outage happened is worth more than its
		// numbers.
		body = []byte(`{"severity":"info"}`)
	}
	sev := severity
	c.pendingEvents = append(c.pendingEvents, &netrav1.Event{
		TsMs: now.UnixMilli(),
		Type: EventTypeHub,
		// No subject: this is about the host as a whole, which is what the
		// proto reserves the empty string for.
		Severity:   &sev,
		DetailJson: string(body),
	})
}

// takePendingEvents hands over the client's own events and clears them.
func (c *Client) takePendingEvents() []*netrav1.Event {
	if len(c.pendingEvents) == 0 {
		return nil
	}
	out := c.pendingEvents
	c.pendingEvents = nil
	return out
}
