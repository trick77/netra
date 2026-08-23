package client

import (
	"context"
	"errors"
	"log/slog"

	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
)

// CheckHub answers "is this agent talking to its hub" at startup, in seconds,
// instead of leaving the operator to infer it from silence.
//
// Run blocks on its ticker before the first scrape and the first flush, so
// every hub-side fault -- a wrong URL, a revoked token, a proxy that routes
// /api/agent/ nowhere -- used to take a full minute to reach the log. An
// operator who has just run `docker compose up -d` reads the log immediately,
// finds one line saying the agent started, and reasonably concludes it works.
//
// The check is a real POST to the real ingest path with the real token,
// carrying no samples at all. Nothing cheaper actually tests what breaks:
// a TCP handshake (probeHub) proves the port answers and says nothing about
// routing or authentication, and there is no unauthenticated endpoint on the
// agent route to GET -- deliberately, since only PathPrefix(/api/agent/) is
// exposed to the internet. Every family insert on the hub returns early on an
// empty slice and host_current is only touched when a sample is present, so a
// sample-free batch is a no-op that still exercises auth, TLS, routing and the
// proxy in front.
//
// It NEVER fails the agent. A hub that is down at the moment its agents boot
// is the ordinary case the ring buffer exists for, and refusing to start would
// turn a recoverable outage into a fleet that has to be restarted by hand once
// it ends. This only makes the fault legible.
func (c *Client) CheckHub(ctx context.Context) {
	// The hash rides along, empty batch or not. Without it the hub compares
	// its stored hash against nothing, answers "send me metadata" to every
	// agent on every restart, and re-saves a block that never changed.
	resp, err := c.post(ctx, &netrav1.IngestRequest{MetadataHash: c.metadataHash})

	switch {
	case err == nil:
		slog.Info("hub reachable and token accepted", "hub", c.cfg.HubURL)
		// The hub asks for metadata when its stored hash does not match the one
		// this check carried, and that answer is as valid here as on any other
		// post. Honouring it means the FIRST real batch carries the block
		// rather than the second.
		//
		// Only ever set TRUE. A "no" from the hub is an answer about the hash
		// this check sent, not permission to cancel a send something else has
		// already decided is owed.
		if resp.GetRequestMetadata() {
			c.sendMetadata = true
		}

	case errors.Is(err, ErrUnauthorized):
		// Error, not Warn: unlike an unreachable hub, this does not fix itself.
		// The agent keeps running and keeps buffering, but every sample it
		// takes meanwhile is destined for a hub that will refuse it.
		slog.Error("hub rejected the agent token; check AGENT_TOKEN and that this host still exists",
			"hub", c.cfg.HubURL, "err", err)

	default:
		// Everything else: DNS, TLS, connection refused, and the 404 that a
		// misrouted reverse proxy returns -- which is indistinguishable from a
		// healthy agent until something posts.
		slog.Warn("hub did not accept the startup check; samples will buffer and retry",
			"hub", c.cfg.HubURL, "err", err)
	}
}
