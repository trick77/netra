package client

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/trick77/netra/internal/agent/buffer"
	"github.com/trick77/netra/internal/agent/collector"
	"github.com/trick77/netra/internal/agent/config"
)

// NewWithInterval builds a Client scraping at an arbitrary cadence.
//
// The production cadence is a fixed 60s (config.ScrapeInterval) and is
// deliberately not configurable, but Run's ticker behaviour — backoff,
// retry_after, cancellation, scraping while a flush is held off — can only be
// tested at a cadence measured in milliseconds. This file is compiled only
// into this package's test binary, so the escape hatch cannot leak into a
// production call site.
func NewWithInterval(cfg config.Config, collectors []collector.Collector, interval time.Duration) *Client {
	return newClient(cfg, collectors, interval)
}

// SetMachineIDPaths redirects the fingerprint source for one test and restores
// it afterwards. fingerprint() reads absolute host paths, which a test cannot
// otherwise reach without being root on the machine running the suite.
func SetMachineIDPaths(t *testing.T, paths ...string) {
	t.Helper()
	saved := machineIDPaths
	machineIDPaths = paths
	t.Cleanup(func() { machineIDPaths = saved })
}

// Fingerprint exposes the unexported fingerprint() to the external test package.
func Fingerprint() string { return fingerprint() }

// MaxBatchRowsForTest exposes the per-request row cap so a test can assert the
// drain respects it without restating the literal, which would then agree with
// a wrong value as readily as a right one.
const MaxBatchRowsForTest = maxBatchRows

// CountRowsForTest exposes the flush bound's row arithmetic, and
// AppendFamiliesForTest the merge that feeds it, so the completeness tests can
// walk buffer.Scrape reflectively rather than restating a list of families
// that would drift out of date exactly as quietly as the code it guards.
func CountRowsForTest(s *buffer.Scrape) int { return countRows(s) }

func AppendFamiliesForTest(s *buffer.Scrape, res *collector.Result) { appendFamilies(s, res) }

// HubDialAddressForTest exposes the hub URL -> host:port derivation.
func HubDialAddressForTest(raw string) (string, bool) { return hubDialAddress(raw) }

// SetResolverForTest points the probe's name lookup at a resolver the test
// controls, so the resolution path can be exercised without touching DNS.
//
// Every probe test used a literal 127.0.0.1 URL, which short-circuits
// LookupIPAddr entirely -- so the whole resolve-then-dial path, and this seam
// with it, went untested and a resolver failure's accounting was wrong.
func (c *Client) SetResolverForTest(r *net.Resolver) { c.resolver = r }

// HandshakeForTest exposes one timed connect over a candidate address list,
// so the multi-address fallback can be driven without a DNS server.
func HandshakeForTest(c *Client, ctx context.Context, addrs []string) (time.Duration, bool) {
	return c.handshake(ctx, addrs)
}
