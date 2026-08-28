package sim

import (
	"fmt"
	"math"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
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
	SystemdEvents []*netrav1.SystemdUnitEvent
	PackageEvents []*netrav1.PackageEvent
	Addresses     []*netrav1.HostAddress
	Interfaces    []*netrav1.HostInterface
	Packages      []*netrav1.HostPackage

	// SystemdSnapshot mirrors what a real agent sends every snapshotFloor.
	// Without it a simulated fleet reproduces the bug the snapshot exists to
	// fix -- units pinned at "failed" with nothing able to clear them -- and
	// the local hub could not be used to check that it is gone.
	SystemdSnapshot *netrav1.SystemdSnapshot
}

// Rows counts what this scrape will insert. The batcher bounds a POST by rows
// rather than by scrapes because a 32-core host is 100 rows per scrape and a
// 1-vCPU host is a dozen -- counting scrapes would make the batch size depend
// on which host is being simulated.
func (s *Scrape) Rows() int {
	n := len(s.Cores) + len(s.Disks) + len(s.Sensors) + len(s.Nets) +
		len(s.Collectors) + len(s.Events) + len(s.Containers) +
		len(s.Filesystems) + len(s.Smart) +
		len(s.SystemdEvents) + len(s.PackageEvents) +
		len(s.Addresses) + len(s.Interfaces) + len(s.Packages) +
		len(s.SystemdSnapshot.GetUnits())
	if s.Host != nil {
		n++
	}
	return n
}

