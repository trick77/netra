package collector_test

import (
	"context"
	"testing"
	"time"

	"github.com/trick77/netra/internal/agent/collector"
	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

func netAt(t *testing.T, c *collector.Network, at time.Time) *collector.Result {
	t.Helper()
	c.SetClockForTest(func() time.Time { return at })
	res, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	return res
}

func netRow(t *testing.T, rows []*netrav1.NetSample, iface string) *netrav1.NetSample {
	t.Helper()
	for _, r := range rows {
		if r.GetIface() == iface {
			return r
		}
	}
	t.Fatalf("no row for %q in %d rows", iface, len(rows))
	return nil
}

// eth0 over ten seconds: rx 2000000 -> 2100000 = 10000 B/s, tx 1000000 ->
// 1050000 = 5000 B/s, rx errs 10 -> 30 = 2/s, tx errs 5 -> 15 = 1/s. Every
// expected value is distinct, so a transposed field fails rather than passes
// by coincidence.
func TestNetworkComputesRatesPerInterface(t *testing.T) {
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	testee := collector.NewNetwork("testdata/netdev/first", time.Minute)

	res := netAt(t, testee, base)
	if len(res.Nets) != 0 {
		t.Fatalf("first scrape produced %d rows, want 0", len(res.Nets))
	}

	testee.SetProcRootForTest("testdata/netdev/second")
	res = netAt(t, testee, base.Add(10*time.Second))

	eth0 := netRow(t, res.Nets, "eth0")
	for _, c := range []struct {
		name string
		got  float64
		want float64
	}{
		{"rx_bytes", eth0.GetRxBytes(), 10000},
		{"tx_bytes", eth0.GetTxBytes(), 5000},
		{"rx_errs", eth0.GetRxErrs(), 2},
		{"tx_errs", eth0.GetTxErrs(), 1},
	} {
		if c.got != c.want {
			t.Errorf("eth0 %s = %v, want %v", c.name, c.got, c.want)
		}
	}
	if eth0.GetTsMs() == 0 {
		t.Error("row carries no ts_ms")
	}
}

// Virtual interfaces are excluded.
//
// lo is not network traffic. veth* and br-* are the host side of container
// networking, so their bytes are the SAME bytes already counted on the real
// interface -- including them double-counts, and on a host with forty
// containers it adds forty series that measure nothing new. docker0 is the
// same for the default bridge.
func TestNetworkExcludesLoopbackAndContainerInterfaces(t *testing.T) {
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	testee := collector.NewNetwork("testdata/netdev/first", time.Minute)
	netAt(t, testee, base)

	testee.SetProcRootForTest("testdata/netdev/second")
	res := netAt(t, testee, base.Add(10*time.Second))

	for _, r := range res.Nets {
		switch r.GetIface() {
		case "lo":
			t.Error("lo reported; loopback is not network traffic")
		case "docker0", "veth123", "br-abc":
			t.Errorf("%s reported; container-side interfaces double-count the real interface", r.GetIface())
		}
	}
	if len(res.Nets) != 2 {
		t.Errorf("interfaces = %d, want 2 (eth0 and wlan0)", len(res.Nets))
	}
}

// A reboot resets the counters. No row rather than a negative rate or a spike.
func TestNetworkEmitsNoRowAfterACounterReset(t *testing.T) {
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	testee := collector.NewNetwork("testdata/netdev/second", time.Minute)
	netAt(t, testee, base)

	testee.SetProcRootForTest("testdata/netdev/first")
	res := netAt(t, testee, base.Add(10*time.Second))

	if len(res.Nets) != 0 {
		t.Errorf("rows after a counter reset = %d, want 0", len(res.Nets))
	}
}

// An unreadable /proc/net/dev is an error, not an empty result.
func TestNetworkReportsAnUnreadableNetDev(t *testing.T) {
	testee := collector.NewNetwork(t.TempDir(), time.Minute)

	res, err := testee.Collect(context.Background())
	if err == nil {
		t.Fatal("Collect succeeded with no net/dev, want an error")
	}
	if res != nil {
		t.Errorf("Collect returned %+v alongside an error; want nil", res)
	}
}
