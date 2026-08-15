package sim

import (
	"testing"
	"time"
)

// The memory parts must partition MemoryTotal rather than float free of it.
// The old model drew mem_used from a daily signal and buffcache as a flat 17%
// that was never carved out of it, so the two could sum past the host's whole
// RAM -- and a stacked chart fed from that draws a host as fuller than full.
func TestMemoryPartsNeverClaimMoreThanTheHostHas(t *testing.T) {
	for _, p := range Fleet() {
		g := NewGenerator(p, 1, testFrom, testTo)
		for _, at := range []time.Time{testFrom, testFrom.Add(37 * time.Hour), testTo.Add(-time.Minute)} {
			h := g.Scrape(at, Options{}).Host

			total := h.GetMemTotal()
			// Every band the chart stacks, plus the free gap it leaves at the
			// top. ARC is in here because it is memory the host really is
			// holding, and the chart draws it as its own band.
			sum := h.GetMemFree() + h.GetMemBuffers() + h.GetMemCached() +
				h.GetMemShared() + h.GetMemSreclaimable() + h.GetMemZfsArc()
			if sum > total {
				t.Errorf("%s at %s: parts sum to %d of %d bytes; the stack would overflow its ceiling",
					p.Hostname, at.Format(time.RFC3339), sum, total)
			}
			if got, want := h.GetMemBuffcache(),
				h.GetMemBuffers()+h.GetMemCached()+h.GetMemShared(); got != want {
				t.Errorf("%s: mem_buffcache = %d, want %d (buffers + cached + shared)",
					p.Hostname, got, want)
			}
			// used is total minus available, the collector's own definition.
			if got, want := h.GetMemUsed(), total-h.GetMemAvailable(); got != want {
				t.Errorf("%s: mem_used = %d, want %d (total - available)", p.Hostname, got, want)
			}
		}
	}
}

// The per-core stack's top edge is the MEAN of the cores, and the chart shows
// it against the same cpu_total the meter reads. A bias that does not average
// to 1 makes those two numbers disagree for the same instant.
func TestPerCoreBusyAveragesToTheHostsCpuTotal(t *testing.T) {
	p := Fleet()[2]
	g := NewGenerator(p, 1, testFrom, testTo)

	// Averaged over many scrapes: any single hour is one draw of the bias, and
	// it is the long-run mean that has to land on cpu_total.
	var sumCores, sumTotal float64
	const scrapes = 240
	for i := range scrapes {
		s := g.Scrape(testTo.Add(-time.Duration(i)*time.Hour), Options{})
		var mean float64
		for _, c := range s.Cores {
			mean += c.GetBusy()
		}
		sumCores += mean / float64(len(s.Cores))
		sumTotal += s.Host.GetCpuTotal()
	}

	ratio := sumCores / sumTotal
	if ratio < 0.9 || ratio > 1.1 {
		t.Errorf("mean per-core busy is %.2fx cpu_total, want ~1x: the stack would not "+
			"agree with the host's own number", ratio)
	}
}

// The container breakdown must be a split of the numbers beside it, not two
// more independent signals. A chart stacks cpu_user and cpu_system against
// cpu_pct, and the four memory parts against mem_used -- if they merely sit
// near those totals, the breakdown and the total describe different
// containers.
func TestContainerBreakdownIsASplitOfItsTotals(t *testing.T) {
	for _, p := range Fleet() {
		g := NewGenerator(p, 1, testFrom, testTo)
		for _, at := range []time.Time{testFrom, testFrom.Add(53 * time.Hour), testTo.Add(-time.Minute)} {
			for _, c := range g.Scrape(at, Options{}).Containers {
				parts := c.GetMemAnon() + c.GetMemFile() + c.GetMemShmem() + c.GetMemKernel()
				if parts != c.GetMemUsed() {
					t.Errorf("%s %s: memory parts sum to %d, want mem_used %d",
						p.Hostname, c.GetContainerKey(), parts, c.GetMemUsed())
				}
				// round2 is applied to each half, so the sum may differ from
				// the total by a hundredth without either being wrong.
				diff := c.GetCpuUser() + c.GetCpuSystem() - c.GetCpuPct()
				if diff > 0.011 || diff < -0.011 {
					t.Errorf("%s %s: user + system = %v, want cpu_pct %v",
						p.Hostname, c.GetContainerKey(),
						c.GetCpuUser()+c.GetCpuSystem(), c.GetCpuPct())
				}
				if c.GetCpuUser() < 0 || c.GetCpuSystem() < 0 {
					t.Errorf("%s %s: negative split, user %v system %v",
						p.Hostname, c.GetContainerKey(), c.GetCpuUser(), c.GetCpuSystem())
				}
			}
		}
	}
}
