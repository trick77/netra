// Package client scrapes collectors and posts batches to the hub.
package client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"math"
	"math/rand/v2"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/trick77/netra/internal/agent/buffer"
	"github.com/trick77/netra/internal/agent/collector"
	"github.com/trick77/netra/internal/agent/config"
	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

// ErrUnauthorized means the hub rejected this agent's token.
var ErrUnauthorized = errors.New("hub rejected the agent token")

// ingestPath is the hub endpoint agents post to.
const ingestPath = "/api/agent/v1/ingest"

// maxAdoptedRetryAfter caps a hub-supplied retry_after_s so a bad value
// cannot stall the agent indefinitely.
const maxAdoptedRetryAfter = 10 * time.Minute

// maxBatchRows caps how many rows one POST carries: host samples plus every
// per-entity row riding with them.
//
// The hub caps an ingest body at 4 MiB (httpapi.maxBodyBytes). Sending an
// over-sized ring whole would earn a 413 the agent can never recover from:
// the ring only drops its oldest entry to make room for a new one, so it
// stays at capacity and every later flush re-sends the same oversized body.
// Draining a prefix per flush keeps every request comfortably inside the
// hub's limit; AckThrough is prefix-based and seq is monotonic, so a partial
// drain is exactly as safe as a whole one.
//
// This counts ROWS, not scrapes. Counting scrapes was a sound proxy for body
// size only while a scrape was one narrow row: a 64-core host now carries 65
// rows per scrape, so 2000 scrapes would be 130,000 rows and blow the very
// limit this constant exists to respect.
//
// 20000 rows at a generous 100 bytes each is ~2 MB, half the cap, leaving
// room for the metadata block and for the families that later 1C PRs add.
const maxBatchRows = 20000

// maxBufferSlots caps the ring's capacity in entries, independently of the
// window/interval arithmetic that sizes it. capacityFor bounds the buffered
// WINDOW, which says nothing on its own about how much memory the entries in
// it hold — and during an outage the ring fills to capacity by design, on a
// host the agent is meant to be a negligible tenant of.
//
// An entry is now a whole scrape rather than one narrow row, so a slot is
// both larger and variable: a 64-core host buffers ~65 rows per slot where a
// single-core VPS buffers 2. The window arithmetic still bounds the slot
// COUNT correctly — 360 at the 6h maximum, at the fixed 60s cadence — so the
// worst case is ~23,000 buffered rows, a few MB. This stays a standing guard
// on the invariant rather than a limit reached in practice; revisit it when a
// family arrives whose row count per scrape is unbounded (containers,
// processes).
const maxBufferSlots = 10000

// RetryAfterError wraps a flush failure that came with a hub-specified
// minimum retry delay (a 503 with retry_after_s set). Run honours it in
// place of its own exponential backoff.
type RetryAfterError struct {
	After time.Duration
	err   error
}

func (e *RetryAfterError) Error() string { return e.err.Error() }
func (e *RetryAfterError) Unwrap() error { return e.err }

// Client owns the scrape loop, the buffer and the HTTP conversation.
type Client struct {
	cfg        config.Config
	collectors []collector.Collector
	http       *http.Client
	ring       *buffer.Ring

	// interval is the scrape cadence. In production it is always
	// config.ScrapeInterval; it is a field only so the package's own tests
	// can drive Run's ticker faster than 60s (see export_test.go). Nothing
	// mutates it after construction.
	interval time.Duration

	seq          uint64
	metadata     *netrav1.Metadata
	metadataHash []byte
	sendMetadata bool
	// replaying stays true for the WHOLE of a replay, not just its first
	// batch. It was cleared on the first successful partial drain, so
	// recovering 7200 buffered samples flagged batch 1 as backfill and the
	// three 2000-sample batches after it as live — every one of which was
	// replayed history the hub needs to invalidate aggregates for.
	replaying  bool
	retryAfter time.Duration

	// lastPostLatency is the round-trip time of the most recent SUCCESSFUL
	// post, or nil when the last attempt failed or none has happened yet.
	//
	// It is reported one scrape behind by construction: a post's RTT is only
	// known once the post returns, which is after the sample that would carry
	// it has already been built and buffered.
	lastPostLatency *time.Duration

	// startedAt is when this process began, so uptime is the AGENT's rather
	// than the host's. The two are different facts: an agent uptime reset with
	// host uptime unchanged means the agent restarted alone, which also means
	// its ring buffer was lost -- exactly the crash-looping that conflating
	// them would hide behind a healthy-looking host.
	startedAt time.Time

	// postFailures is cumulative across the agent's life and is never reset by
	// a success. An agent that failed ten times and then recovered must still
	// report ten, or the history of the outage vanishes the moment it ends.
	postFailures uint64

	// inventoryLost records that a buffered scrape was discarded before the
	// hub could see it, so the whole-set collectors must report again once
	// there is somewhere for the set to go. Latched rather than acted on at
	// the moment of loss -- see resendInventory.
	inventoryLost bool

	// tokenRejected records that the MOST RECENT POST was rejected with a 401.
	// Read only by flushOnShutdown, to skip a request the hub has already said
	// it will refuse. Any other outcome clears it: a 503 or a transport error
	// says nothing about the token.
	tokenRejected bool
}

