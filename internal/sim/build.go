package sim

import (
	"fmt"
	"math"
	"time"

	"google.golang.org/protobuf/proto"

	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

// Scrape is one instant of one simulated host: the host row and every
// per-entity family that goes with it.
//
// It mirrors the agent's buffer.Scrape rather than flattening straight into
// an IngestRequest, for the same reason the agent keeps it: a batch spans
// many scrapes, and rows have to stay grouped by the instant they belong to
// so a partial batch cannot lose a family.
type Scrape struct {
	Host          *netrav1.HostSample
	Cores         []*netrav1.CpuCoreSample
	Disks         []*netrav1.DiskIoSample
	Sensors       []*netrav1.SensorSample
	Nets          []*netrav1.NetSample
	Collectors    []*netrav1.CollectorSample
	Events        []*netrav1.Event
	Containers    []*netrav1.ContainerSample
	Filesystems   []*netrav1.FilesystemSample
	Smart         []*netrav1.SmartAttribute
	Processes     []*netrav1.ProcessSample
	SystemdEvents []*netrav1.SystemdUnitEvent
	PackageEvents []*netrav1.PackageEvent
	Addresses     []*netrav1.HostAddress
	Packages      []*netrav1.HostPackage
}

// Rows counts what this scrape will insert. The batcher bounds a POST by rows
// rather than by scrapes because a 32-core host is 100 rows per scrape and a
// 1-vCPU host is a dozen -- counting scrapes would make the batch size depend
// on which host is being simulated.
func (s *Scrape) Rows() int {
	n := len(s.Cores) + len(s.Disks) + len(s.Sensors) + len(s.Nets) +
		len(s.Collectors) + len(s.Events) + len(s.Containers) +
		len(s.Filesystems) + len(s.Smart) + len(s.Processes) +
		len(s.SystemdEvents) + len(s.PackageEvents) +
		len(s.Addresses) + len(s.Packages)
	if s.Host != nil {
		n++
	}
	return n
}

// Options selects which families a scrape carries. They are not all emitted
// at every instant: SMART changes over hours and reading it spins up sleeping
// drives, process rows are retained for 48 hours so generating 90 days of
// them writes 88 days into a retention policy, and inventory describes what
// the host HAS rather than what it measured.
type Options struct {
	Smart      bool
	Processes  bool
	Collectors bool
	Inventory  bool
}

// Generator produces one host's scrapes across a fixed window.
type Generator struct {
	p     *Profile
	sig   signal
	from  time.Time
	to    time.Time
	sched *schedule

	boot       time.Time
	agentStart time.Time
}

// liveHorizon is how far past the backfill window discrete events are
// scheduled.
//
// Without it --live is silent: the schedule is laid out once over [from,to)
// with `to` pinned at start-up, and every live instant is at or after `to`, so
// nothing is ever due again. A process left running overnight wrote one
// baseline row per unit and then no event at all, which makes the events UI
// -- the thing most likely to be developed against a live simulator -- look
// permanently empty. Events past `to` cost nothing in a backfill-only run:
// the grid never reaches them.
const liveHorizon = 90 * 24 * time.Hour

// NewGenerator prepares a host for the window [from,to). The window bounds
// matter beyond iteration: the slow ramps -- a filesystem filling, a drive
// accumulating power-on hours, reallocated sectors appearing -- are
// interpolated across it, so the same profile tells a different story over a
// day than over three months.
//
// Ramps use [from,to); the event schedule deliberately runs further, to
// to+liveHorizon. A ramp past `to` would be extrapolation, and ramp() clamps
// it instead -- a filesystem that reached 91% full stays there in live mode
// rather than marching past 100%.
func NewGenerator(p *Profile, seed uint64, from, to time.Time) *Generator {
	sig := newSignal(seed ^ hashString(p.Hostname))
	return &Generator{
		p:     p,
		sig:   sig,
		from:  from,
		to:    to,
		sched: newSchedule(p, sig, from, to.Add(liveHorizon)),
		// Booted before the window opens, so uptime is never a number that
		// implies the host came up exactly when the simulator started.
		boot:       from.Add(-37*24*time.Hour - 4*time.Hour),
		agentStart: from.Add(-6 * time.Hour),
	}
}

// Scrape builds one instant.
func (g *Generator) Scrape(ts time.Time, opt Options) *Scrape {
	s := &Scrape{}
	cpu := g.cpuTotal(ts)

	s.Host = g.hostSample(ts, cpu)
	s.Cores = g.cores(ts, cpu)
	s.Disks = g.diskIO(ts, cpu)
	s.Sensors = g.sensors(ts, cpu)
	s.Nets = g.nets(ts)
	s.Containers = g.containers(ts, cpu)
	s.Filesystems = g.filesystems(ts)

	if opt.Collectors {
		s.Collectors = g.collectorHealth(ts)
	}
	if opt.Smart {
		s.Smart = g.smart(ts)
	}
	// Gated on the profile as well as the instant. A host that emits process
	// rows without listing the collector that produces them is a state no
	// real fleet can reach, and a read-side query joining process data to
	// collector health would report the collector as absent while the rows
	// sit right there.
	if opt.Processes && g.p.runsCollector("processes") {
		s.Processes = g.processes(ts, cpu)
	}
	if opt.Inventory {
		s.Addresses = g.addresses()
		s.Packages = g.packages(ts)
	}

	for _, e := range g.sched.due(ts) {
		switch {
		case e.unit != nil:
			u := proto.Clone(e.unit).(*netrav1.SystemdUnitEvent)
			u.TsMs = ts.UnixMilli()
			s.SystemdEvents = append(s.SystemdEvents, u)
		case e.pkg != nil:
			pk := proto.Clone(e.pkg).(*netrav1.PackageEvent)
			pk.TsMs = ts.UnixMilli()
			s.PackageEvents = append(s.PackageEvents, pk)
		case e.event != nil:
			ev := proto.Clone(e.event).(*netrav1.Event)
			ev.TsMs = ts.UnixMilli()
			s.Events = append(s.Events, ev)
		}
	}
	return s
}

// cpuTotal is the value everything else on the host is derived from, so a
// busy minute is busy in the load average, the core breakdown, the sensor
// readings and the container rows at the same time. Deriving each of those
// independently produced a host whose CPU chart and temperature chart
// disagreed about whether anything was happening.
func (g *Generator) cpuTotal(ts time.Time) float64 {
	v := g.sig.daily("cpu", ts, g.p.CPUBase, 0.55, 0.14)
	v = g.sig.spike("cpu", ts, v, 0.004, 2.6)
	return clamp(v, 0.2, 99)
}

func (g *Generator) hostSample(ts time.Time, cpu float64) *netrav1.HostSample {
	p := g.p
	h := &netrav1.HostSample{TsMs: ts.UnixMilli()}

	steal := 0.0
	if p.StealPct > 0 {
		steal = clamp(g.sig.daily("steal", ts, p.StealPct, 0.6, 0.5), 0, 40)
		h.CpuSteal = proto.Float64(round2(steal))
	}
	// The breakdown sums to cpu_total. iowait is proportionally larger on the
	// hosts with spinning disks, which is the whole reason the split is not a
	// fixed ratio everywhere.
	iowaitShare := 0.08
	if g.hasSpinningDisk() {
		iowaitShare = 0.21
	}
	iowait := cpu * iowaitShare
	system := cpu * 0.26
	user := cpu - iowait - system
	if user < 0 {
		user = 0
	}

	h.CpuTotal = proto.Float64(round2(cpu))
	h.CpuUser = proto.Float64(round2(user))
	h.CpuSystem = proto.Float64(round2(system))
	h.CpuIowait = proto.Float64(round2(iowait))
	h.CpuIdle = proto.Float64(round2(clamp(100-cpu-steal, 0, 100)))

	// Memory is generated as a PARTITION of MemoryTotal, not as independent
	// fractions. The old model drew mem_used from a daily signal and
	// buffcache as a flat 17% that was never carved out of it, so the two
	// could sum past 100% of the host's RAM -- a stacked chart fed from that
	// draws a host as fuller than full. Every part below is subtracted from
	// the same budget, and free is whatever is left.
	total := float64(p.MemoryTotal)

	// ARC is memory the host really is holding, so it comes out of the
	// budget before free does. Computed here rather than with the other
	// optional subsystems below because the partition has to account for it.
	var arc uint64
	if p.ZFSArc > 0 {
		arc = uint64(float64(p.ZFSArc) * clamp(g.sig.daily("arc", ts, 0.86, 0.1, 0.05), 0.2, 1))
	}

	shared := uint64(total * clamp(g.sig.daily("shmem", ts, 0.02, 0.4, 0.2), 0.002, 0.06))
	buffers := uint64(total * clamp(g.sig.daily("buffers", ts, 0.03, 0.3, 0.2), 0.005, 0.07))
	cached := uint64(total * clamp(g.sig.daily("cached", ts, 0.12, 0.35, 0.15), 0.02, 0.30))
	sreclaimable := uint64(total * clamp(g.sig.daily("slab", ts, 0.02, 0.3, 0.2), 0.003, 0.05))

	// The anonymous, unreclaimable part -- what MemUsedFrac has always
	// meant. Held back so the parts above plus this one never claim the
	// whole of RAM: a host at literally zero free is a different and much
	// rarer state than a busy one, and inventing it every scrape would make
	// the free gap at the top of the stack meaningless.
	const ceiling = 0.98
	parts := float64(arc + shared + buffers + cached + sreclaimable)
	anon := total * clamp(g.sig.daily("mem", ts, p.MemUsedFrac, 0.16, 0.04), 0.05, 0.97)
	if anon+parts > total*ceiling {
		anon = clamp(total*ceiling-parts, 0, total)
	}
	free := uint64(total - anon - parts)

	// mem_used and mem_available keep the meanings the real collector gives
	// them: available is free plus what the kernel can reclaim, and used is
	// total minus available. So used covers the anonymous pages, the ARC and
	// shmem -- which is exactly why it cannot be the bottom band of a stack
	// built from the other parts, and why the chart derives its used band as
	// the remainder instead.
	available := free + buffers + cached + sreclaimable
	h.MemTotal = proto.Uint64(p.MemoryTotal)
	h.MemAvailable = proto.Uint64(available)
	h.MemUsed = proto.Uint64(p.MemoryTotal - available)
	h.MemFree = proto.Uint64(free)
	h.MemBuffers = proto.Uint64(buffers)
	h.MemCached = proto.Uint64(cached)
	h.MemShared = proto.Uint64(shared)
	h.MemSreclaimable = proto.Uint64(sreclaimable)
	// The same invariant the agent maintains: buffcache is the sum of its
	// three parts, so the old single band and the new stack cannot disagree
	// about the same host.
	h.MemBuffcache = proto.Uint64(buffers + cached + shared)

	// Absent subsystems stay unset. A host with no swap reporting
	// swap_total = 0 is indistinguishable from a host whose swap collector
	// broke, which is the distinction the optional fields exist to keep.
	if p.SwapTotal > 0 {
		swapFrac := clamp(g.sig.daily("swap", ts, 0.12, 0.5, 0.3), 0, 0.95)
		h.SwapTotal = proto.Uint64(p.SwapTotal)
		h.SwapUsed = proto.Uint64(uint64(float64(p.SwapTotal) * swapFrac))
	}
	if p.ZFSArc > 0 {
		h.MemZfsArc = proto.Uint64(arc)
	}

	// Load follows CPU, averaged backwards over the window each figure
	// names -- load5 really is smoother than load1 rather than three
	// independently noisy series that happen to sit near each other.
	threads := float64(p.Threads)
	h.Load1 = proto.Float64(round2(g.loadOver(ts, time.Minute) / 100 * threads * 1.15))
	h.Load5 = proto.Float64(round2(g.loadOver(ts, 5*time.Minute) / 100 * threads * 1.15))
	h.Load15 = proto.Float64(round2(g.loadOver(ts, 15*time.Minute) / 100 * threads * 1.15))

	h.UptimeS = proto.Uint64(uint64(ts.Sub(g.boot).Seconds()))
	h.BootTimeS = proto.Uint64(uint64(g.boot.Unix()))

	h.CtxtPerS = proto.Float64(round2(g.sig.daily("ctxt", ts, 1400*threads, 0.6, 0.2) * (0.3 + cpu/60)))
	h.IntrPerS = proto.Float64(round2(g.sig.daily("intr", ts, 900*threads, 0.5, 0.2) * (0.3 + cpu/60)))
	h.ForksPerS = proto.Float64(round2(g.sig.daily("forks", ts, 3.5, 0.8, 0.5)))
	h.ProcsRunning = proto.Uint32(uint32(1 + int(cpu/100*threads)))
	h.ProcsBlocked = proto.Uint32(uint32(g.sig.unit("blocked", ts) * 3))

	// processes_total is unset -- never 0 -- on a host whose agent is in a
	// PID namespace and can see only itself.
	if p.Capabilities["processes"] != "namespaced" {
		h.ProcessesTotal = proto.Uint32(uint32(g.sig.daily("procs", ts, 40*float64(len(p.Processes))+120, 0.15, 0.08)))
	}
	if p.Capabilities["users"] != "absent" {
		h.UsersLoggedIn = proto.Uint32(uint32(g.sig.unit("users", ts) * 3))
	}
	if p.Capabilities["systemd"] != "unavailable" && len(p.Units) > 0 {
		h.ServicesTotal = proto.Uint32(uint32(len(p.Units)))
		h.ServicesFailed = proto.Uint32(g.sched.failedUnits(ts))
	}

	g.netstat(h, ts, cpu)
	h.Agent = g.agentSample(ts)
	return h
}

// loadOver averages the CPU signal backwards over d, which is what a load
// average is.
func (g *Generator) loadOver(ts time.Time, d time.Duration) float64 {
	const steps = 6
	sum := 0.0
	for i := range steps {
		sum += g.cpuTotal(ts.Add(-time.Duration(i) * d / steps))
	}
	return sum / steps
}

// netstat fills the /proc/net/snmp block. The IPv6 half is emitted only on a
// host that actually has an IPv6 address: a v4-only box has no Ip6 counters
// to read, and reporting zeros for them would claim the kernel measured
// something it never did.
func (g *Generator) netstat(h *netrav1.HostSample, ts time.Time, cpu float64) {
	busy := 0.4 + cpu/70

	h.TcpRetransSegsPerS = proto.Float64(round2(g.sig.daily("tcp.retrans", ts, 1.9, 0.9, 0.7) * busy))
	h.TcpOutRstsPerS = proto.Float64(round2(g.sig.daily("tcp.rst", ts, 3.1, 0.8, 0.6) * busy))
	h.TcpInErrsPerS = proto.Float64(round2(g.sig.daily("tcp.inerr", ts, 0.4, 1.2, 1.0) * busy))
	h.TcpActiveOpensPerS = proto.Float64(round2(g.sig.daily("tcp.active", ts, 22, 0.7, 0.3) * busy))
	h.TcpPassiveOpensPerS = proto.Float64(round2(g.sig.daily("tcp.passive", ts, 31, 0.7, 0.3) * busy))
	h.TcpAttemptFailsPerS = proto.Float64(round2(g.sig.daily("tcp.fail", ts, 0.7, 1.1, 0.9) * busy))
	h.TcpCurrEstab = proto.Uint32(uint32(g.sig.daily("tcp.estab", ts, 180, 0.6, 0.15) * busy))
	h.TcpListenOverflowsPerS = proto.Float64(round2(g.sig.unit("tcp.ovf2", ts) * 0.2))
	h.TcpListenDropsPerS = proto.Float64(round2(g.sig.unit("tcp.drops", ts) * 0.3))
	h.UdpInErrorsPerS = proto.Float64(round2(g.sig.daily("udp.inerr", ts, 0.5, 1.0, 0.8)))
	h.UdpRcvbufErrorsPerS = proto.Float64(round2(g.sig.daily("udp.rcv", ts, 0.2, 1.0, 1.0)))
	h.UdpSndbufErrorsPerS = proto.Float64(round2(g.sig.daily("udp.snd", ts, 0.1, 1.0, 1.0)))
	h.UdpNoPortsPerS = proto.Float64(round2(g.sig.daily("udp.noport", ts, 1.4, 0.9, 0.6)))
	h.IpReasmReqdsPerS = proto.Float64(round2(g.sig.daily("ip.reasm", ts, 0.9, 0.8, 0.6)))
	h.IpReasmFailsPerS = proto.Float64(round2(g.sig.daily("ip.reasmfail", ts, 0.05, 1.4, 1.4)))
	h.IpFragFailsPerS = proto.Float64(round2(g.sig.daily("ip.fragfail", ts, 0.03, 1.4, 1.4)))
	h.IpFragCreatesPerS = proto.Float64(round2(g.sig.daily("ip.fragcreate", ts, 1.1, 0.9, 0.7)))

	if !g.hasIPv6() {
		return
	}
	h.Udp6InErrorsPerS = proto.Float64(round2(g.sig.daily("udp6.inerr", ts, 0.2, 1.0, 0.9)))
	h.Udp6RcvbufErrorsPerS = proto.Float64(round2(g.sig.daily("udp6.rcv", ts, 0.08, 1.2, 1.2)))
	h.Udp6SndbufErrorsPerS = proto.Float64(round2(g.sig.daily("udp6.snd", ts, 0.04, 1.2, 1.2)))
	h.Udp6NoPortsPerS = proto.Float64(round2(g.sig.daily("udp6.noport", ts, 0.6, 0.9, 0.7)))
	h.Ip6ReasmReqdsPerS = proto.Float64(round2(g.sig.daily("ip6.reasm", ts, 0.4, 0.9, 0.7)))
	h.Ip6ReasmFailsPerS = proto.Float64(round2(g.sig.daily("ip6.reasmfail", ts, 0.02, 1.5, 1.5)))
	h.Ip6FragFailsPerS = proto.Float64(round2(g.sig.daily("ip6.fragfail", ts, 0.01, 1.5, 1.5)))
	h.Ip6FragCreatesPerS = proto.Float64(round2(g.sig.daily("ip6.fragcreate", ts, 0.5, 0.9, 0.7)))
}

// agentSample is the agent's telemetry about itself. post_latency_ms lags by
// design in the real agent -- it is the RTT of the previous successful post,
// and is left unset rather than zeroed when the last flush failed.
func (g *Generator) agentSample(ts time.Time) *netrav1.AgentSample {
	a := &netrav1.AgentSample{
		ScrapeDurationMs:   proto.Uint32(uint32(clamp(g.sig.daily("agent.scrape", ts, 42, 0.4, 0.5), 4, 900))),
		UptimeS:            proto.Uint64(uint64(ts.Sub(g.agentStart).Seconds())),
		RssBytes:           proto.Uint64(uint64(g.sig.daily("agent.rss", ts, 34*mib, 0.12, 0.05))),
		Goroutines:         proto.Uint32(uint32(clamp(g.sig.daily("agent.goro", ts, 21, 0.3, 0.2), 8, 200))),
		BufferDepth:        proto.Uint32(uint32(g.sig.unit("agent.buf", ts) * 3)),
		BufferDroppedTotal: proto.Uint64(0),
		PostFailuresTotal:  proto.Uint64(uint64(ts.Sub(g.from).Hours() / 37)),
	}
	if g.sig.unit("agent.postok", ts) > 0.02 {
		a.PostLatencyMs = proto.Uint32(uint32(clamp(g.sig.daily("agent.latency", ts, 38, 0.5, 0.6), 3, 5000)))
	}
	return a
}

func (g *Generator) cores(ts time.Time, cpu float64) []*netrav1.CpuCoreSample {
	out := make([]*netrav1.CpuCoreSample, 0, g.p.Threads)
	for i := range g.p.Threads {
		key := fmt.Sprintf("core/%d", i)
		// Cores are not uniformly loaded: one or two carry the interrupt
		// work and the rest idle, which is what makes a per-core heatmap
		// worth looking at.
		// Mean 1, not 1.25. The bias used to run 0.55..1.95, so the average
		// core was busier than the host's own cpu_total -- harmless while
		// nothing added the cores up, but the per-core stack's top edge IS
		// the mean, and it would have sat a quarter above the number the
		// meter and the host page show for the same instant. The spread is
		// unchanged: one or two cores still carry the interrupt work.
		bias := 0.4 + 1.2*g.sig.unit(key+"/bias", ts.Truncate(time.Hour))
		busy := clamp(cpu*bias+g.sig.jitter(key, ts)*6, 0, 100)
		out = append(out, &netrav1.CpuCoreSample{
			TsMs: ts.UnixMilli(),
			Core: uint32(i),
			Busy: proto.Float64(round2(busy)),
		})
	}
	return out
}

func (g *Generator) diskIO(ts time.Time, cpu float64) []*netrav1.DiskIoSample {
	busy := 0.35 + cpu/60
	out := make([]*netrav1.DiskIoSample, 0, len(g.p.Disks))
	for _, d := range g.p.Disks {
		key := "disk/" + d.Device
		read := g.sig.daily(key+"/r", ts, d.ReadBase, 0.8, 0.35) * busy
		write := g.sig.daily(key+"/w", ts, d.WriteBase, 0.7, 0.3) * busy
		write = g.sig.spike(key+"/w", ts, write, 0.006, 4.5)

		blk := 64.0 * 1024
		if !d.SolidState {
			blk = 128 * 1024
		}
		util := clamp((read+write)/(d.ReadBase+d.WriteBase+1)*22*busy, 0, 100)

		out = append(out, &netrav1.DiskIoSample{
			TsMs:          ts.UnixMilli(),
			Device:        d.Device,
			ReadBytes:     proto.Float64(round2(read)),
			WriteBytes:    proto.Float64(round2(write)),
			ReadOps:       proto.Float64(round2(read / blk)),
			WriteOps:      proto.Float64(round2(write / blk)),
			IoUtilPct:     proto.Float64(round2(util)),
			RAwaitMs:      proto.Float64(round2(d.AwaitBase * (0.6 + util/60))),
			WAwaitMs:      proto.Float64(round2(d.AwaitBase * (0.8 + util/45))),
			WeightedIoPct: proto.Float64(round2(clamp(util*1.3, 0, 100))),
		})
	}
	return out
}

func (g *Generator) sensors(ts time.Time, cpu float64) []*netrav1.SensorSample {
	out := make([]*netrav1.SensorSample, 0, len(g.p.Sensors))
	for _, s := range g.p.Sensors {
		key := "sensor/" + s.Chip + "/" + s.Label
		temp := s.Base + s.Swing*(cpu/100) + g.sig.jitter(key, ts)*1.4
		out = append(out, &netrav1.SensorSample{
			TsMs:  ts.UnixMilli(),
			Chip:  s.Chip,
			Label: s.Label,
			Temp:  proto.Float64(round2(temp)),
		})
	}
	return out
}

func (g *Generator) nets(ts time.Time) []*netrav1.NetSample {
	out := make([]*netrav1.NetSample, 0, len(g.p.Nets))
	for _, n := range g.p.Nets {
		key := "net/" + n.Iface
		rx := g.sig.spike(key+"/rx", ts, g.sig.daily(key+"/rx", ts, n.RxBase, 0.85, 0.3), 0.005, 3.2)
		tx := g.sig.spike(key+"/tx", ts, g.sig.daily(key+"/tx", ts, n.TxBase, 0.85, 0.3), 0.005, 3.0)
		out = append(out, &netrav1.NetSample{
			TsMs:    ts.UnixMilli(),
			Iface:   n.Iface,
			RxBytes: proto.Float64(round2(rx)),
			TxBytes: proto.Float64(round2(tx)),
			RxErrs:  proto.Float64(round2(g.sig.unit(key+"/rxe", ts) * 0.4)),
			TxErrs:  proto.Float64(round2(g.sig.unit(key+"/txe", ts) * 0.2)),
		})
	}
	return out
}

func (g *Generator) containers(ts time.Time, cpu float64) []*netrav1.ContainerSample {
	busy := 0.4 + cpu/70
	out := make([]*netrav1.ContainerSample, 0, len(g.p.Containers))
	for _, c := range g.p.Containers {
		key := "ctr/" + c.Key
		mem := uint64(float64(c.MemBase) * clamp(g.sig.daily(key+"/mem", ts, 1, 0.14, 0.05), 0.4, 1.6))
		if c.MemLimit > 0 && mem > c.MemLimit {
			mem = c.MemLimit
		}
		cpu := round2(clamp(g.sig.daily(key+"/cpu", ts, c.CPUBase, 0.7, 0.3)*busy, 0, 100*float64(g.p.Threads)))
		// user and system are a SPLIT of cpu_pct, not two more signals: a
		// chart stacks them against it, so they have to sum to it. The share
		// varies per container and over the day -- a proxy lives in system
		// time, a compiler in user -- but it is always a share.
		systemShare := clamp(g.sig.daily(key+"/sys", ts, 0.3, 0.5, 0.2), 0.05, 0.8)
		system := round2(cpu * systemShare)

		// memory.stat's parts, carved out of the same mem_used the container
		// already reports rather than generated beside it -- anon plus the
		// caches has to BE the number, or the breakdown and the total would
		// describe different containers.
		shmem := uint64(float64(mem) * 0.05)
		kernel := uint64(float64(mem) * 0.04)
		file := uint64(float64(mem) * clamp(g.sig.daily(key+"/file", ts, 0.22, 0.4, 0.2), 0.05, 0.5))
		anon := mem - shmem - kernel - file

		out = append(out, &netrav1.ContainerSample{
			TsMs:         ts.UnixMilli(),
			ContainerKey: c.Key,
			Name:         c.Name,
			Image:        c.Image,
			IsAgent:      c.IsAgent,
			CpuPct:       proto.Float64(cpu),
			CpuUser:      proto.Float64(round2(cpu - system)),
			CpuSystem:    proto.Float64(system),
			MemUsed:      proto.Uint64(mem),
			MemAnon:      proto.Uint64(anon),
			MemFile:      proto.Uint64(file),
			MemShmem:     proto.Uint64(shmem),
			MemKernel:    proto.Uint64(kernel),
			MemLimit:     proto.Uint64(c.MemLimit),
			NetRx:        proto.Float64(round2(g.sig.daily(key+"/rx", ts, 90*1024, 1.0, 0.5) * busy)),
			NetTx:        proto.Float64(round2(g.sig.daily(key+"/tx", ts, 64*1024, 1.0, 0.5) * busy)),
			IoRead:       proto.Float64(round2(g.sig.daily(key+"/ior", ts, 40*1024, 1.1, 0.6) * busy)),
			IoWrite:      proto.Float64(round2(g.sig.daily(key+"/iow", ts, 70*1024, 1.1, 0.6) * busy)),
		})
	}
	return out
}

func (g *Generator) filesystems(ts time.Time) []*netrav1.FilesystemSample {
	out := make([]*netrav1.FilesystemSample, 0, len(g.p.Filesystems))
	for _, f := range g.p.Filesystems {
		key := "fs/" + f.Label
		frac := clamp(ramp(g.from, g.to, ts, f.UsedStart, f.UsedEnd)+g.sig.jitter(key, ts)*0.004, 0, 0.999)
		used := uint64(float64(f.Total) * frac)

		fs := &netrav1.FilesystemSample{
			TsMs:       ts.UnixMilli(),
			Label:      f.Label,
			Mountpoint: f.Mountpoint,
			DeviceId:   proto.Uint64(f.DeviceID),
			Total:      proto.Uint64(f.Total),
			Used:       proto.Uint64(used),
			Free:       proto.Uint64(f.Total - used),
			// The simulator fills read_bytes/write_bytes and the real agent
			// does not: filesystems.go sets device_id but never maps it to a
			// block device, so these two columns are permanently NULL in
			// production. They are filled here so the read API and the UI
			// have something to develop against. This is a DELIBERATE
			// divergence -- do not "fix" the agent to match it.
			ReadBytes:  proto.Float64(round2(g.sig.daily(key+"/r", ts, 220*1024, 1.0, 0.5))),
			WriteBytes: proto.Float64(round2(g.sig.daily(key+"/w", ts, 380*1024, 1.0, 0.5))),
		}
		// A ZFS dataset has no fixed inode count, so those two columns stay
		// NULL there rather than reporting a made-up total.
		if f.InodesTotal > 0 {
			fs.InodesTotal = proto.Uint64(f.InodesTotal)
			fs.InodesUsed = proto.Uint64(uint64(float64(f.InodesTotal) * frac * 0.7))
		}
		out = append(out, fs)
	}
	return out
}

// smartFailureOnset is how far into the window the failing drive starts
// reallocating sectors. Late enough that the history has a clean stretch to
// compare against.
const smartFailureOnset = 0.55

func (g *Generator) smart(ts time.Time) []*netrav1.SmartAttribute {
	var out []*netrav1.SmartAttribute
	hoursIn := int64(ts.Sub(g.from).Hours())
	progress := clamp(ts.Sub(g.from).Seconds()/math.Max(g.to.Sub(g.from).Seconds(), 1), 0, 1)

	for _, d := range g.p.Drives {
		key := "smart/" + d.Device
		add := func(id uint32, raw int64, normalized uint32) {
			out = append(out, &netrav1.SmartAttribute{
				TsMs:       ts.UnixMilli(),
				Device:     d.Device,
				Model:      d.Model,
				Serial:     d.Serial,
				AttrId:     id,
				Raw:        proto.Int64(raw),
				Normalized: proto.Uint32(normalized),
			})
		}

		temp := 34 + 8*g.sig.unit(key+"/temp", ts.Truncate(time.Hour))
		if d.Failing {
			// A drive that is reallocating sectors runs warm, so the
			// temperature series corroborates the sector count instead of
			// being the one place nothing changed.
			temp += 9 * clamp((progress-smartFailureOnset)/(1-smartFailureOnset), 0, 1)
		}

		add(9, d.PowerOnHours+hoursIn, 98)
		add(194, int64(temp), uint32(120-int(temp)))
		add(1, int64(g.sig.unit(key+"/rre", ts.Truncate(time.Hour))*40), 100)

		reallocated := int64(0)
		pending := int64(0)
		normalized := uint32(100)
		if d.Failing && progress > smartFailureOnset {
			f := (progress - smartFailureOnset) / (1 - smartFailureOnset)
			reallocated = int64(f * 248)
			pending = int64(f * 16)
			normalized = uint32(100 - clamp(f*32, 0, 32))
		}
		add(5, reallocated, normalized)
		add(197, pending, normalized)

		if d.SSD {
			// Wear levelling: normalized counts DOWN from 100 as the drive
			// is written, which is the opposite direction to every other
			// attribute here and the usual source of an inverted chart.
			add(233, int64(float64(d.PowerOnHours+hoursIn)*3.4), uint32(100-int(progress*3)))
			add(241, int64(float64(d.PowerOnHours+hoursIn)*1.9e6), 100)
		}
	}
	return out
}

func (g *Generator) processes(ts time.Time, cpu float64) []*netrav1.ProcessSample {
	busy := 0.4 + cpu/70
	out := make([]*netrav1.ProcessSample, 0, len(g.p.Processes))
	for _, pr := range g.p.Processes {
		key := "proc/" + pr.Name
		out = append(out, &netrav1.ProcessSample{
			TsMs:     ts.UnixMilli(),
			Name:     comm(pr.Name),
			CpuPct:   proto.Float64(round2(clamp(g.sig.daily(key+"/cpu", ts, pr.CPUBase, 0.7, 0.35)*busy, 0, 100*float64(g.p.Threads)))),
			MemBytes: proto.Uint64(uint64(float64(pr.MemBase) * clamp(g.sig.daily(key+"/mem", ts, 1, 0.12, 0.05), 0.4, 1.8))),
			Count:    proto.Uint32(pr.Count),
		})
	}
	return out
}

// collectorHealth reports each collector's own duration and success.
//
// The real agent sends these too: its scrape loop times each collector from
// the outside and fills IngestRequest.collectors, because a collector cannot
// time itself. The shape here mirrors that -- a duration per collector, ok,
// and a short error token that clears on recovery rather than latching.
func (g *Generator) collectorHealth(ts time.Time) []*netrav1.CollectorSample {
	out := make([]*netrav1.CollectorSample, 0, len(g.p.Collectors))
	for _, name := range g.p.Collectors {
		key := "coll/" + name
		base := 3.0
		switch name {
		case "smart":
			base = 820
		case "packages":
			base = 140
		case "containers", "systemd":
			base = 45
		case "processes":
			base = 62
		}
		c := &netrav1.CollectorSample{
			TsMs:       ts.UnixMilli(),
			Collector:  name,
			DurationMs: proto.Uint32(uint32(clamp(g.sig.daily(key, ts, base, 0.4, 0.5), 1, 60000))),
			Ok:         true,
		}
		// A collector that fails must clear its error on recovery rather
		// than reading as broken forever, so the failure is sampled per
		// scrape rather than latched.
		if g.sig.unit(key+"/fail", ts) < 0.0015 {
			c.Ok = false
			c.ErrorCode = proto.String("timeout")
		}
		out = append(out, c)
	}
	return out
}

func (g *Generator) addresses() []*netrav1.HostAddress {
	out := make([]*netrav1.HostAddress, 0, len(g.p.Addresses))
	for _, a := range g.p.Addresses {
		out = append(out, &netrav1.HostAddress{
			Iface:   a.Iface,
			IfIndex: proto.Uint32(a.IfIndex),
			Address: a.Address,
			Family:  a.Family,
			// scope is deliberately not sent: the hub derives it from the
			// address so the classification is one implementation that can be
			// corrected without redeploying every agent.
			Description: a.Description,
		})
	}
	return out
}

// packages renders the inventory AS OF ts: the profile's static set, with the
// versions the upgrade events have moved on, plus everything installed since
// the window opened.
//
// Replaying the events rather than returning the static list is what keeps
// host_packages and package_events telling the same story. A static list
// leaves the inventory reporting the original version of every package the
// event log says was upgraded, and missing every package it says was
// installed.
func (g *Generator) packages(ts time.Time) []*netrav1.HostPackage {
	versions, installed := g.sched.packageStateAt(ts)

	render := func(p PackageSpec) *netrav1.HostPackage {
		version := p.Version
		if v, ok := versions[p.Name]; ok {
			version = v
		}
		arch := p.Arch
		if arch == "" {
			arch = g.pkgArch()
		}
		return &netrav1.HostPackage{
			Name:      p.Name,
			Version:   version,
			Arch:      arch,
			Format:    g.p.PkgFormat,
			SizeBytes: proto.Uint64(p.Size),
		}
	}

	out := make([]*netrav1.HostPackage, 0, len(g.p.Packages)+len(installed))
	for _, p := range g.p.Packages {
		out = append(out, render(p))
	}
	for _, p := range installed {
		out = append(out, render(p))
	}
	return out
}

// pkgArch is the architecture an installed package inherits, taken from the
// inventory rather than from Profile.Arch: the package manager's name for it
// ("amd64", "x86_64") is not always Go's.
func (g *Generator) pkgArch() string {
	if len(g.p.Packages) > 0 {
		return g.p.Packages[0].Arch
	}
	return g.p.Arch
}

func (g *Generator) hasSpinningDisk() bool {
	for _, d := range g.p.Disks {
		if !d.SolidState {
			return true
		}
	}
	return false
}

func (g *Generator) hasIPv6() bool {
	for _, a := range g.p.Addresses {
		if a.Family == 6 {
			return true
		}
	}
	return false
}

// comm truncates a name the way the kernel does. The real collector reads
// /proc/PID/comm, which is capped at 15 bytes, and a simulator that emitted
// longer names would make the UI look like it can show something it cannot.
func comm(name string) string {
	if len(name) <= 15 {
		return name
	}
	return name[:15]
}

// round2 keeps the generated values readable in psql. The columns are double
// precision and would happily store the full mantissa, but a table of
// 17-digit fake numbers is unreadable and reads as precision that is not
// there.
func round2(v float64) float64 {
	return math.Round(v*100) / 100
}

func hashString(s string) uint64 {
	var h uint64 = 14695981039346656037
	for i := range len(s) {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	return h
}
