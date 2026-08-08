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

// maxBatchSamples caps how many buffered samples one POST carries.
//
// The hub caps an ingest body at 4 MiB (httpapi.maxBodyBytes). Sending an
// over-sized ring whole would earn a 413 the agent can never recover from:
// the ring only drops its oldest entry to make room for a new one, so it
// stays at capacity and every later flush re-sends the same oversized body.
// Draining a prefix per flush keeps every request comfortably inside the
// hub's limit; AckThrough is prefix-based and seq is monotonic, so a partial
// drain is exactly as safe as a whole one.
//
// At the fixed 60s cadence the ring maxes out at 360 slots (6h window), so
// this bound does not bind today. It is kept as the invariant that guards
// the 413-forever failure mode independently of the window arithmetic.
const maxBatchSamples = 2000

// maxBufferSlots caps the ring's capacity in entries, independently of the
// window/interval arithmetic that sizes it. capacityFor bounds the buffered
// WINDOW, which says nothing on its own about how many live *HostSample
// values fit in it — and during an outage the ring fills to capacity by
// design, on a host the agent is meant to be a negligible tenant of. The
// fixed 60s cadence keeps the real number small (360 at the 6h maximum), so
// like maxBatchSamples this is a standing guard on the memory invariant
// rather than a limit reached in practice.
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
		// to get less of one. config.Load errors on the same coupling and
		// Ring.Resize logs its own losses; a quiet downgrade here would be
		// the odd one out.
		slog.Warn("buffer capacity clamped; the effective buffered window is shorter than NETRA_BUFFER_WINDOW",
			"requested_slots", capacity, "max_slots", maxBufferSlots,
			"interval", interval, "effective_window", time.Duration(maxBufferSlots)*interval)
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

// ScrapeOnce runs every collector and buffers the resulting sample.
//
// A collector that fails is logged and skipped: its fields stay unset, and
// the rest of the sample is still worth sending.
func (c *Client) ScrapeOnce(ctx context.Context) *netrav1.HostSample {
	sample := c.collect(ctx)
	c.seq++
	c.ring.Add(c.seq, sample)
	return sample
}

// Prime runs every collector once without buffering or sending the result.
// The delta-based collectors (CPU, kernelstat, netstat) need a baseline
// scrape before they can report a rate; calling Collect once here gives them
// that baseline without leaving
// behind a stored row whose values are NULL for a reason ("not computable
// yet") that is indistinguishable from an absent subsystem.
func (c *Client) Prime(ctx context.Context) {
	c.collect(ctx)
}

// collect runs every collector and returns the resulting sample, without
// touching the sequence counter or the ring buffer.
func (c *Client) collect(ctx context.Context) *netrav1.HostSample {
	sample := &netrav1.HostSample{TsMs: time.Now().UnixMilli()}

	start := time.Now()
	for _, col := range c.collectors {
		if err := col.Collect(ctx, sample); err != nil {
			slog.Warn("collector failed", "collector", col.Name(), "err", err)
		}
	}
	elapsed := time.Since(start)

	c.refreshCapabilities()

	depth := uint32(c.ring.Depth())
	dropped := c.ring.Dropped()
	agent := &netrav1.AgentSample{
		ScrapeDurationMs:   ptr(uint32(elapsed.Milliseconds())),
		BufferDepth:        &depth,
		BufferDroppedTotal: &dropped,
	}
	// Only carried when the last post actually succeeded. Reusing a stale
	// value would report a healthy RTT throughout an outage, and zeroing it
	// would report an impossibly fast one; both are worse than saying nothing.
	if c.lastPostLatency != nil {
		agent.PostLatencyMs = ptr(uint32(c.lastPostLatency.Milliseconds()))
	}
	sample.Agent = agent

	return sample
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
	if len(merged) == 0 {
		return
	}

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

	// Send at most maxBatchSamples per POST, oldest first. Pending is ordered
	// and AckThrough drops a prefix, so the remainder simply goes out on the
	// next flush rather than being lost.
	if len(pending) > maxBatchSamples {
		pending = pending[:maxBatchSamples]
	}

	samples := make([]*netrav1.HostSample, 0, len(pending))
	for _, e := range pending {
		samples = append(samples, e.Sample)
	}
	highest := pending[len(pending)-1].Seq

	req := &netrav1.IngestRequest{
		Seq:          highest,
		MetadataHash: c.metadataHash,
		HostSamples:  samples,
		// Anything sent after a failed flush is replayed history, and the hub
		// needs to know so it can invalidate the affected aggregate ranges.
		Backfill: c.replaying,
	}
	if c.sendMetadata {
		req.Metadata = c.metadata
	}

	resp, err := c.post(ctx, req)
	if err != nil {
		if errors.Is(err, ErrUnauthorized) {
			// The token is gone. Replaying forever would hammer the hub for
			// nothing, so the buffer is dropped and the operator has to act.
			//
			// The WHOLE buffer, not just the batch that was attempted.
			// AckThrough(highest) dropped at most maxBatchSamples of the
			// maxBufferSlots the ring can hold, and ScrapeOnce keeps adding on
			// every tick — so a revoked token left the ring pinned at capacity
			// indefinitely, which is exactly the state this branch says it is
			// avoiding. math.MaxUint64 is above every sequence number the agent
			// can issue.
			c.ring.AckThrough(math.MaxUint64)
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

	// Sequence numbers start at 1 (c.seq is incremented before the first
	// Add), so a genuine ack is never 0. A zero ack_seq means the hub sent a
	// zero-value or malformed response: treating it as success would leave
	// the buffer un-drained while the agent believes it is healthy, with no
	// backoff and no backfill flag on the next attempt.
	if resp.GetAckSeq() == 0 {
		slog.Warn("hub returned a zero ack_seq; treating flush as failed",
			"buffer_depth", c.ring.Depth())
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
			return ctx.Err()
		case <-ticker.C:
			c.ScrapeOnce(ctx)

			if time.Now().Before(flushNotBefore) {
				continue
			}

			if err := c.Flush(ctx); err != nil {
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
