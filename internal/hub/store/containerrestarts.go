package store

import (
	"encoding/json"
	"fmt"
	"time"

	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
)

// A restart is an EVENT, not a column. See migration 0019 for why: the counter
// is a gauge, the question is a count, and no aggregate of a gauge answers it
// -- so the fleet's own 24h window could not be answered from the only tier
// that carries restart_count.
//
// This file is the derivation. It is deliberately pure: everything that decides
// WHETHER a restart happened, WHEN, and what to say about it takes values and
// returns values, so the rules can be tested without a database. The wiring
// that reads the previous row and writes the events lives in families.go, in
// one transaction with the upsert that moves the counter past them.

// Event types. Neither may be added to eventStateKeys in event_state.go: those
// are STATES, where a repeat says nothing new, and these are OCCURRENCES, where
// how often they repeat is the entire reading.
const (
	// EventContainerRestart is the counter going UP -- Docker restarted the
	// container in place, which means it died.
	EventContainerRestart = "container_restart"
	// EventContainerRecreate is the counter going DOWN, or a new start time
	// with an unchanged counter. Docker resets RestartCount for a new
	// container, and container_key is the compose service, so this is the same
	// service on a new container: a redeploy.
	EventContainerRecreate = "container_recreate"
)

// Where an event's timestamp came from, recorded in detail.ts_source so no
// reader mistakes one for the other.
const (
	// tsSourceStartedAt: Docker's own State.StartedAt for the new incarnation.
	// Exact to the second, even when the agent's rationed inspect noticed the
	// change up to ten scrapes late.
	tsSourceStartedAt = "docker_started_at"
	// tsSourceObserved: the sample the change was seen on. An UPPER BOUND --
	// the restart happened at or before this instant, never on it.
	tsSourceObserved = "observed"
)

// containerState is what the hub already knows about a container: the values
// stored on its row before this batch is applied.
//
// Count and StartedAt are pointers because absent is a third state, and the
// whole derivation turns on it: a delta measured from an unknown is not a
// reading, so a nil on either side of a step yields nothing at all.
type containerState struct {
	Count     *int64
	StartedAt *time.Time
	// LastSeen is the newest sample already stored. Samples at or before it
	// have been accounted for; see restartEvents.
	LastSeen *time.Time
	// Image as stored, so a recreate can name what was replaced.
	Image *string
}

// observation is one sample's contribution to the walk.
type observation struct {
	TS        time.Time
	Count     *int64
	StartedAt *time.Time
}

// restartEvent is one row destined for `events`.
type restartEvent struct {
	Type     string
	TS       time.Time
	Severity string
	Detail   map[string]any
}

// restartEvents walks one container's samples oldest-first and returns the
// events they imply.
//
// THE WALK IS OVER EVERY SAMPLE, not just the newest. The upsert beside this
// only stores the newest row per key, which is right for name and image -- but
// a ring buffer replayed after a two-hour outage carries every scrape in that
// outage, and comparing only its last row against the stored one would collapse
// three separate restarts into a single event dated at the end.
//
// Samples at or before `prev.LastSeen` are SKIPPED, and that guard is the one
// thing standing between a delayed batch and a fabricated redeploy: an
// out-of-order delivery compared against a newer stored count reads as a
// decrease, which is the shape of a recreate. It has its own test; keep it.
func restartEvents(prev containerState, obs []observation, image *string) []restartEvent {
	var out []restartEvent

	count, started := prev.Count, prev.StartedAt
	for _, o := range obs {
		if prev.LastSeen != nil && !o.TS.After(*prev.LastSeen) {
			continue
		}

		switch {
		case count == nil || o.Count == nil:
			// A delta from an unknown is not a reading. This is also the case
			// an agent that has lost inspect lands in, and it must produce
			// nothing rather than a restart from 0.

		case *o.Count > *count:
			out = append(out, restartEvent{
				Type:     EventContainerRestart,
				TS:       eventTS(o, started),
				Severity: "warning",
				Detail: withTSSource(map[string]any{
					"from":  *count,
					"to":    *o.Count,
					"delta": *o.Count - *count,
				}, o, started),
			})

		case *o.Count < *count:
			out = append(out, restartEvent{
				Type:     EventContainerRecreate,
				TS:       eventTS(o, started),
				Severity: "info",
				Detail: withTSSource(recreateDetail(*count, *o.Count, prev.Image, image),
					o, started),
			})

		case startedMoved(started, o.StartedAt):
			// The counter did not move but the container did: a new
			// incarnation whose RestartCount happens to match the old one's.
			// Docker resets the counter on recreate, so this is the redeploy
			// that a counter of 0 replacing a counter of 0 would otherwise
			// hide completely.
			out = append(out, restartEvent{
				Type:     EventContainerRecreate,
				TS:       eventTS(o, started),
				Severity: "info",
				Detail: withTSSource(recreateDetail(*count, *o.Count, prev.Image, image),
					o, started),
			})
		}

		// The walk's running state advances on EVERY sample it did not skip,
		// including the ones that emitted nothing -- otherwise a nil count in
		// the middle of a batch would be compared against forever.
		count, started = o.Count, o.StartedAt
	}
	return out
}

