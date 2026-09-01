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
			// No entry written, so the container reports no restart count at
			// all this scrape rather than a stale or zero one. The hub
			// coalesces an unset restart_count precisely so this does not blank
			// a number it already has.
			failed++
			continue
		}
		c.restarts[id] = restartEntry{count: count, scrape: scrape}
	}

	// Evict what this scrape did not see. A container that is gone will not be
	// asked about again, and one that comes back arrives with a new id and so a
	// fresh read -- which is right, because Docker resets RestartCount when a
	// container is recreated.
	for id := range c.restarts {
		if _, ok := meta[id]; !ok {
			delete(c.restarts, id)
		}
	}

	// Every attempt failing is the daemon refusing inspect -- an agent whose
	// socket is mounted read-only through a proxy that allows the list endpoint
	// and nothing else. One that merely timed out on some ids is not that, and
	// says nothing.
	if attempted > 0 && failed == attempted {
		c.setRestartCapability(capRestartsNoInspect)
		return
	}
	c.setRestartCapability("")
}

// readRestart returns the cached restart count for one container.
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
