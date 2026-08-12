package client_test

import (
	"context"
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

	if agent.HubConnectMs == nil {
		t.Fatal("hub_connect_ms unset on the first scrape; the probe runs inside the scrape and must not lag it")
	}
	if agent.HubConnectMaxMs == nil {
		t.Fatal("hub_connect_max_ms unset")
	}
	if agent.GetHubConnectMaxMs() < agent.GetHubConnectMs() {
		t.Errorf("max %d < min %d", agent.GetHubConnectMaxMs(), agent.GetHubConnectMs())
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

	if agent.HubConnectMs != nil {
		t.Errorf("hub_connect_ms = %d for an unreachable hub; want unset", agent.GetHubConnectMs())
	}
	if agent.HubConnectMaxMs != nil {
		t.Errorf("hub_connect_max_ms = %d for an unreachable hub; want unset", agent.GetHubConnectMaxMs())
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
