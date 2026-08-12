package client_test

import (
	"context"
	"errors"
	"net"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/trick77/netra/internal/agent/client"
	"github.com/trick77/netra/internal/agent/collector"
	"github.com/trick77/netra/internal/agent/config"
)

func probeClient(t *testing.T, hubURL string) *client.Client {
	t.Helper()
	return client.NewWithInterval(
		config.Config{
			HubURL:       hubURL,
			Token:        "nta_test",
			BufferWindow: time.Hour,
			ProcRoot:     "../collector/testdata/proc1",
		},
		[]collector.Collector{},
		time.Minute,
	)
}

// The handshake measures the network path, which post_latency_ms cannot: a
// post's round trip includes TLS, the hub's own handling and the Postgres
// write, so a slow database inflates it exactly like a slow network does.
//
// It also does NOT lag a scrape. post_latency_ms is only known after the post
// returns, by which time the sample carrying it is already buffered; the
// probe runs inside the scrape, so the very first sample carries one.
func TestHubConnectIsMeasuredOnTheFirstScrape(t *testing.T) {
	srv := httptest.NewServer((&recorder{}).handler(t))
	defer srv.Close()

	c := probeClient(t, srv.URL)

	agent := c.ScrapeOnce(context.Background()).GetAgent()

	if agent.HubConnectUs == nil {
		t.Fatal("hub_connect_us unset on the first scrape; the probe runs inside the scrape and must not lag it")
	}
	if agent.HubConnectMaxUs == nil {
		t.Fatal("hub_connect_max_us unset")
	}
	if agent.GetHubConnectMaxUs() < agent.GetHubConnectUs() {
		t.Errorf("max %d < min %d", agent.GetHubConnectMaxUs(), agent.GetHubConnectUs())
	}
	// Contrast: the round trip genuinely cannot be known yet.
	if agent.PostLatencyMs != nil {
		t.Error("post_latency_ms set before any post; the two must not be conflated")
	}
	if agent.HubConnectFailuresTotal == nil {
		t.Error("hub_connect_failures_total unset; it must report 0 rather than nothing")
	}
}

// An unreachable hub has no round-trip time. Recording the probe timeout as a
// latency would put a 5000ms spike on the chart at the moment the link broke,
// which is exactly when someone is reading it.
func TestHubConnectUnsetNotZeroWhenTheHubIsUnreachable(t *testing.T) {
	// A port nothing listens on: the handshake is refused immediately.
	c := probeClient(t, "http://127.0.0.1:1")

	agent := c.ScrapeOnce(context.Background()).GetAgent()

	if agent.HubConnectUs != nil {
		t.Errorf("hub_connect_us = %d for an unreachable hub; want unset", agent.GetHubConnectUs())
	}
	if agent.HubConnectMaxUs != nil {
		t.Errorf("hub_connect_max_us = %d for an unreachable hub; want unset", agent.GetHubConnectMaxUs())
	}
	// The counter is what makes the outage visible while the gauge is NULL.
	if got := agent.GetHubConnectFailuresTotal(); got == 0 {
		t.Error("hub_connect_failures_total = 0 after failed handshakes; nothing would record the outage")
	}
}

// Cumulative for the agent's life, like post_failures_total: an agent that
// failed and then recovered must still report the failures, or the history of
// the outage vanishes the moment it ends.
func TestHubConnectFailuresAccumulateAcrossScrapes(t *testing.T) {
	c := probeClient(t, "http://127.0.0.1:1")
	ctx := context.Background()

	first := c.ScrapeOnce(ctx).GetAgent().GetHubConnectFailuresTotal()
	second := c.ScrapeOnce(ctx).GetAgent().GetHubConnectFailuresTotal()

	if second <= first {
		t.Errorf("failures went %d -> %d; the counter must accumulate", first, second)
	}
}

// The port comes from the URL, defaulting from the scheme the way a browser
// would -- the probe has to reach the port the real traffic uses, or it is
// measuring a path nothing else takes.
func TestHubDialAddressDefaultsThePortFromTheScheme(t *testing.T) {
	cases := []struct {
		url  string
		want string
		ok   bool
	}{
		{"https://hub.example.com", "hub.example.com:443", true},
		{"http://hub.example.com", "hub.example.com:80", true},
		{"http://hub.example.com:8080", "hub.example.com:8080", true},
		{"https://[2001:db8::1]:9000", "[2001:db8::1]:9000", true},
		{"https://[2001:db8::1]", "[2001:db8::1]:443", true},
		{"ftp://hub.example.com", "", false},
		{"not a url at all", "", false},
	}
	for _, tc := range cases {
		got, ok := client.HubDialAddressForTest(tc.url)
		if ok != tc.ok {
			t.Errorf("%q: ok = %v, want %v", tc.url, ok, tc.ok)
			continue
		}
		if got != tc.want {
			t.Errorf("%q: addr = %q, want %q", tc.url, got, tc.want)
		}
	}
}

