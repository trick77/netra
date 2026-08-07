// Package client scrapes collectors and posts batches to the hub.
package client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
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

// minAdoptedInterval and maxAdoptedInterval bound a hub-supplied interval_s
// before it is adopted, so a malformed or malicious response cannot make the
// agent scrape in a tight loop or effectively stop scraping.
const (
	minAdoptedInterval = time.Second
	maxAdoptedInterval = 24 * time.Hour
)

// maxAdoptedRetryAfter caps a hub-supplied retry_after_s so a bad value
// cannot stall the agent indefinitely.
const maxAdoptedRetryAfter = 10 * time.Minute

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

	// interval is the current scrape cadence. It starts at cfg.Interval and
	// may be overridden by a hub-supplied interval_s. Collectors keep the
	// interval they were constructed with (only CPU exposes it, and only for
	// reporting; its percentage math is delta-based, not fixed-interval), so
	// adopting a new value here does not require rebuilding them.
	interval time.Duration

	seq             uint64
	metadata        *netrav1.Metadata
	metadataHash    []byte
	sendMetadata    bool
	lastFlushFailed bool
	retryAfter      time.Duration
}

// New builds a Client. Buffer capacity is derived from the configured window
// and interval, so NETRA_BUFFER_WINDOW is expressed in time rather than in a
// sample count nobody can reason about.
func New(cfg config.Config, collectors []collector.Collector) *Client {
	capacity := int(cfg.BufferWindow / cfg.Interval)
	if capacity < 1 {
		capacity = 1
	}

	md := BuildMetadata(cfg)

	return &Client{
		cfg:          cfg,
		collectors:   collectors,
		http:         &http.Client{Timeout: 30 * time.Second},
		ring:         buffer.New(capacity),
		interval:     cfg.Interval,
		metadata:     md,
		metadataHash: HashMetadata(md),
		// The hub asks for metadata when it needs it; nothing is assumed.
		sendMetadata: false,
	}
}

// BufferDepth reports how many samples are waiting to be acknowledged.
func (c *Client) BufferDepth() int { return c.ring.Depth() }

// Interval reports the scrape cadence currently in effect, which may have
// been adopted from a hub-supplied interval_s.
func (c *Client) Interval() time.Duration { return c.interval }

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
// Some collectors (CPU) need a baseline scrape before they can report a
// delta; calling Collect once here gives them that baseline without leaving
// behind a stored row whose values are NULL for a reason ("not computable
// yet") that is indistinguishable from an absent subsystem.
func (c *Client) Prime(ctx context.Context) {
	c.collect(ctx)
}

// collect runs every collector and returns the resulting sample, without
// touching the sequence counter or the ring buffer.
func (c *Client) collect(ctx context.Context) *netrav1.HostSample {
	sample := &netrav1.HostSample{TsMs: time.Now().UnixMilli()}

	for _, col := range c.collectors {
		if err := col.Collect(ctx, sample); err != nil {
			slog.Warn("collector failed", "collector", col.Name(), "err", err)
		}
	}

	return sample
}

// Flush posts every buffered sample, oldest first, and drops the ones the hub
// acknowledges.
func (c *Client) Flush(ctx context.Context) error {
	pending := c.ring.Pending()
	if len(pending) == 0 {
		return nil
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
		Backfill: c.lastFlushFailed,
	}
	if c.sendMetadata {
		req.Metadata = c.metadata
	}

	resp, err := c.post(ctx, req)
	if err != nil {
		if errors.Is(err, ErrUnauthorized) {
			// The token is gone. Replaying forever would hammer the hub for
			// nothing, so the buffer is dropped and the operator has to act.
			c.ring.AckThrough(highest)
			c.lastFlushFailed = false
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

		c.lastFlushFailed = true
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
		c.lastFlushFailed = true
		return fmt.Errorf("hub returned ack_seq=0 for a batch of %d samples", len(samples))
	}

	c.ring.AckThrough(resp.GetAckSeq())
	c.sendMetadata = resp.GetRequestMetadata()
	c.lastFlushFailed = false
	c.retryAfter = 0

	if iv := time.Duration(resp.GetIntervalS()) * time.Second; iv >= minAdoptedInterval && iv <= maxAdoptedInterval {
		if iv != c.interval {
			slog.Info("adopting hub-supplied scrape interval", "old", c.interval, "new", iv)
			c.interval = iv
		}
	}

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

	httpResp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("post to hub: %w", err)
	}
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

// Run scrapes and flushes on the configured interval until ctx is cancelled.
func (c *Client) Run(ctx context.Context) error {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	backoff := time.Second

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			c.ScrapeOnce(ctx)

			intervalBefore := c.interval

			if err := c.Flush(ctx); err != nil {
				if errors.Is(err, ErrUnauthorized) {
					// Retry slowly: a revoked agent must not hammer the hub.
					slog.Error("hub rejected the agent token; retrying slowly", "err", err)
					sleep(ctx, 5*time.Minute)
					continue
				}

				slog.Warn("flush failed; samples are buffered",
					"err", err, "buffer_depth", c.ring.Depth(),
					"dropped_total", c.ring.Dropped())

				// Jitter keeps a fleet from reconnecting in lockstep after a
				// hub restart.
				jittered := backoff + time.Duration(rand.Int64N(int64(backoff)))

				// A hub-specified retry_after replaces the agent's own
				// backoff rather than merely bounding it below: the hub
				// knows more about how long its own outage will last.
				wait := jittered
				if c.retryAfter > 0 {
					wait = c.retryAfter
				}

				sleep(ctx, wait)
				if backoff < time.Minute {
					backoff *= 2
				}
				continue
			}

			backoff = time.Second
			if c.interval != intervalBefore {
				ticker.Reset(c.interval)
			}
		}
	}
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