// New builds a Client scraping at the fixed config.ScrapeInterval. Buffer
// capacity is derived from the configured window and that interval, so
// NETRA_BUFFER_WINDOW is expressed in time rather than in a sample count
// nobody can reason about.
func New(cfg config.Config, collectors []collector.Collector) *Client {
	return newClient(cfg, collectors, config.ScrapeInterval)
}

// newClient is New with the cadence injected, so export_test.go can hand the
// package's own tests a faster ticker without exposing an interval knob to
// production callers.
func newClient(cfg config.Config, collectors []collector.Collector, interval time.Duration) *Client {
	capacity := capacityFor(cfg.BufferWindow, interval)

	md := BuildMetadata(cfg)

	return &Client{
		cfg:          cfg,
		collectors:   collectors,
		http:         &http.Client{Timeout: 30 * time.Second},
		ring:         buffer.New(capacity),
		interval:     interval,
		metadata:     md,
		metadataHash: HashMetadata(md),
		// The hub asks for metadata when it needs it; nothing is assumed.
		sendMetadata: false,
		startedAt:    time.Now(),
	}
}

// capacityFor computes ring capacity in slots so that capacity * interval
// stays within window, and never exceeds config.MaxBufferWindow regardless
// of what window is passed in — a defence in depth against the 6h
// continuous-aggregate start_offset, on top of the config-load-time guard
// that already bounds cfg.BufferWindow itself. The slot count is capped
// separately at maxBufferSlots so a very short interval cannot turn a bounded
// window into an unbounded amount of memory.
func capacityFor(window, interval time.Duration) int {
	if window > config.MaxBufferWindow {
		window = config.MaxBufferWindow
	}
	capacity := int(window / interval)
	if capacity < 1 {
		capacity = 1
	}
	if capacity > maxBufferSlots {
		// Loudly, not silently: the operator asked for a window and is about
		// to get less of one. config.Load errors on the same coupling, so a
		// quiet downgrade here would be the odd one out.
		slog.Warn("buffer capacity clamped; the effective buffered window is shorter than NETRA_BUFFER_WINDOW",
			// The window the operator actually asked for, alongside the slot
			// count it worked out to. Logging only the derived slot count under
			// the name "requested" asked them to reverse the arithmetic to find
			// out which setting to change.
			"requested_window", window, "would_need_slots", capacity,
			"max_slots", maxBufferSlots, "interval", interval,
			"effective_window", time.Duration(maxBufferSlots)*interval)
		capacity = maxBufferSlots
	}
	return capacity
}

// BufferDepth reports how many samples are waiting to be acknowledged.
func (c *Client) BufferDepth() int { return c.ring.Depth() }

// BufferCapacity reports the ring's capacity in slots. capacity *
// config.ScrapeInterval is the effective buffered window, which capacityFor
// keeps within cfg.BufferWindow.
func (c *Client) BufferCapacity() int { return c.ring.Capacity() }

// ScrapeOnce runs every collector and buffers the resulting scrape.
//
// A collector that fails is logged and skipped, and contributes nothing at
// all: the rest of the scrape is still worth sending. It returns the host row
// for the caller's convenience; the per-entity rows go to the ring with it.
func (c *Client) ScrapeOnce(ctx context.Context) *netrav1.HostSample {
	scrape := c.collect(ctx)
	c.seq++

	// Add overwrites the oldest entry when the ring is full, and that entry
	// may have been the one carrying an inventory set. The inventory
	// collectors have already advanced their own state, so unless they are
	// told, the set is simply lost -- see collector.InventoryResender.
	//
	// Only NOTED here, not acted on. During an outage the ring sits at
	// capacity, so every scrape drops one: re-arming on each would re-parse a
	// 20 MB dpkg status file every 60s and push ~4000 inventory rows into a
	// ring that is already overflowing, making the overflow worse. That is
	// exactly the waste Packages' daily floor exists to prevent, arriving
	// precisely when the host is already degraded.
	dropped := c.ring.Dropped()
	c.ring.Add(c.seq, scrape)
	if c.ring.Dropped() != dropped {
		c.inventoryLost = true
	}

	return scrape.Host
}

