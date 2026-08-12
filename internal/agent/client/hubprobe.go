package client

import (
	"context"
	"net"
	"net/url"
	"time"
)

// hubProbes is how many handshakes one scrape performs.
//
// Three, not one: the minimum of several is the best estimator of the true
// path RTT, because it is the sample least contaminated by queuing, and the
// spread between minimum and maximum is jitter. Three is enough for both and
// cheap enough to run every scrape -- six extra packets a minute per host.
const hubProbes = 3

// hubProbeTimeout bounds one handshake.
//
// Deliberately far below the scrape interval: a probe is a measurement, not a
// connectivity test, and a hub that takes five seconds to answer a SYN has
// already told us everything the number could. The real post carries its own
// 30s timeout and is what decides whether the hub is reachable.
const hubProbeTimeout = 5 * time.Second

// hubLatency is one scrape's measurement of the network path to the hub.
type hubLatency struct {
	minMs  uint32
	maxMs  uint32
	probed bool
}

// probeHub measures the TCP handshake to the hub's host and port.
//
// This is the network RTT that post_latency_ms is NOT. A post's round trip
// includes TLS, the request upload, the hub's own handling, the Postgres
// write and the response unmarshal, so a slow database inflates it exactly
// like a slow network does and it cannot answer "is the link bad". A
// handshake stops at SYN-ACK. The two together decompose the path: the gap
// between them is everything the hub does after accepting.
//
// TCP rather than ICMP, and not only because ICMP needs a raw socket and
// CAP_NET_RAW on an agent that ships as a container. ICMP is handled on the
// router slow path, routinely rate-limited and often dropped outright, so a
// red ICMP graph frequently means a filter rather than a problem. A handshake
// to the hub's real port traverses the exact path, ports and policy the real
// traffic uses.
//
// Packet loss is deliberately not measured here: tcp_retrans_segs_per_s
// already carries it, observed on real traffic rather than inferred from
// synthetic probes, at no extra packets.
func (c *Client) probeHub(ctx context.Context) hubLatency {
	addr, ok := hubDialAddress(c.cfg.HubURL)
	if !ok {
		return hubLatency{}
	}

	// Resolved OUTSIDE the timed section. DNS is a different fault with a
	// different fix, and folding a cold-cache lookup into the handshake would
	// report a 40ms path as 240ms once every TTL.
	resolver := c.resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return hubLatency{}
	}
	ips, err := resolver.LookupIPAddr(ctx, host)
	if err != nil || len(ips) == 0 {
		return hubLatency{}
	}
	resolved := net.JoinHostPort(ips[0].IP.String(), port)

	var out hubLatency
	for range hubProbes {
		if ctx.Err() != nil {
			break
		}

		rtt, ok := c.handshake(ctx, resolved)
		if !ok {
			// A failed handshake is not a slow one. Recording it as a latency
			// would put a 5000ms spike on the chart at the moment the link
			// broke; hub_connect_failures_total is what carries that.
			c.hubConnectFailures++
			continue
		}

		ms := uint32(rtt.Milliseconds())
		if !out.probed || ms < out.minMs {
			out.minMs = ms
		}
		if !out.probed || ms > out.maxMs {
			out.maxMs = ms
		}
		out.probed = true
	}

	return out
}

// handshake times one TCP connect and closes it immediately.
func (c *Client) handshake(ctx context.Context, addr string) (time.Duration, bool) {
	dialer := net.Dialer{Timeout: hubProbeTimeout}

	start := time.Now()
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	rtt := time.Since(start)
	if err != nil {
		return 0, false
	}
	// Closed at once. One short-lived connection per host per minute is
	// nothing, but left to accumulate it would not be.
	_ = conn.Close()

	return rtt, true
}

// hubDialAddress turns the hub URL into a host:port, defaulting the port from
// the scheme the way a browser would.
func hubDialAddress(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", false
	}
	if u.Port() != "" {
		return u.Host, true
	}
	switch u.Scheme {
	case "https":
		return net.JoinHostPort(u.Hostname(), "443"), true
	case "http":
		return net.JoinHostPort(u.Hostname(), "80"), true
	default:
		return "", false
	}
}