// Microseconds, not milliseconds.
//
// A hub on the same LAN or in the same datacentre answers a SYN in 200-900us.
// As milliseconds that truncates to 0 on every scrape -- the exact value the
// schema calls impossible, since it claims an instantaneous connection -- and
// min and max would both be 0, so the jitter that is half the reason for
// three handshakes could never be seen. A loopback hub is the extreme case of
// exactly that, so it is what this asserts against.
func TestHubConnectHasSubMillisecondResolution(t *testing.T) {
	srv := httptest.NewServer((&recorder{}).handler(t))
	defer srv.Close()

	c := probeClient(t, srv.URL)

	agent := c.ScrapeOnce(context.Background()).GetAgent()

	if agent.HubConnectUs == nil {
		t.Fatal("hub_connect_us unset against a live loopback hub")
	}
	if got := agent.GetHubConnectUs(); got == 0 {
		t.Error("hub_connect_us = 0 for a completed handshake; the unit cannot represent the latency it is measuring")
	}
}

// A hub URL that cannot be resolved is a failure to REACH the hub, not an
// absence of measurement. Counting it is what keeps the schema's promise that
// a NULL gauge always has a rising counter beside it -- without it a resolver
// outage reads as "never probed", which is what the proto comment says it is
// not.
func TestHubConnectCountsAResolverFailureAsAFailure(t *testing.T) {
	c := probeClient(t, "http://hub.invalid:9999")
	// A resolver pointed at a black hole on a port nothing answers: every
	// lookup fails rather than returning NXDOMAIN quickly.
	c.SetResolverForTest(&net.Resolver{
		PreferGo: true,
		Dial: func(context.Context, string, string) (net.Conn, error) {
			return nil, errors.New("resolver unavailable")
		},
	})

	agent := c.ScrapeOnce(context.Background()).GetAgent()

	if agent.HubConnectUs != nil {
		t.Errorf("hub_connect_us = %d with an unresolvable hub; want unset", agent.GetHubConnectUs())
	}
	if got := agent.GetHubConnectFailuresTotal(); got == 0 {
		t.Error("hub_connect_failures_total = 0 after a resolver failure; the outage would be invisible")
	}
}

// EVERY resolved address is a candidate, not the first.
//
// Pinning ips[0] made the probe disagree with the posts, which dial the
// hostname and get the standard multi-address fallback. On a dual-stack host
// whose IPv6 egress to the hub is blocked, every handshake would fail forever
// -- gauges NULL, failures climbing -- while the POSTs succeeded over IPv4. A
// permanent false outage against a healthy hub is worse than no measurement.
func TestHubProbeFallsBackToTheSecondAddress(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	c := probeClient(t, "http://127.0.0.1:1")

	// First address refuses immediately -- the shape of a family whose egress
	// to the hub is blocked. Second is the live listener.
	dead := "127.0.0.1:1"
	alive := ln.Addr().String()

	if _, ok := client.HandshakeForTest(c, context.Background(), []string{dead}); ok {
		t.Fatal("a refused address reported a successful handshake")
	}
	if _, ok := client.HandshakeForTest(c, context.Background(), []string{dead, alive}); !ok {
		t.Error("no fallback past a dead first address; a dual-stack host with one family blocked would report a permanent false outage")
	}
}

// The probe must not leave connections behind. One per host per minute is
// nothing; accumulating them for the life of the agent would not be.
func TestHubProbeClosesEveryConnectionItOpens(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	// Accept and immediately read; a closed peer returns EOF at once.
	closed := make(chan struct{}, 16)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				buf := make([]byte, 1)
				_, _ = c.Read(buf) // returns on peer close
				_ = c.Close()
				select {
				case closed <- struct{}{}:
				default:
				}
			}(conn)
		}
	}()

	c := probeClient(t, "http://"+ln.Addr().String())
	c.ScrapeOnce(context.Background())

	// Three handshakes, so at least one peer close must be observed promptly.
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("no connection was closed by the probe; they would accumulate for the life of the agent")
	}
}