// recreateDetail names what was replaced, when the two images are known and
// differ. A redeploy that did not change the image is still a redeploy, and
// saying "nginx:1.27 -> nginx:1.27" would be noise dressed as a fact.
func recreateDetail(from, to int64, was, now *string) map[string]any {
	d := map[string]any{"from": from, "to": to, "delta": 0}
	if was != nil && now != nil && *was != *now {
		d["image_from"] = *was
		d["image_to"] = *now
	}
	return d
}

// startedMoved reports a new incarnation from the start time alone: both known,
// and the new one later. Later, not merely different -- an out-of-order sample
// carrying an older instant is not a redeploy going backwards in time.
func startedMoved(prev, next *time.Time) bool {
	return prev != nil && next != nil && next.After(*prev)
}

// eventTS dates the event, preferring Docker's own instant.
//
// This is what collecting started_at buys, and the two features were never
// independent: the agent's inspect cache can be ten scrapes behind, so an event
// dated from the sample that NOTICED a restart is an upper bound up to ten
// minutes wide -- while State.StartedAt names the second the new incarnation
// actually came up, however late the reading arrived.
//
// Clamped to the sample's own ts. Agent and hub clocks differ, and an event
// dated in the future of the sample that reported it would fall outside every
// window query that should have found it.
func eventTS(o observation, prevStarted *time.Time) time.Time {
	if o.StartedAt != nil && startedMoved(prevStarted, o.StartedAt) && !o.StartedAt.After(o.TS) {
		return *o.StartedAt
	}
	// A first sighting with a start time and no previous one to compare
	// against: still exact, and still better than the sample ts.
	if o.StartedAt != nil && prevStarted == nil && !o.StartedAt.After(o.TS) {
		return *o.StartedAt
	}
	return o.TS
}

// withTSSource records which clock dated the event, and the instant it was
// observed on when they differ. A reader must be able to tell an exact moment
// from an upper bound without consulting this file.
func withTSSource(d map[string]any, o observation, prevStarted *time.Time) map[string]any {
	if eventTS(o, prevStarted).Equal(o.TS) {
		d["ts_source"] = tsSourceObserved
	} else {
		d["ts_source"] = tsSourceStartedAt
		d["observed_ts"] = o.TS.UTC().Format(time.RFC3339Nano)
	}
	return d
}

// observationsOf collects one container's samples from a batch, oldest first.
//
// Sorted rather than assumed: nothing in the ingest contract promises a batch
// arrives in order, and the walk's whole meaning depends on it.
func observationsOf(rows []*netrav1.ContainerSample, key string) []observation {
	var out []observation
	for _, r := range rows {
		if r.GetContainerKey() != key {
			continue
		}
		out = append(out, observation{
			TS:        tsOf(r.GetTsMs()),
			Count:     int64OrNil(r.RestartCount),
			StartedAt: tsPtrOf(r.StartedAtMs),
		})
	}
	// A stable sort, so two samples sharing a ts keep the order the agent sent
	// them in rather than an arbitrary one.
	stableSortByTS(out)
	return out
}

func stableSortByTS(o []observation) {
	for i := 1; i < len(o); i++ {
		for j := i; j > 0 && o[j].TS.Before(o[j-1].TS); j-- {
			o[j], o[j-1] = o[j-1], o[j]
		}
	}
}

// carriesRestartData reports whether any sample for this key says anything
// inspect-derived.
//
// The fast path out of the transaction below. A container on a host with no
// Docker socket -- or one whose daemon refuses inspect -- can never produce a
// restart event, because every rule in restartEvents needs a non-nil count on
// both sides of a step. Those containers keep the single-statement upsert they
// have always had, and only containers that could actually have restarted pay
// for the transaction.
func carriesRestartData(rows []*netrav1.ContainerSample, key string) bool {
	for _, r := range rows {
		if r.GetContainerKey() == key && (r.RestartCount != nil || r.StartedAtMs != nil) {
			return true
		}
	}
	return false
}

// detailBody renders an event's detail for the JSONB column.
func detailBody(d map[string]any) (string, error) {
	body, err := json.Marshal(d)
	if err != nil {
		return "", fmt.Errorf("render restart event detail: %w", err)
	}
	return string(body), nil
}