// resendInventory asks the whole-set collectors to report again, because a
// buffered scrape was discarded before the hub could see it.
//
// Deferred to the moment the ring drains rather than run at the moment of
// loss, so a long outage re-arms ONCE on recovery instead of once a minute
// throughout. Waiting also makes the re-armed set useful: emitted mid-outage it
// would only join the queue of scrapes being dropped.
//
// This covers the 401 dump too. That path empties the ring and sets
// inventoryLost, and the first flush that succeeds once the token is fixed
// finds the ring empty and re-arms then.
//
// Cheap and idempotent -- it only clears a "last reported" marker, so a
// re-arm after a drop that carried no inventory costs one redundant set.
func (c *Client) resendInventory() {
	if !c.inventoryLost {
		return
	}
	c.inventoryLost = false

	for _, col := range c.collectors {
		if r, ok := col.(collector.InventoryResender); ok {
			r.ResendInventory()
		}
	}
}

// Prime runs the delta-based collectors once without buffering or sending the
// result. The delta-based collectors (CPU, kernelstat, netstat) need a
// baseline scrape before they can report a rate; calling Collect once here
// gives them that baseline without leaving
// behind a stored row whose values are NULL for a reason ("not computable
// yet") that is indistinguishable from an absent subsystem.
//
// A collector reporting itself as a collector.BaselineEmitter is SKIPPED, not
// primed. Its first Collect is data -- mdraid and systemd each report what
// they found on arrival -- and priming it would consume that baseline into the
// result this function throws away, leaving the first real scrape with nothing
// to report because the state it would compare against is now identical. An
// array or a unit that was already failed when the agent started would raise
// no event at all, which is the one case the baseline exists for.
//
// Collectors are run here directly rather than through collect() for that
// reason: collect() has no way to leave one out.
func (c *Client) Prime(ctx context.Context) {
	for _, col := range c.collectors {
		if b, ok := col.(collector.BaselineEmitter); ok && b.EmitsBaseline() {
			continue
		}
		if _, err := col.Collect(ctx); err != nil {
			// Priming is best-effort: the scheduled scrape reports the same
			// failure, with somewhere to record it.
			slog.Warn("priming collector failed", "collector", col.Name(), "err", err)
		}
	}
}

// collect runs every collector and returns the resulting scrape, without
// touching the sequence counter or the ring buffer.
func (c *Client) collect(ctx context.Context) *buffer.Scrape {
	sample := &netrav1.HostSample{TsMs: time.Now().UnixMilli()}
	scrape := &buffer.Scrape{}

	start := time.Now()
	for _, col := range c.collectors {
		colStart := time.Now()
		res, err := col.Collect(ctx)
		colElapsed := time.Since(colStart)

		// Every collector reports its own health, on every scrape, whether it
		// worked or not. Recording it only on success would make a collector
		// that is failing indistinguishable from one that was never
		// registered -- which is exactly the question the panel exists to
		// answer.
		// A scrape cancelled by shutdown makes every remaining ctx-aware
		// collector return context.Canceled at once. Recording those would
		// paint a fleet-wide wall of failures across the availability panel at
		// every redeploy -- flushOnShutdown posts this scrape with a fresh
		// context, so the rows do land. The collectors did not fail; they were
		// never given the chance to run.
		if ctx.Err() == nil {
			scrape.Collectors = append(scrape.Collectors, &netrav1.CollectorSample{
				TsMs:      sample.TsMs,
				Collector: col.Name(),
				// Rounded, not truncated: most procfs collectors finish in tens
				// of microseconds, and truncating reports 0 ms for all of them
				// while the simulator writes realistic figures -- the same panel
				// reading differently for real and simulated hosts is the exact
				// divergence this set out to remove.
				DurationMs: ptr(uint32((colElapsed + 500*time.Microsecond) / time.Millisecond)),
				Ok:         err == nil,
				ErrorCode:  errorCode(err),
			})
		}

		if err != nil {
			// Nothing from a failed collector reaches the scrape. Merging a
			// partial result would store fields the collector never finished
			// measuring, and an unset field is supposed to mean the subsystem
			// is absent.
			slog.Warn("collector failed", "collector", col.Name(), "err", err)
			continue
		}
		if res == nil {
			// The interface says a failed collector returns a nil Result and
			// an error; nothing enforces the inverse. Dereferencing here
			// would take the whole agent down over one collector's slip, so
			// treat "no error, no result" as "nothing to report".
			continue
		}
		if res.Host != nil {
			// Merge rather than assign: each collector owns a disjoint set of
			// fields, and proto.Merge copies only the ones actually set,
			// which is what keeps unset meaning "absent".
			proto.Merge(sample, res.Host)
		}
		appendFamilies(scrape, res)
	}
	elapsed := time.Since(start)

	c.refreshCapabilities()

	depth := uint32(c.ring.Depth())
	dropped := c.ring.Dropped()
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	agent := &netrav1.AgentSample{
		ScrapeDurationMs:   ptr(uint32(elapsed.Milliseconds())),
		BufferDepth:        &depth,
		BufferDroppedTotal: &dropped,
		// The AGENT's uptime, not the host's -- see the startedAt field.
		UptimeS: ptr(uint64(time.Since(c.startedAt).Seconds())),
		// Sys rather than Alloc: what the process took from the OS is what an
		// operator sees in ps and what makes the agent a bad tenant, whereas
		// Alloc is Go's live heap and understates the footprint.
		RssBytes:          ptr(mem.Sys),
		Goroutines:        ptr(uint32(runtime.NumGoroutine())),
		PostFailuresTotal: ptr(c.postFailures),
	}
	// Only carried when the last post actually succeeded. Reusing a stale
	// value would report a healthy RTT throughout an outage, and zeroing it
	// would report an impossibly fast one; both are worse than saying nothing.
	if c.lastPostLatency != nil {
		agent.PostLatencyMs = ptr(uint32(c.lastPostLatency.Milliseconds()))
	}
	sample.Agent = agent
	scrape.Host = sample

	return scrape
}

