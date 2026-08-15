package httpapi_test

import (
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/trick77/netra/internal/hub/httpapi"
	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
)

const (
	scrapeA = 1_700_000_000_000
	scrapeB = 1_700_000_060_000
)

func net(ts int64, iface string, rx, tx *float64) *netrav1.NetSample {
	return &netrav1.NetSample{TsMs: ts, Iface: iface, RxBytes: rx, TxBytes: tx}
}

func wantValue(t *testing.T, name string, got *float64, want float64) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s = nil, want %v", name, want)
	}
	if *got != want {
		t.Fatalf("%s = %v, want %v", name, *got, want)
	}
}

func wantAbsent(t *testing.T, name string, got *float64) {
	t.Helper()
	if got != nil {
		t.Fatalf("%s = %v, want nil — absent is not zero", name, *got)
	}
}

// A host's traffic is the sum over its interfaces, which is what the fleet
// tile prints as "ingress + egress".
func TestLatestNetTotalsSumsEveryInterface(t *testing.T) {
	rx, tx := httpapi.LatestNetTotalsForTest([]*netrav1.NetSample{
		net(scrapeA, "eth0", proto.Float64(100), proto.Float64(10)),
		net(scrapeA, "eth1", proto.Float64(25), proto.Float64(5)),
	})

	wantValue(t, "rx", rx, 125)
	wantValue(t, "tx", tx, 15)
}

// The load-bearing one. A post carries several scrapes -- more of them the
// longer the agent could not reach the hub -- and totalling all of them would
// report a host's traffic as a multiple of itself, growing with the size of
// the backlog. Only the newest instant counts.
func TestLatestNetTotalsUsesOnlyTheNewestScrape(t *testing.T) {
	rx, tx := httpapi.LatestNetTotalsForTest([]*netrav1.NetSample{
		net(scrapeA, "eth0", proto.Float64(1_000_000), proto.Float64(1_000_000)),
		net(scrapeB, "eth0", proto.Float64(100), proto.Float64(10)),
	})

	wantValue(t, "rx", rx, 100)
	wantValue(t, "tx", tx, 10)
}

// Order is not guaranteed by anything: the agent builds the batch per
// collector, and a reordered post must not make an older scrape win.
func TestLatestNetTotalsIgnoresTheOrderSamplesArriveIn(t *testing.T) {
	rx, _ := httpapi.LatestNetTotalsForTest([]*netrav1.NetSample{
		net(scrapeB, "eth0", proto.Float64(100), nil),
		net(scrapeA, "eth0", proto.Float64(1_000_000), nil),
		net(scrapeB, "eth1", proto.Float64(50), nil),
	})

	wantValue(t, "rx", rx, 150)
}

// rx_bytes and tx_bytes are individually optional in NetSample. Answering 0
// for the missing half would claim traffic in one direction and none in the
// other, where the truth is that one was not measured.
func TestLatestNetTotalsKeepsTheDirectionsIndependent(t *testing.T) {
	rx, tx := httpapi.LatestNetTotalsForTest([]*netrav1.NetSample{
		net(scrapeA, "eth0", proto.Float64(100), nil),
	})

	wantValue(t, "rx", rx, 100)
	wantAbsent(t, "tx", tx)
}

// A post with no net samples at all -- the collector failed this scrape, or
// the capability is off. NULL reaches the column, the UI renders its absent
// marker, and the upsert's coalesce keeps whatever was last known rather than
// flickering the tile to absent and back.
func TestLatestNetTotalsAnswersAbsentForNoSamples(t *testing.T) {
	rx, tx := httpapi.LatestNetTotalsForTest(nil)

	wantAbsent(t, "rx", rx)
	wantAbsent(t, "tx", tx)
}

// Zero is a reading, not a gap: an idle interface really did move no bytes,
// and that must survive as 0 rather than becoming absent.
func TestLatestNetTotalsKeepsAMeasuredZero(t *testing.T) {
	rx, tx := httpapi.LatestNetTotalsForTest([]*netrav1.NetSample{
		net(scrapeA, "eth0", proto.Float64(0), proto.Float64(0)),
	})

	wantValue(t, "rx", rx, 0)
	wantValue(t, "tx", tx, 0)
}
