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

// hubResolveTimeout bounds the name lookup.
//
// It needs its own bound because the probe runs on the goroutine that owns
// the ring and the flush, and LookupIPAddr has no deadline of its own beyond
// the context. Against a black-holed resolver the system's full retry budget
// -- tens of seconds -- would stall the scrape loop, so a probe would have
// cost the agent the very samples it exists to describe.
const hubResolveTimeout = 2 * time.Second

// hubLatency is one scrape's measurement of the network path to the hub.
type hubLatency struct {
	minUs  uint32
	maxUs  uint32
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
	addrs, ok := c.hubTargets(ctx)
	if !ok {
		// A hub URL that cannot be parsed or resolved is a failure to reach
		// it, not an absence of measurement. Counting it is what keeps the
		// schema's promise that a NULL gauge always has a rising counter
		// beside it -- otherwise a resolver outage reads as "never probed".
		c.hubConnectFailures++
		return hubLatency{}
	}

	var out hubLatency
	for range hubProbes {
		if ctx.Err() != nil {
			break
		}

		rtt, ok := c.handshake(ctx, addrs)
		if !ok {
			// A failed handshake is not a slow one. Recording it as a latency
			// would put a 5000ms spike on the chart at the moment the link
			// broke; hub_connect_failures_total is what carries that.
			c.hubConnectFailures++
			continue
		}

		// Microseconds. A hub on the same LAN answers in 200-900us, which as
		// milliseconds truncates to the 0 the schema calls impossible -- and
		// would make min and max identical, so jitter could never be seen.
		us := uint32(rtt.Microseconds())
		if !out.probed || us < out.minUs {
			out.minUs = us
		}
		if !out.probed || us > out.maxUs {
			out.maxUs = us
		}
		out.probed = true
	}

	return out
}

// hubTargets resolves the hub to every address it answers on.
//
// Resolution happens OUTSIDE the timed section: DNS is a different fault with
// a different fix, and folding a cold-cache lookup into the handshake would
// report a 40ms path as 240ms once every TTL.
//
// EVERY address is returned, not the first. Pinning ips[0] made the probe
// disagree with the posts, which dial the hostname and get the standard
// multi-address fallback: on a dual-stack host whose IPv6 egress to the hub
// is blocked, every handshake would fail forever -- gauges NULL and the
// failure counter climbing -- while the POSTs succeeded over IPv4. A
// permanent false outage against a healthy hub is worse than no measurement.
func (c *Client) hubTargets(ctx context.Context) ([]string, bool) {
	addr, ok := hubDialAddress(c.cfg.HubURL)
	if !ok {
		return nil, false
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, false
	}

	resolver := c.resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}

	ctx, cancel := context.WithTimeout(ctx, hubResolveTimeout)
	defer cancel()

	ips, err := resolver.LookupIPAddr(ctx, host)
	if err != nil || len(ips) == 0 {
		return nil, false
	}

	out := make([]string, 0, len(ips))
	for _, ip := range ips {
		out = append(out, net.JoinHostPort(ip.IP.String(), port))
	}
	return out, true
}

// handshake times one TCP connect and closes it immediately.
//
// It tries each address in turn and times only the attempt that succeeded, so
// a blocked address family costs the measurement nothing but does not corrupt
// it either.
func (c *Client) handshake(ctx context.Context, addrs []string) (time.Duration, bool) {
	dialer := net.Dialer{Timeout: hubProbeTimeout}

	for _, addr := range addrs {
		if ctx.Err() != nil {
			return 0, false
		}

		start := time.Now()
		conn, err := dialer.DialContext(ctx, "tcp", addr)
		rtt := time.Since(start)
		if err != nil {
			continue
		}
		// Closed at once. One short-lived connection per host per minute is
		// nothing, but left to accumulate it would not be.
		_ = conn.Close()

		return rtt, true
	}
	return 0, false
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