// appendFamilies concatenates one collector's per-entity rows onto the scrape.
//
// Every family is listed here explicitly. A family added to Result and Scrape
// but forgotten here would be collected and then silently dropped before it
// ever reached the ring -- TestScrapeCarriesEveryFamilyFromAResult walks the
// struct reflectively so a missed line fails rather than ships.
// errorCode reduces a collector's error to a short, stable token.
//
// The raw error text is deliberately not sent. It carries paths, device names
// and errno strings that differ per host and per scrape, and the column is
// rolled up with last() into both aggregate tiers -- so a free-text message
// would make "why is this collector failing" unanswerable across a fleet
// while the aggregates filled with one-off strings.
//
// Returns nil on success: the proto says a collector that failed once must
// not read as broken forever, so this clears on recovery rather than latching.
func errorCode(err error) *string {
	if err == nil {
		return nil
	}

	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return ptr("timeout")
	case errors.Is(err, context.Canceled):
		return ptr("canceled")
	case errors.Is(err, os.ErrPermission):
		return ptr("permission-denied")
	case errors.Is(err, os.ErrNotExist):
		return ptr("not-found")
	default:
		return ptr("error")
	}
}

func appendFamilies(s *buffer.Scrape, res *collector.Result) {
	s.Cores = append(s.Cores, res.Cores...)
	s.Disks = append(s.Disks, res.Disks...)
	s.Sensors = append(s.Sensors, res.Sensors...)
	s.Nets = append(s.Nets, res.Nets...)
	s.Containers = append(s.Containers, res.Containers...)
	s.Filesystems = append(s.Filesystems, res.Filesystems...)
	s.Smart = append(s.Smart, res.Smart...)
	s.Processes = append(s.Processes, res.Processes...)
	s.Events = append(s.Events, res.Events...)
	s.SystemdEvents = append(s.SystemdEvents, res.SystemdEvents...)
	s.PackageEvents = append(s.PackageEvents, res.PackageEvents...)
	s.Addresses = append(s.Addresses, res.Addresses...)
	s.Packages = append(s.Packages, res.Packages...)
}

// countRows is how many rows a scrape contributes to a request body: the host
// row plus every per-entity row measured with it.
//
// The flush bound is expressed in rows because a scrape stopped being one row.
// A family missing from this sum silently un-enforces the 4 MiB body limit --
// nothing fails until a large host replays after an outage and 413s forever,
// which is why TestScrapeRowCountCoversEveryFamily walks the struct rather
// than trusting this list to stay complete.
func countRows(s *buffer.Scrape) int {
	return 1 +
		len(s.Cores) + len(s.Disks) + len(s.Sensors) + len(s.Nets) +
		len(s.Containers) + len(s.Filesystems) + len(s.Smart) +
		len(s.Processes) + len(s.Events) + len(s.SystemdEvents) +
		len(s.PackageEvents) + len(s.Addresses) + len(s.Packages) +
		len(s.Collectors)
}

