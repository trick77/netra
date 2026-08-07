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

	"github.com/trick77/netra/agent/internal/buffer"
	"github.com/trick77/netra/agent/internal/collector"
	"github.com/trick77/netra/agent/internal/config"
	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

// ErrUnauthorized means the hub rejected this agent's token.
var ErrUnauthorized = errors.New("hub rejected the agent token")

// ingestPath is the hub endpoint agents post to.
const ingestPath = "/api/agent/v1/ingest"

// Client owns the scrape loop, the buffer and the HTTP conversation.
type Client struct {
	cfg        config.Config
	collectors []collector.Collector
	http       *http.Client
	ring       *buffer.Ring

	seq             uint64
	metadata        *netrav1.Metadata
	metadataHash    []byte
	sendMetadata    bool
	lastFlushFailed bool
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
		metadata:     md,
		metadataHash: HashMetadata(md),
		// The hub asks for metadata when it needs it; nothing is assumed.
		sendMetadata: false,
	}
}

// BufferDepth reports how many samples are waiting to be acknowledged.
func (c *Client) BufferDepth() int { return c.ring.Depth() }

// ScrapeOnce runs every collector and buffers the resulting sample.
//
// A collector that fails is logged and skipped: its fields stay unset, and
// the rest of the sample is still worth sending.
func (c *Client) ScrapeOnce(ctx context.Context) *netrav1.HostSample {
	sample := &netrav1.HostSample{TsMs: time.Now().UnixMilli()}

	for _, col := range c.collectors {
		if err := col.Collect(ctx, sample); err != nil {
			slog.Warn("collector failed", "collector", col.Name(), "err", err)
		}
	}

	c.seq++
	c.ring.Add(c.seq, sample)
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
			return err
		}
		c.lastFlushFailed = true
		return err
	}

	c.ring.AckThrough(resp.GetAckSeq())
	c.sendMetadata = resp.GetRequestMetadata()
	c.lastFlushFailed = false

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

// Run scrapes and flushes on the configured interval until ctx is cancelled.
func (c *Client) Run(ctx context.Context) error {
	ticker := time.NewTicker(c.cfg.Interval)
	defer ticker.Stop()

	backoff := time.Second

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			c.ScrapeOnce(ctx)

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
				sleep(ctx, jittered)
				if backoff < time.Minute {
					backoff *= 2
				}
				continue
			}

			backoff = time.Second
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
