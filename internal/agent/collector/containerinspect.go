package collector

import (
	"context"
	"slices"
)

// ContainerInspector returns Docker's RestartCount for one container id.
//
// Injected for the same two reasons ContainerLister is: the tests need no
// daemon, and the socket stays an optional enrichment. It is a SECOND seam
// rather than another field on ContainerMeta because it is the only part of
// this collector's Docker enrichment that costs a request, and the policy that
// rations those requests -- refreshRestarts, below -- is the thing worth
// testing.
type ContainerInspector func(ctx context.Context, id string) (uint64, error)

// capRestartsNoInspect: the daemon lists containers but will not inspect them,
// or no inspector was wired up at all. Reported under its own key because
// everything else on the card still works -- state, health and labels all ride
// on the list response -- and only the restart count is missing.
const capRestartsNoInspect = "no-inspect"

// restartRefreshEvery is how many scrapes may pass before a container that has
// shown no sign of restarting is inspected anyway.
//
// The counter-reset detector below is not complete: a container that restarts
// and then burns MORE CPU than its previous life did, inside one interval,
// leaves usage_usec higher than it was and looks like a container that simply
// got busy. At the 60s default this closes that hole within ten minutes, which
// is the resolution a restart count is read at.
const restartRefreshEvery = 10

// maxInspectsPerScrape bounds the cost of a scrape on a host that just rebooted
// and presents two hundred containers the cache has never seen.
//
// Overflow is not lost, only deferred: an id that is not inspected this scrape
// is still uncached next scrape, so it is at the front of the queue. The bound
// exists because dockerClient's timeout is per request -- twenty requests to a
// wedged daemon is already a hundred seconds, and the scrape interval is sixty.
const maxInspectsPerScrape = 20

// noInspectAfterScrapes is how many CONSECUTIVE scrapes must fail every attempt
// before the agent says the daemon refuses inspect.
//
// Not one, which is what it used to be. In steady state a scrape attempts zero
// to two inspects, so a single 404 -- a container removed between the list and
// the inspect, which is ordinary -- would have been "every attempt failed" and
// flipped the capability on for one scrape and off the next. An operator would
// see the badge flap. Three consecutive scrapes is three minutes at the 60s
// default, which no transient refusal survives and no permanent one escapes.
const noInspectAfterScrapes = 3

// backoffScrapes is how often the agent probes a daemon it has concluded will
// not answer.
//
// Without it a socket proxied to /containers/json alone costs 20 rejected
// requests every scrape, forever: a failed inspect writes no cache entry, so
// every container stays uncached and is retried on every pass. netNSDenied in
// this collector latches per container for exactly this reason. The refusal
// here is host-wide rather than per-container, so it backs OFF rather than
// latching -- an operator who fixes the proxy gets restart counts back within
// ten minutes without restarting the agent.
const backoffScrapes = 10

// restartEntry is one container's last known restart count and the scrape it
// was read on.
type restartEntry struct {
	count  uint64
	scrape uint64
}

// SetInspector wires up the restart-count reader. Not a NewContainers
// parameter: the constructor already takes four, every existing test would
// grow a nil argument that says nothing, and an agent with a socket but no
// inspect permission is a supported configuration that this being separate
// makes easy to express.
func (c *Containers) SetInspector(fn ContainerInspector) { c.inspector = fn }