// refreshCapabilities re-reads what each collector reports about its own
// availability and, if anything changed, flips the metadata hash so the hub
// asks for a resend.
//
// Capabilities travel with metadata rather than with the sample because they
// change on the order of deployments, not of scrapes: repeating them 1440
// times a day would be the same constant string every time.
func (c *Client) refreshCapabilities() {
	merged := make(map[string]string)
	for _, col := range c.collectors {
		reporter, ok := col.(collector.CapabilityReporter)
		if !ok {
			continue
		}
		for k, v := range reporter.Capabilities() {
			merged[k] = v
		}
	}
	// No early return on an empty merged set. Skipping the comparison when
	// nothing is reported means the LAST capability can never be cleared: the
	// metadata keeps its stale entry, the hash never moves, and the hub is
	// never told the subsystem recovered. maps.Equal already handles the
	// genuine no-op -- two empty maps are equal -- so the guard only ever cost
	// correctness.
	//
	// It is unreachable in today's wiring, because Procs and Users store a
	// capability unconditionally on every Collect and both are always
	// registered. That is precisely why it had to go: it was a trap armed to
	// fire the moment either of them is removed or made conditional.
	if maps.Equal(merged, c.metadata.GetCapabilities()) {
		return
	}

	c.metadata.Capabilities = merged
	c.metadataHash = HashMetadata(c.metadata)
	c.sendMetadata = true
}

// ptr returns a pointer to v, for the optional protobuf scalars that must
// distinguish "not measured" from zero.
func ptr[T any](v T) *T { return &v }