// Options selects which families a scrape carries. They are not all emitted
// at every instant: SMART changes over hours and reading it spins up sleeping
// drives, and inventory describes what the host HAS rather than what it
// measured.
type Options struct {
	Smart      bool
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

	// hubFailures is cumulative across the window, like the counter it
	// stands for: the agent reports failures since ITS start, never a
	// per-scrape count. Held on the generator rather than derived from ts
	// because samples are built in order and a total that only ever climbs
	// is the property the UI's counter handling is written against -- a
	// value recomputed per instant could go backwards and read as a reset.
	hubFailures uint64

	// oomKills is cumulative for the same reason hubFailures is: the kernel
	// counter counts since boot and never decreases.
	oomKills uint64
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
	if opt.Inventory {
		s.Addresses = g.addresses()
		s.Interfaces = g.interfaces()
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

	s.SystemdSnapshot = g.systemdSnapshot(ts)
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
	//
	// Sized from the thread count rather than from a per-process list, which
	// no profile carries any more: a busier host runs more processes, and the
	// count is the only process figure netra reports.
	if p.Capabilities["processes"] != "namespaced" {
		h.ProcessesTotal = proto.Uint32(uint32(g.sig.daily("procs", ts, 40*threads+120, 0.15, 0.08)))
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

	// The TCP and UDP volume counters, which land in host_proto_samples.
	//
	// Orders of magnitude above the error counters beside them on purpose:
	// that gap IS the reading. A retransmit rate of 3/s is unremarkable
	// against 9000 segments/s and alarming against 30, and the panels exist
	// to let a reader see which one they are looking at.
	h.TcpInSegsPerS = proto.Float64(round2(g.sig.daily("tcp.inseg", ts, 4200, 0.7, 0.3) * busy))
	h.TcpOutSegsPerS = proto.Float64(round2(g.sig.daily("tcp.outseg", ts, 4800, 0.7, 0.3) * busy))
	h.TcpEstabResetsPerS = proto.Float64(round2(g.sig.daily("tcp.estabreset", ts, 0.6, 1.1, 0.9) * busy))
	h.UdpInDatagramsPerS = proto.Float64(round2(g.sig.daily("udp.indgram", ts, 640, 0.8, 0.4) * busy))
	h.UdpOutDatagramsPerS = proto.Float64(round2(g.sig.daily("udp.outdgram", ts, 610, 0.8, 0.4) * busy))
	h.Udp6InDatagramsPerS = proto.Float64(round2(g.sig.daily("udp6.indgram", ts, 41, 0.9, 0.5) * busy))
	h.Udp6OutDatagramsPerS = proto.Float64(round2(g.sig.daily("udp6.outdgram", ts, 38, 0.9, 0.5) * busy))

	// The rest of Ip:, and Icmp:. Volume scales with load; the error
	// counters stay low and bursty, because a host discarding datagrams
	// steadily is not the interesting default and a flat line reads as a
	// broken panel rather than a quiet one.
	h.IpInReceivesPerS = proto.Float64(round2(g.sig.daily("ip.rx", ts, 820, 0.7, 0.3) * busy))
	h.IpInDeliversPerS = proto.Float64(round2(g.sig.daily("ip.deliver", ts, 780, 0.7, 0.3) * busy))
	h.IpOutRequestsPerS = proto.Float64(round2(g.sig.daily("ip.tx", ts, 690, 0.7, 0.3) * busy))
	h.IpForwDatagramsPerS = proto.Float64(round2(g.sig.daily("ip.forward", ts, 0.6, 1.1, 0.9)))
	h.IpReasmOksPerS = proto.Float64(round2(g.sig.daily("ip.reasmok", ts, 0.8, 0.8, 0.6)))
	h.IpFragOksPerS = proto.Float64(round2(g.sig.daily("ip.fragok", ts, 1.0, 0.9, 0.7)))
	h.IpInHdrErrorsPerS = proto.Float64(round2(g.sig.daily("ip.hdrerr", ts, 0.02, 1.5, 1.5)))
	h.IpInAddrErrorsPerS = proto.Float64(round2(g.sig.daily("ip.addrerr", ts, 0.04, 1.4, 1.4)))
	h.IpInUnknownProtosPerS = proto.Float64(round2(g.sig.daily("ip.unkproto", ts, 0.01, 1.6, 1.6)))
	h.IpInDiscardsPerS = proto.Float64(round2(g.sig.daily("ip.indisc", ts, 0.03, 1.4, 1.4)))
	h.IpOutDiscardsPerS = proto.Float64(round2(g.sig.daily("ip.outdisc", ts, 0.05, 1.4, 1.4)))
	h.IpOutNoRoutesPerS = proto.Float64(round2(g.sig.daily("ip.noroute", ts, 0.02, 1.5, 1.5)))
	h.IpReasmTimeoutPerS = proto.Float64(round2(g.sig.daily("ip.reasmto", ts, 0.02, 1.5, 1.5)))

	h.IcmpInMsgsPerS = proto.Float64(round2(g.sig.daily("icmp.in", ts, 2.4, 0.8, 0.5)))
	h.IcmpOutMsgsPerS = proto.Float64(round2(g.sig.daily("icmp.out", ts, 2.1, 0.8, 0.5)))
	h.IcmpInErrorsPerS = proto.Float64(round2(g.sig.daily("icmp.inerr", ts, 0.02, 1.5, 1.5)))
	h.IcmpOutErrorsPerS = proto.Float64(round2(g.sig.daily("icmp.outerr", ts, 0.02, 1.5, 1.5)))
	h.IcmpInDestUnreachsPerS = proto.Float64(round2(g.sig.daily("icmp.indu", ts, 0.3, 1.2, 1.1)))
	h.IcmpOutDestUnreachsPerS = proto.Float64(round2(g.sig.daily("icmp.outdu", ts, 0.25, 1.2, 1.1)))
	h.IcmpInTimeExcdsPerS = proto.Float64(round2(g.sig.daily("icmp.inte", ts, 0.08, 1.3, 1.2)))
	h.IcmpOutTimeExcdsPerS = proto.Float64(round2(g.sig.daily("icmp.outte", ts, 0.06, 1.3, 1.2)))
	h.IcmpInParmProbsPerS = proto.Float64(round2(g.sig.daily("icmp.inpp", ts, 0.01, 1.6, 1.6)))
	h.IcmpOutParmProbsPerS = proto.Float64(round2(g.sig.daily("icmp.outpp", ts, 0.01, 1.6, 1.6)))
	h.IcmpInRedirectsPerS = proto.Float64(round2(g.sig.daily("icmp.inrd", ts, 0.04, 1.4, 1.3)))
	h.IcmpOutRedirectsPerS = proto.Float64(round2(g.sig.daily("icmp.outrd", ts, 0.03, 1.4, 1.3)))

	// Echo. Steady and non-zero on every host worth monitoring -- something
	// is always pinging it -- which is what makes the informational panel look
	// alive rather than broken.
	h.IcmpInEchosPerS = proto.Float64(round2(g.sig.daily("icmp.inecho", ts, 1.1, 0.6, 0.3)))
	h.IcmpOutEchosPerS = proto.Float64(round2(g.sig.daily("icmp.outecho", ts, 0.4, 0.7, 0.4)))
	h.IcmpInEchoRepsPerS = proto.Float64(round2(g.sig.daily("icmp.inechorep", ts, 0.4, 0.7, 0.4)))
	h.IcmpOutEchoRepsPerS = proto.Float64(round2(g.sig.daily("icmp.outechorep", ts, 1.1, 0.6, 0.3)))

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

	// The Ip6* and Icmp6* mirrors of the block above.
	h.Ip6InReceivesPerS = proto.Float64(round2(g.sig.daily("ip6.rx", ts, 260, 0.7, 0.3) * busy))
	h.Ip6InDeliversPerS = proto.Float64(round2(g.sig.daily("ip6.deliver", ts, 245, 0.7, 0.3) * busy))
	h.Ip6OutRequestsPerS = proto.Float64(round2(g.sig.daily("ip6.tx", ts, 210, 0.7, 0.3) * busy))
	h.Ip6OutForwDatagramsPerS = proto.Float64(round2(g.sig.daily("ip6.forward", ts, 0.2, 1.1, 0.9)))
	h.Ip6ReasmOksPerS = proto.Float64(round2(g.sig.daily("ip6.reasmok", ts, 0.35, 0.9, 0.7)))
	h.Ip6FragOksPerS = proto.Float64(round2(g.sig.daily("ip6.fragok", ts, 0.45, 0.9, 0.7)))
	h.Ip6InHdrErrorsPerS = proto.Float64(round2(g.sig.daily("ip6.hdrerr", ts, 0.01, 1.5, 1.5)))
	h.Ip6InAddrErrorsPerS = proto.Float64(round2(g.sig.daily("ip6.addrerr", ts, 0.02, 1.4, 1.4)))
	h.Ip6InUnknownProtosPerS = proto.Float64(round2(g.sig.daily("ip6.unkproto", ts, 0.005, 1.6, 1.6)))
	h.Ip6InDiscardsPerS = proto.Float64(round2(g.sig.daily("ip6.indisc", ts, 0.015, 1.4, 1.4)))
	h.Ip6OutDiscardsPerS = proto.Float64(round2(g.sig.daily("ip6.outdisc", ts, 0.02, 1.4, 1.4)))
	h.Ip6OutNoRoutesPerS = proto.Float64(round2(g.sig.daily("ip6.outnoroute", ts, 0.01, 1.5, 1.5)))
	h.Ip6InNoRoutesPerS = proto.Float64(round2(g.sig.daily("ip6.innoroute", ts, 0.01, 1.5, 1.5)))
	h.Ip6InTooBigErrorsPerS = proto.Float64(round2(g.sig.daily("ip6.toobig", ts, 0.02, 1.5, 1.5)))
	h.Ip6ReasmTimeoutPerS = proto.Float64(round2(g.sig.daily("ip6.reasmto", ts, 0.01, 1.5, 1.5)))

	h.Icmp6InMsgsPerS = proto.Float64(round2(g.sig.daily("icmp6.in", ts, 1.6, 0.8, 0.5)))
	h.Icmp6OutMsgsPerS = proto.Float64(round2(g.sig.daily("icmp6.out", ts, 1.5, 0.8, 0.5)))
	h.Icmp6InErrorsPerS = proto.Float64(round2(g.sig.daily("icmp6.inerr", ts, 0.01, 1.5, 1.5)))
	h.Icmp6OutErrorsPerS = proto.Float64(round2(g.sig.daily("icmp6.outerr", ts, 0.01, 1.5, 1.5)))
	h.Icmp6InDestUnreachsPerS = proto.Float64(round2(g.sig.daily("icmp6.indu", ts, 0.15, 1.2, 1.1)))
	h.Icmp6OutDestUnreachsPerS = proto.Float64(round2(g.sig.daily("icmp6.outdu", ts, 0.12, 1.2, 1.1)))
	h.Icmp6InTimeExcdsPerS = proto.Float64(round2(g.sig.daily("icmp6.inte", ts, 0.04, 1.3, 1.2)))
	h.Icmp6OutTimeExcdsPerS = proto.Float64(round2(g.sig.daily("icmp6.outte", ts, 0.03, 1.3, 1.2)))
	h.Icmp6InParmProblemsPerS = proto.Float64(round2(g.sig.daily("icmp6.inpp", ts, 0.005, 1.6, 1.6)))
	h.Icmp6OutParmProblemsPerS = proto.Float64(round2(g.sig.daily("icmp6.outpp", ts, 0.005, 1.6, 1.6)))
	h.Icmp6InPktTooBigsPerS = proto.Float64(round2(g.sig.daily("icmp6.inptb", ts, 0.02, 1.4, 1.3)))
	h.Icmp6OutPktTooBigsPerS = proto.Float64(round2(g.sig.daily("icmp6.outptb", ts, 0.02, 1.4, 1.3)))
	h.Icmp6InRedirectsPerS = proto.Float64(round2(g.sig.daily("icmp6.inrd", ts, 0.01, 1.4, 1.3)))
	h.Icmp6OutRedirectsPerS = proto.Float64(round2(g.sig.daily("icmp6.outrd", ts, 0.01, 1.4, 1.3)))
	h.Icmp6InEchosPerS = proto.Float64(round2(g.sig.daily("icmp6.inecho", ts, 0.5, 0.6, 0.3)))
	h.Icmp6OutEchosPerS = proto.Float64(round2(g.sig.daily("icmp6.outecho", ts, 0.2, 0.7, 0.4)))
	h.Icmp6InEchoRepliesPerS = proto.Float64(round2(g.sig.daily("icmp6.inechorep", ts, 0.2, 0.7, 0.4)))
	h.Icmp6OutEchoRepliesPerS = proto.Float64(round2(g.sig.daily("icmp6.outechorep", ts, 0.5, 0.6, 0.3)))

	// Neighbour discovery: IPv6 has no ARP, so this is the background
	// chatter that keeps a segment resolvable. Steady and low -- the
	// "quiet but non-zero" case the informational panel needs in order
	// to be distinguishable from a dead one.
	h.Icmp6InNeighborSolicitsPerS = proto.Float64(round2(g.sig.daily("icmp6.inns", ts, 0.32, 0.5, 0.25)))
	h.Icmp6OutNeighborSolicitsPerS = proto.Float64(round2(g.sig.daily("icmp6.outns", ts, 0.28, 0.5, 0.25)))
	h.Icmp6InNeighborAdvertisementsPerS = proto.Float64(round2(g.sig.daily("icmp6.inna", ts, 0.26, 0.5, 0.25)))
	h.Icmp6OutNeighborAdvertisementsPerS = proto.Float64(round2(g.sig.daily("icmp6.outna", ts, 0.3, 0.5, 0.25)))
	h.Icmp6InRouterSolicitsPerS = proto.Float64(round2(g.sig.daily("icmp6.inrs", ts, 0.03, 0.6, 0.3)))
	h.Icmp6OutRouterSolicitsPerS = proto.Float64(round2(g.sig.daily("icmp6.outrs", ts, 0.02, 0.6, 0.3)))
	h.Icmp6InRouterAdvertisementsPerS = proto.Float64(round2(g.sig.daily("icmp6.inra", ts, 0.06, 0.5, 0.2)))
	h.Icmp6OutRouterAdvertisementsPerS = proto.Float64(round2(g.sig.daily("icmp6.outra", ts, 0.01, 0.6, 0.3)))

	// Memory pressure. Kept low and bursty: a host that majors-faults
	// constantly is not the interesting default, and a flat line would make
	// the panel look broken rather than quiet.
	h.PgmajfaultPerS = proto.Float64(round2(g.sig.daily("vm.majfault", ts, 1.2, 1.4, 1.6)))
	h.PswpinPerS = proto.Float64(round2(g.sig.daily("vm.swpin", ts, 0.05, 1.6, 1.8)))
	h.PswpoutPerS = proto.Float64(round2(g.sig.daily("vm.swpout", ts, 0.03, 1.6, 1.8)))
	// Monotonic and almost always flat: an OOM kill is an event, not a
	// level. It does happen, though, and a counter pinned at zero forever
	// meant the one thing that reads it -- the attention badge -- could
	// never be seen working against the simulator.
	//
	// Cumulative since boot, so it only ever climbs, and rare enough that
	// most windows contain no kill at all. That is the state worth
	// defaulting to: the badge must stay silent on a healthy host, and a
	// simulator that fires it constantly would prove nothing about whether
	// it is silent when it should be.
	//
	// The threshold is a probability PER SCRAPE, and the scrape count is
	// what makes it rare -- not the wall clock. Inside the raw retention
	// window the backfill writes a sample a minute, so 1 in 200 (0.995) is
	// seven kills in a 24h window on every host in the fleet, which is a
	// permanent critical badge rather than a rare event. 1 in ~14000 is
	// about 0.1 expected kills in 24h and a couple across a 90-day
	// backfill: usually silent, occasionally there to be looked at.
	if g.sig.unit("vm.oom", ts) > 0.99993 {
		g.oomKills++
	}
	h.OomKillTotal = proto.Uint64(g.oomKills)

	// Exhaustion gauges, each well below its ceiling so the ratio the panel
	// exists to show is legible rather than alarming.
	//
	// Gated on the capability that covers each source, because in the real
	// agent the capability and the sample are two halves of one statement:
	// Limits.setCapability DELETES the key when the read succeeded and
	// writes "unavailable" only when it failed -- in which case the fields
	// that read would have filled are left unset. A simulated host that
	// declares a source unavailable and then reports its numbers anyway
	// puts the Limits card in a state no real host can be in: the agent's
	// "unavailable" standing in for a meter whose data is in the database.
	if g.reports(capSockets) {
		h.SocketsUsed = proto.Uint32(uint32(clamp(g.sig.daily("lim.sockets", ts, 240, 0.35, 0.4), 20, 60000)))
		h.TcpOrphan = proto.Uint32(uint32(clamp(g.sig.daily("lim.orphan", ts, 2, 1.2, 1.2), 0, 1000)))
		h.TcpTw = proto.Uint32(uint32(clamp(g.sig.daily("lim.tw", ts, 900, 0.6, 0.5), 0, 60000)))
		h.TcpAlloc = proto.Uint32(uint32(clamp(g.sig.daily("lim.alloc", ts, 160, 0.4, 0.4), 10, 60000)))
	}
	if g.reports(capFileDescriptors) {
		h.FdUsed = proto.Uint64(uint64(clamp(g.sig.daily("lim.fd", ts, 2200, 0.3, 0.3), 200, 500000)))
		// The ceiling is a sysctl: constant unless someone changes it. Read
		// from the same file as the gauge (/proc/sys/fs/file-nr's third
		// field), so it is absent exactly when the gauge is.
		h.FdLimit = proto.Uint64(g.fileMax())
	}
	if g.reports(capConntrack) {
		h.ConntrackCount = proto.Uint32(uint32(clamp(g.sig.daily("lim.ct", ts, 1800, 0.5, 0.5), 0, 200000)))
		h.ConntrackLimit = proto.Uint32(262144)
	}

	// The TCP ceilings are sysctls under /proc/sys/net/ipv4, which the agent
	// reads whether or not /proc/net/sockstat could be opened -- so they are
	// not gated on the sockets capability.
	h.TcpTwLimit = proto.Uint32(131072)
	h.TcpOrphanLimit = proto.Uint32(65536)
}

// Capability keys the limits collector reports, mirroring the constants in
// internal/agent/collector/limits.go.
const (
	capSockets         = "sockets"
	capFileDescriptors = "file_descriptors"
	capConntrack       = "conntrack"
)

// reports is whether this profile's agent could read a source at all.
//
// The agent's convention, not an invented one: a key present in the
// capability map is a collector saying it could NOT do something, and a
// working collector reports no key.
func (g *Generator) reports(capability string) bool {
	_, degraded := g.p.Capabilities[capability]
	return !degraded
}

// fileMax is the profile's /proc/sys/fs/file-max, defaulting to int64 max --
// the "no practical limit" a great many hosts are left at, and the value the
// UI answers with "no limit" instead of a bar against 9.2 quintillion.
func (g *Generator) fileMax() uint64 {
	if g.p.FileMax == 0 {
		return math.MaxInt64
	}
	return g.p.FileMax
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

	// The TCP path to the hub, and the failures on it.
	//
	// Both gauges are the duration of a handshake that COMPLETED, so when
	// the hub is unreachable there is nothing to time and they are left
	// unset -- NULL, exactly as the real agent leaves them. The failure
	// counter is what carries the event, and it is cumulative: it keeps
	// climbing and then holds its new level, which is why the UI draws its
	// per-bucket increase rather than the total.
	//
	// A sprinkle of independent failures, NOT a contiguous outage: unit()
	// is a hash of seed+timestamp with no correlation between neighbouring
	// instants, so this fires on isolated scrapes at about 3% of them. That
	// exercises the pair -- a raw-tier bucket with a null latency beside a
	// counter that stepped -- but at the 5m and 1h tiers every bucket still
	// contains completed handshakes, so latency does not go blank there.
	// Simulating a real outage means a run of consecutive failing scrapes,
	// which needs a temporally correlated signal this package does not have
	// yet.
	if g.sig.unit("agent.hubdown", ts) > 0.97 {
		g.hubFailures++
	} else {
		connect := clamp(g.sig.daily("agent.connect", ts, 900, 0.6, 0.5), 120, 40000)
		a.HubConnectUs = proto.Uint32(uint32(connect))
		a.HubConnectMaxUs = proto.Uint32(uint32(clamp(connect*(1.4+g.sig.unit("agent.connectmax", ts)), 150, 90000)))
	}
	a.HubConnectFailuresTotal = proto.Uint64(g.hubFailures)

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
		// The instance is in the signal key too, not only on the wire: two
		// drivetemp chips sharing a key would draw the identical curve, and
		// a pair of disks that track each other to the decimal is the one
		// thing a reader would call a bug in the simulator.
		key := "sensor/" + s.Chip + "/" + s.Label + "/" + s.Instance
		kind := s.Kind
		if kind == "" {
			kind = "temperature"
		}

		// Every kind rises with load, which is what makes them worth
		// charting together on one machine: the package heats up, the fans
		// spin up to answer it, the rails sag a little under the draw and
		// the power figure climbs. Jitter is scaled per kind because a
		// tenth of a volt and a tenth of an RPM are not comparable
		// quantities.
		value := s.Base + s.Swing*(cpu/100) + g.sig.jitter(key, ts)*1.4
		switch kind {
		case "fan":
			value = s.Base + s.Swing*(cpu/100) + g.sig.jitter(key, ts)*40
			if value < 0 {
				value = 0
			}
			// A stalled fan, on isolated scrapes: unit() is a hash of
			// seed+timestamp with no correlation between neighbouring
			// instants, so this is a single zero reading here and there
			// rather than one continuous stall. That is enough for what the
			// fan card is being shown: inside the fine-grained region the
			// backfill writes a sample a minute, so a 5m bucket holding one
			// zero and four healthy readings has an average and a maximum
			// that look fine and a value_min of 0 -- the failure only
			// value_min reports. In the coarse region the grid is itself 5m,
			// so a stalled sample is the bucket's only sample and min, avg
			// and max all read 0 together.
			if s.Stalls && g.sig.unit(key+"/stall", ts) > 0.93 {
				value = 0
			}
		case "voltage":
			// A rail moves in millivolts, and the sag is UNDER load, so the
			// swing is subtracted rather than added.
			value = s.Base - s.Swing*(cpu/100) + g.sig.jitter(key, ts)*0.02
		case "current", "power":
			value = s.Base + s.Swing*(cpu/100) + g.sig.jitter(key, ts)*0.6
			if value < 0 {
				value = 0
			}
		}

		sample := &netrav1.SensorSample{
			TsMs:     ts.UnixMilli(),
			Chip:     s.Chip,
			Label:    s.Label,
			Kind:     kind,
			Instance: s.Instance,
			// Set for every kind, temperature included. The real agent
			// writes value unconditionally and temp only for temperatures,
			// so leaving it unset here would send simulated hosts down a
			// different path through the UI than real ones -- the one place
			// a simulator must not differ.
			Value: proto.Float64(round2(value)),
		}
		if kind == "temperature" {
			sample.Temp = proto.Float64(round2(value))
		}
		out = append(out, sample)
	}
	return out
}

func (g *Generator) nets(ts time.Time) []*netrav1.NetSample {
	out := make([]*netrav1.NetSample, 0, len(g.p.Nets))
	for _, n := range g.p.Nets {
		key := "net/" + n.Iface
		rxChance, rxMagnitude := n.burst(defaultRxBurstMagnitude)
		txChance, txMagnitude := n.burst(defaultTxBurstMagnitude)
		rx := g.sig.spike(
			key+"/rx", ts,
			g.sig.daily(key+"/rx", ts, n.RxBase, 0.85, 0.3),
			rxChance, rxMagnitude,
		)
		tx := g.sig.spike(
			key+"/tx", ts,
			g.sig.daily(key+"/tx", ts, n.TxBase, 0.85, 0.3),
			txChance, txMagnitude,
		)
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

		sample := &netrav1.ContainerSample{
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
			IoRead:       proto.Float64(round2(g.sig.daily(key+"/ior", ts, 40*1024, 1.1, 0.6) * busy)),
			IoWrite:      proto.Float64(round2(g.sig.daily(key+"/iow", ts, 70*1024, 1.1, 0.6) * busy)),
		}

		// Traffic only where the agent could measure it. A host reporting
		// container_network is a host whose agent could not enter the
		// namespaces (containers.go leaves NetRx/NetTx unset in exactly that
		// case), and the container page now reads that capability to explain
		// the empty Network panel -- so a simulated host that declares it and
		// then sends bytes anyway renders "no traffic was measured" over a
		// chart's worth of traffic.
		if g.reports("container_network") {
			sample.NetRx = proto.Float64(round2(g.sig.daily(key+"/rx", ts, 90*1024, 1.0, 0.5) * busy))
			sample.NetTx = proto.Float64(round2(g.sig.daily(key+"/tx", ts, 64*1024, 1.0, 0.5) * busy))
		}
		out = append(out, sample)
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

		if d.NVMe {
			// The synthetic ids from nvmeAttrs (collector/smart.go). No
			// normalized value on any of them: the health log has no such
			// scale, so the real collector leaves it unset and so does this.
			nvme := func(id uint32, raw int64) {
				out = append(out, &netrav1.SmartAttribute{
					TsMs:   ts.UnixMilli(),
					Device: d.Device,
					Model:  d.Model,
					Serial: d.Serial,
					AttrId: id,
					Raw:    proto.Int64(raw),
				})
			}

			nvmeTemp := 44 + 9*g.sig.unit(key+"/temp", ts.Truncate(time.Hour))
			nvme(1000, 0) // critical_warning: clear
			// Wear climbing slowly across the window. Below the 80% warning
			// line, because the fleet already has one drive going bad and a
			// second alarm would make "which drive needs attention" ambiguous.
			nvme(1001, int64(6+progress*2))
			nvme(1002, 100) // available_spare
			nvme(1003, 10)  // available_spare_threshold
			nvme(1004, 0)   // media_errors
			nvme(1005, int64(3+progress*1))
			nvme(1006, d.PowerOnHours+hoursIn)
			nvme(1007, 41)
			nvme(1008, int64(nvmeTemp))
			nvme(1009, 0)
			continue
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

// interfaces derives the link set from the addresses the profile already
// declares, rather than adding a second list to every profile that would then
// have to be kept in step with the first.
//
// Attributes are assigned by name because that is what makes the result
// readable: a simulated fleet where every interface reports "1 Gb/s, full" has
// nothing in it to look at, and the states worth rendering -- a virtual device
// with no speed at all, a bridge that is down -- are exactly the ones a
// uniform generator would never produce.
func (g *Generator) interfaces() []*netrav1.HostInterface {
	// Distinct ifaces, in first-seen order, so the list is deterministic
	// without sorting the addresses themselves.
	seen := make(map[string]*netrav1.HostInterface)
	order := make([]string, 0, len(g.p.Addresses))
	for _, a := range g.p.Addresses {
		if _, ok := seen[a.Iface]; ok {
			continue
		}
		order = append(order, a.Iface)

		link := &netrav1.HostInterface{
			Iface:       a.Iface,
			IfIndex:     proto.Uint32(a.IfIndex),
			Description: a.Description,
		}
		switch {
		case a.Iface == "lo":
			// A loopback has no link layer and no speed, and the kernel
			// reports its operstate as "unknown" rather than "up".
			link.OperState = "unknown"
			link.Mtu = proto.Uint32(65536)
		case strings.HasPrefix(a.Iface, "wg"), strings.HasPrefix(a.Iface, "tun"):
			link.OperState = "unknown"
			link.Mtu = proto.Uint32(1420)
		case strings.HasPrefix(a.Iface, "docker"), strings.HasPrefix(a.Iface, "br"):
			// A bridge with nothing plugged into it is down, which is both
			// the common real state and the one worth being able to see.
			link.OperState = "down"
			link.Mtu = proto.Uint32(1500)
			link.Mac = simMAC(a.Iface, 0x02)
		default:
			link.OperState = "up"
			link.SpeedMbps = proto.Uint64(1000)
			link.Duplex = "full"
			link.Mtu = proto.Uint32(1500)
			link.Mac = simMAC(a.Iface, 0x52)
		}
		seen[a.Iface] = link
	}

	out := make([]*netrav1.HostInterface, 0, len(order))
	for _, name := range order {
		out = append(out, seen[name])
	}
	return out
}

// simMAC builds a stable, obviously-fake MAC from an interface name, so the
// same simulated host reports the same address across restarts.
func simMAC(iface string, oui byte) string {
	var h uint32 = 2166136261
	for i := 0; i < len(iface); i++ {
		h = (h ^ uint32(iface[i])) * 16777619
	}
	return fmt.Sprintf("%02x:00:00:%02x:%02x:%02x",
		oui, byte(h>>16), byte(h>>8), byte(h))
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

// simSnapshotFloor mirrors snapshotFloor in the systemd collector: how often a
// real agent resends the full unit set. Kept equal so a simulated fleet
// exercises the same write pattern the hub sees in production -- in
// particular, that all but the first snapshot after a change are no-ops.
const simSnapshotFloor = 5 * time.Minute

// systemdSnapshot builds the level-triggered unit set a real agent sends every
// simSnapshotFloor.
//
// Returns nil on a host with no systemd, which is what the collector does when
// the bus is unreachable -- and the distinction matters here as much as it
// does there: the hub prunes units missing from a COMPLETE snapshot, so a
// simulated host that sent an empty one would have its units deleted rather
// than left alone.
//
// Aligned to the wall clock rather than counted from the run's start so that a
// backfill and a live run put snapshots at the same instants, and replaying a
// window twice writes the same rows.
func (g *Generator) systemdSnapshot(ts time.Time) *netrav1.SystemdSnapshot {
	p := g.p
	if p.Capabilities["systemd"] == "unavailable" || len(p.Units) == 0 {
		return nil
	}
	if !ts.Truncate(simSnapshotFloor).Equal(ts) {
		return nil
	}

	moved := g.sched.unitStates(ts)
	units := make([]*netrav1.SystemdUnitState, 0, len(p.Units))
	for _, name := range p.Units {
		// A unit the schedule never touched has never failed, so it is a
		// healthy daemon. Only units with events have any other state.
		st := unitState{state: "active", substate: "running"}
		if u, ok := moved[name]; ok {
			st = u
		}
		units = append(units, &netrav1.SystemdUnitState{
			UnitName: name,
			State:    st.state,
			Substate: st.substate,
		})
	}
	return &netrav1.SystemdSnapshot{
		TsMs:     ts.UnixMilli(),
		Complete: true,
		Units:    units,
	}
}