// refreshRestarts brings the restart cache up to date for the containers this
// scrape saw, and returns nothing -- readRestart is how the row build asks.
//
// The whole design is about NOT calling inspect. RestartCount is the one field
// the list endpoint does not carry, and the obvious implementation -- inspect
// every container every scrape -- is the same cost shape dockermeta.go already
// rejects for /containers/{id}/stats: per-container daemon work, once a minute,
// forever, on hosts running hundreds of containers.
//
// So it inspects only where the answer can have changed:
//
//   - an id with no cached value, which is a container the agent has not seen
//     before;
//   - an id whose cgroup counters went BACKWARDS since the last scrape. That is
//     already computed for another purpose -- Collect refuses to rate a
//     negative delta -- and "the cgroup was recreated under the same id" is
//     precisely a restart. A free detector, reused;
//   - one in every restartRefreshEvery scrapes, staggered across ids so a
//     hundred containers do not all refresh on the same tick, to cover the case
//     the detector misses.
//
// In steady state -- no restarts, no new containers -- this is zero requests on
// nine scrapes out of ten and a handful on the tenth.
//
// What the CACHE holds is then reported on every scrape, not only on the ones
// that inspected, and that is deliberate: a series carrying a point one time in
// ten is not a series, and the whole reason restart_count is per-sample is so a
// hole in the container's charts can be attributed. The cost is that the number
// can be up to restartRefreshEvery scrapes behind, so a restart shows up on the
// chart within ten minutes rather than within one. A counter read at that
// resolution is the trade the rationing buys.
func (c *Containers) refreshRestarts(ctx context.Context, meta map[string]ContainerMeta, recreated map[string]bool) {
	if c.inspector == nil {
		c.setRestartCapability(capRestartsNoInspect)
		return
	}

	c.scrapeN++
	scrape := c.scrapeN

	if c.restarts == nil {
		c.restarts = make(map[string]restartEntry, len(meta))
	}

	// A daemon that has refused everything for noInspectAfterScrapes in a row
	// is probed once every backoffScrapes rather than up to twenty times a
	// minute. Still probed, because the refusal is a proxy configuration and
	// not a property of the container: fix the proxy and the counts come back.
	backedOff := c.inspectFailStreak >= noInspectAfterScrapes
	if backedOff && scrape%backoffScrapes != 0 {
		c.evictUnlisted(meta)
		return
	}

	// Sorted, so a capped scrape works through the same order every time
	// rather than whatever the map handed it -- otherwise the ids past the cap
	// are a different arbitrary subset each scrape and a busy host could starve
	// one container indefinitely.
	ids := make([]string, 0, len(meta))
	for id := range meta {
		ids = append(ids, id)
	}
	slices.Sort(ids)

	spent, failed, attempted := 0, 0, 0
	for _, id := range ids {
		entry, cached := c.restarts[id]
		switch {
		case !cached, recreated[id]:
		case scrape-entry.scrape >= restartRefreshEvery && staggerSlot(id) == scrape%restartRefreshEvery:
		default:
			continue
		}
		if spent >= maxInspectsPerScrape {
			break
		}
		spent++
		attempted++

		count, err := c.inspector(ctx, id)
		if err != nil {
			// The cached entry is DROPPED, not left standing. An agent that has
			// just been refused is no longer in a position to assert a restart
			// count, and holding the last number it happened to read would put
			// "Restarts: 12" on a page whose State and Health both correctly
			// read "not reported" -- the same stale-assertion failure the
			// overwrite rule for those two exists to prevent.
			//
			// The delay is bounded by the refresh policy above: a cached
			// container is not re-attempted until the slow refresh comes round,
			// so a revoked socket takes up to restartRefreshEvery scrapes to
			// show as unknown rather than showing wrong immediately.
			delete(c.restarts, id)
			failed++
			continue
		}
		c.restarts[id] = restartEntry{count: count, scrape: scrape}
	}

	c.evictUnlisted(meta)

	// Every attempt failing, on several scrapes running, is the daemon refusing
	// inspect -- a socket mounted through a proxy that allows the list endpoint
	// and nothing else. A single failure is not: a container removed between
	// the list and the inspect answers 404, and that is an ordinary Tuesday.
	switch {
	case attempted == 0:
		// Nothing was asked, so nothing was learned. The streak holds rather
		// than resetting, or a backed-off host would clear its own capability
		// on the first quiet scrape and start hammering again.
	case failed == attempted:
		c.inspectFailStreak++
	default:
		c.inspectFailStreak = 0
	}

	if c.inspectFailStreak >= noInspectAfterScrapes {
		c.setRestartCapability(capRestartsNoInspect)
		return
	}
	c.setRestartCapability("")
}

// evictUnlisted drops cached counts for containers this scrape did not see.
//
// A container that is gone will not be asked about again, and one that comes
// back arrives with a new id and so a fresh read -- which is right, because
// Docker resets RestartCount when a container is recreated.
func (c *Containers) evictUnlisted(meta map[string]ContainerMeta) {
	for id := range c.restarts {
		if _, ok := meta[id]; !ok {
			delete(c.restarts, id)
		}
	}
}

// readRestart returns the last restart count read for one container.
//
// The CACHED value, reported on every scrape rather than only on the ones that
// inspected -- see refreshRestarts for why a one-in-ten series is not a series.
// It is absent only for a container never successfully inspected, or one whose
// inspect has since been refused.
func (c *Containers) readRestart(id string) (uint64, bool) {
	entry, ok := c.restarts[id]
	return entry.count, ok
}

// setRestartCapability records why restart counts are absent, or clears it.
func (c *Containers) setRestartCapability(value string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.restartCapability = value
}

// staggerSlot spreads the slow refresh over restartRefreshEvery scrapes by
// hashing the id, so a host with two hundred containers refreshes roughly
// twenty per scrape instead of two hundred on every tenth one.
func staggerSlot(id string) uint64 {
	var h uint64 = 14695981039346656037
	for i := 0; i < len(id); i++ {
		h ^= uint64(id[i])
		h *= 1099511628211
	}
	return h % restartRefreshEvery
}