// Flush posts every buffered sample, oldest first, and drops the ones the hub
// acknowledges.
func (c *Client) Flush(ctx context.Context) error {
	pending := c.ring.Pending()
	if len(pending) == 0 {
		return nil
	}

	// Send at most maxBatchRows per POST, oldest first. Pending is ordered and
	// AckThrough drops a prefix, so the remainder simply goes out on the next
	// flush rather than being lost.
	//
	// Counted in rows rather than scrapes: a scrape now carries its host row
	// plus every per-entity row measured with it, so the scrape count says
	// nothing about the encoded body size.
	rows := 0
	for i, e := range pending {
		next := countRows(e.Scrape)
		// The i > 0 guard lets a single oversized scrape through on its own.
		// Without it a host whose scrape exceeds the cap would never flush,
		// and the ring would fill and start dropping.
		if i > 0 && rows+next > maxBatchRows {
			pending = pending[:i]
			break
		}
		rows += next
	}

	samples := make([]*netrav1.HostSample, 0, len(pending))
	req := &netrav1.IngestRequest{}
	for _, e := range pending {
		samples = append(samples, e.Scrape.Host)
		s := e.Scrape
		req.CpuCores = append(req.CpuCores, s.Cores...)
		req.DiskIo = append(req.DiskIo, s.Disks...)
		req.Sensors = append(req.Sensors, s.Sensors...)
		req.Net = append(req.Net, s.Nets...)
		req.Containers = append(req.Containers, s.Containers...)
		req.Filesystems = append(req.Filesystems, s.Filesystems...)
		req.Smart = append(req.Smart, s.Smart...)
		req.Processes = append(req.Processes, s.Processes...)
		req.Events = append(req.Events, s.Events...)
		req.SystemdEvents = append(req.SystemdEvents, s.SystemdEvents...)
		req.PackageEvents = append(req.PackageEvents, s.PackageEvents...)
		req.Collectors = append(req.Collectors, s.Collectors...)

		// Inventory is a WHOLE SET, not a time series: the hub replaces what
		// it holds with what arrives, deleting anything the set omits. So the
		// newest non-empty set in this batch supersedes the older ones rather
		// than being concatenated with them.
		//
		// Concatenating breaks the replacement. A scrape reporting {A, B}
		// followed by one reporting {A} after B was removed would arrive as
		// A, B, A -- the union -- and the hub, seeing B in the batch it is
		// told is the current set, would keep it forever.
		//
		// "Non-empty" is a residual gap rather than a rule: a collector
		// reports an empty slice both for "unchanged" and for "the host now
		// has none of these", and the two are indistinguishable here. The hub
		// cannot act on an empty set either (UpsertHostAddresses returns
		// early), so a host that loses its LAST address keeps a stale row --
		// closing that needs an explicit "empty" signal on the wire.
		//
		// countRows still counts every scrape's inventory rows. That
		// over-counts the body this loop actually builds, which is the safe
		// direction for a bound that exists to stay under a size cap.
		if len(s.Addresses) > 0 {
			req.Addresses = s.Addresses
		}
		if len(s.Packages) > 0 {
			req.Packages = s.Packages
		}
	}
	highest := pending[len(pending)-1].Seq

	req.Seq = highest
	req.MetadataHash = c.metadataHash
	req.HostSamples = samples
	// Anything sent after a failed flush is replayed history, and the hub
	// needs to know so it can invalidate the affected aggregate ranges.
	req.Backfill = c.replaying
	if c.sendMetadata {
		req.Metadata = c.metadata
	}

	resp, err := c.post(ctx, req)
	if err != nil {
		// Counted here rather than inside post so every failure kind lands in
		// one place: a network error, a 401 and a 503 are all "the agent could
		// not deliver", which is the question this number answers.
		c.postFailures++

		// The flag means "the attempt that just happened was refused for the
		// token", so anything else clears it. A transport error or a 503 says
		// nothing about the token, and an operator who fixed a revoked token
		// into a hub that is briefly down would otherwise still lose the
		// buffer on shutdown. Defaulting to attempting costs at most
		// shutdownFlushTimeout; defaulting to skipping costs the buffer the
		// whole mechanism exists to save.
		c.tokenRejected = errors.Is(err, ErrUnauthorized)

		if errors.Is(err, ErrUnauthorized) {
			// The token is gone. Replaying forever would hammer the hub for
			// nothing, so the buffer is dropped and the operator has to act.
			//
			// The WHOLE buffer, not just the batch that was attempted.
			// AckThrough(highest) dropped only the prefix that fit in one
			// maxBatchRows batch, out of the
			// maxBufferSlots the ring can hold, and ScrapeOnce keeps adding on
			// every tick — so a revoked token left the ring pinned at capacity
			// indefinitely, which is exactly the state this branch says it is
			// avoiding. math.MaxUint64 is above every sequence number the agent
			// can issue.
			c.ring.AckThrough(math.MaxUint64)
			// Everything just discarded may have included an inventory set the
			// hub never saw. Noted rather than acted on now: the token is still
			// rejected, so a set emitted here would go straight into the ring
			// and be dumped again on the next attempt. The first flush that
			// succeeds once the token is fixed re-arms.
			c.inventoryLost = true
			c.replaying = false
			c.retryAfter = 0
			return err
		}

		// A retry_after only applies to the failure that carried it; any
		// other failure clears it so a stale value from an earlier 503
		// cannot outlive its relevance.
		var raErr *RetryAfterError
		if errors.As(err, &raErr) {
			c.retryAfter = raErr.After
		} else {
			c.retryAfter = 0
		}

		c.replaying = true
		return err
	}

	// The hub answered, so whatever the answer says about this batch, the
	// token itself was accepted.
	c.tokenRejected = false

	// Sequence numbers start at 1 (c.seq is incremented before the first
	// Add), so a genuine ack is never 0. A zero ack_seq means the hub sent a
	// zero-value or malformed response: treating it as success would leave
	// the buffer un-drained while the agent believes it is healthy, with no
	// backoff and no backfill flag on the next attempt.
	if resp.GetAckSeq() == 0 {
		slog.Warn("hub returned a zero ack_seq; treating flush as failed",
			"buffer_depth", c.ring.Depth())
		// Counted for the same reason the transport-error path counts: this is
		// "the agent could not deliver", which is the question the number
		// answers. Leaving it out meant a hub bug or a proxy returning an empty
		// 200 produced an agent that buffered and backed off while reporting
		// post_failures_total = 0 throughout -- the one metric that would have
		// shown the outage insisting nothing was wrong.
		c.postFailures++
		c.replaying = true
		// This failure carried no retry_after of its own, so any value left
		// over from an earlier 503 must be cleared here too — the same rule
		// the transport-error path above applies. Leaving it set would make
		// Run wait out a stale delay (up to maxAdoptedRetryAfter) for a
		// failure the hub never asked to be retried slowly.
		c.retryAfter = 0
		return fmt.Errorf("hub returned ack_seq=0 for a batch of %d samples", len(samples))
	}

	c.ring.AckThrough(resp.GetAckSeq())
	c.sendMetadata = resp.GetRequestMetadata()
	// Cleared only once the ring is actually EMPTY. A partial drain means
	// there is still buffered history behind this batch, and every batch of it
	// is backfill.
	if c.ring.Depth() == 0 {
		c.replaying = false
		// The backlog is delivered, so this is the moment to make good on
		// anything the ring dropped getting here.
		c.resendInventory()
	}
	c.retryAfter = 0

	return nil
}

func (c *Client) post(ctx context.Context, req *netrav1.IngestRequest) (*netrav1.IngestResponse, error) {
	body, err := proto.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := strings.TrimRight(c.cfg.HubURL, "/") + ingestPath
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/x-protobuf")
	httpReq.Header.Set("Authorization", "Bearer "+c.cfg.Token)

	// Measured around Do alone, so the number means "how long did the hub and
	// the network take" rather than including this agent's own marshalling.
	// Cleared up front and set only on the success path below, so a 401, a
	// 503 or an unreadable body all leave it nil. A stale value would report
	// a healthy RTT right through an outage, and a zero would report an
	// impossible one; NULL correctly reads as "could not tell".
	c.lastPostLatency = nil

	sentAt := time.Now()
	httpResp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("post to hub: %w", err)
	}
	rtt := time.Since(sentAt)
	defer func() { _ = httpResp.Body.Close() }()

	if httpResp.StatusCode == http.StatusUnauthorized {
		return nil, ErrUnauthorized
	}
	if httpResp.StatusCode == http.StatusServiceUnavailable {
		return nil, &RetryAfterError{
			After: parseRetryAfter(httpResp),
			err:   fmt.Errorf("hub returned %s", httpResp.Status),
		}
	}
	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("hub returned %s", httpResp.Status)
	}

	raw, err := io.ReadAll(io.LimitReader(httpResp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var resp netrav1.IngestResponse
	if err := proto.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	c.lastPostLatency = &rtt

	return &resp, nil
}

// parseRetryAfter best-effort extracts retry_after_s from a 503 body. It
// only trusts a body declared as protobuf, so an intermediary's HTML error
// page (which would happily "unmarshal" as a zero-value message) cannot be
// mistaken for a real instruction, and it clamps the result so a bad or
// hostile value cannot stall the agent for hours.
func parseRetryAfter(resp *http.Response) time.Duration {
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/x-protobuf") {
		return 0
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0
	}

	var body netrav1.IngestResponse
	if err := proto.Unmarshal(raw, &body); err != nil {
		return 0
	}

	d := time.Duration(body.GetRetryAfterS()) * time.Second
	if d > maxAdoptedRetryAfter {
		d = maxAdoptedRetryAfter
	}
	return d
}

// unauthorizedRetry is how long Run waits between flush attempts after the
// hub has rejected the token. A revoked agent must not hammer the hub.
const unauthorizedRetry = 5 * time.Minute

// maxDrainBatches is how many POSTs one tick may make while a backlog lasts.
//
// The ring holds at most MaxBufferWindow/ScrapeInterval slots -- 360 at the 6h
// maximum -- and maxBatchRows is 20000, so even a 64-core host's worst case of
// ~23,000 buffered rows clears in two. Four leaves headroom for a host whose
// per-scrape row count is larger than anything measured, while still bounding
// how long this goroutine can stay away from ScrapeOnce and ctx.Done: Client's
// fields are not mutex-guarded, so everything has to stay on this one
// goroutine, and an unbounded drain loop would starve the scrape it exists to
// preserve.
const maxDrainBatches = 4

// drain flushes until the ring is empty, a flush fails, or maxDrainBatches
// POSTs have gone out.
//
// Flush carries at most maxBatchRows per POST, so a backlog needs more than
// one. Waiting a whole scrape interval between batches stretched recovery out
// and made the hub invalidate its aggregate ranges once per batch instead of
// once per recovery -- for no reason, since the hub has just demonstrated it is
// up by accepting the previous one.
//
// Bounded rather than looping to empty. The ring keeps growing at one scrape
// per tick, and an unbounded loop would hold this goroutine -- the only one
// Client's unguarded fields are safe on -- away from ScrapeOnce and from
// ctx.Done for as long as the backlog took to accumulate.
func (c *Client) drain(ctx context.Context) error {
	err := c.Flush(ctx)
	for range maxDrainBatches - 1 {
		if err != nil || c.ring.Depth() == 0 || ctx.Err() != nil {
			break
		}
		err = c.Flush(ctx)
	}
	return err
}

// shutdownFlushTimeout bounds the last flush on the way out.
//
// Short on purpose. A container runtime sends SIGTERM and then SIGKILL, and
// Docker's default grace period is 10 seconds -- an agent still trying to
// reach an unreachable hub when that expires is killed mid-request and has
// achieved nothing but delaying the shutdown. Five seconds leaves room for the
// runtime's own teardown.
const shutdownFlushTimeout = 5 * time.Second

// flushOnShutdown makes one last attempt to deliver what is buffered.
//
// Without it a SIGTERM discarded the whole ring, including the scrape taken
// seconds earlier: a rolling image update across a fleet silently dropped the
// last minute of history from every host, which is the gap the ring exists to
// prevent.
//
// It runs on its own context because the one Run was given is already
// cancelled -- that is why we are here -- and a cancelled context cannot carry
// a request. Best-effort by construction: the process is going away either
// way, so a failure is logged and the samples are lost exactly as they were
// before, rather than holding up the shutdown.
func (c *Client) flushOnShutdown() {
	depth := c.ring.Depth()
	if depth == 0 {
		return
	}
	if c.tokenRejected {
		// The hub has already refused this token, and nothing about a SIGTERM
		// changes that. Posting anyway is a guaranteed 401 that costs up to
		// shutdownFlushTimeout of shutdown latency on every restart of a
		// revoked agent -- and on a fleet redeploy, one such request per host.
		slog.Warn("skipping the final flush; the hub has rejected this agent's token",
			"buffer_depth", depth)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), shutdownFlushTimeout)
	defer cancel()

	// The same bounded drain the tick path uses. One Flush carries at most
	// maxBatchRows, so a host shut down mid-backlog would otherwise deliver
	// only the first batch and lose the rest -- which is the loss this function
	// exists to prevent, just smaller.
	if err := c.drain(ctx); err != nil {
		// "sent" as well as "lost": drain makes up to maxDrainBatches POSTs,
		// so a failure on the second one still delivered the first. Reporting
		// only the loss would say the whole buffer was dropped when most of it
		// arrived.
		slog.Warn("final flush on shutdown failed; the rest of the buffer is lost",
			"err", err, "sent", depth-c.ring.Depth(), "lost", c.ring.Depth())
		return
	}
	slog.Info("flushed buffered samples on shutdown",
		"sent", depth-c.ring.Depth(), "remaining", c.ring.Depth())
}

// Run scrapes and flushes on the fixed scrape interval until ctx is
// cancelled. The cadence never changes for the lifetime of the process.
func (c *Client) Run(ctx context.Context) error {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	backoff := time.Second

	// flushNotBefore holds off the next FLUSH attempt after a failure. It
	// deliberately does not hold off the SCRAPE.
	//
	// Sleeping the whole loop through the backoff — as this used to — stops
	// the agent collecting samples for the duration of the wait, because
	// time.Ticker drops ticks nobody is receiving. The backoff reaches
	// 60-120s against a 60s interval, and a hub-supplied retry_after may be
	// up to maxAdoptedRetryAfter (10 minutes), so an outage produced a hole
	// in the history rather than the buffered, replayable history the ring
	// exists to provide. Deferring only the flush keeps everything on this
	// one goroutine (Client's fields are not mutex-guarded) while the ticker
	// keeps driving ScrapeOnce at the configured cadence throughout.
	var flushNotBefore time.Time

	for {
		select {
		case <-ctx.Done():
			c.flushOnShutdown()
			return ctx.Err()
		case <-ticker.C:
			c.ScrapeOnce(ctx)

			if time.Now().Before(flushNotBefore) {
				continue
			}

			if err := c.drain(ctx); err != nil {
				if errors.Is(err, ErrUnauthorized) {
					// Retry slowly: a revoked agent must not hammer the hub.
					slog.Error("hub rejected the agent token; retrying slowly", "err", err)
					flushNotBefore = time.Now().Add(unauthorizedRetry)
					continue
				}

				slog.Warn("flush failed; samples are buffered",
					"err", err, "buffer_depth", c.ring.Depth(),
					"dropped_total", c.ring.Dropped())

				// A hub-specified retry_after replaces the agent's own
				// backoff rather than merely bounding it below: the hub
				// knows more about how long its own outage will last.
				//
				// Jitter is added either way. It keeps a fleet from
				// reconnecting in lockstep after a hub restart, and that
				// matters MORE with a hub-supplied value, not less: the hub
				// hands every agent the same constant (30s from its storage
				// failure path), so honouring it verbatim would synchronise
				// the entire fleet onto one instant against a database that
				// is already struggling. The hub's number is treated as a
				// floor and the spread above it is kept small (up to 10%), so
				// the delay still closely tracks what the hub asked for.
				wait := backoff + time.Duration(rand.Int64N(int64(backoff)))
				if c.retryAfter > 0 {
					wait = c.retryAfter + time.Duration(rand.Int64N(int64(c.retryAfter/10)+1))
				}

				flushNotBefore = time.Now().Add(wait)
				if backoff < time.Minute {
					backoff *= 2
				}
				continue
			}

			backoff = time.Second
			flushNotBefore = time.Time{}
		}
	}
}
