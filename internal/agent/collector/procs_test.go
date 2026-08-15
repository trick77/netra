package collector_test

import (
	"testing"

	"github.com/trick77/netra/internal/agent/collector"
	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
)

// The fixture holds five numeric PID directories plus "self" and "net", which
// are directories in /proc that are not processes.
func TestProcsCountsNumericDirentsOnly(t *testing.T) {
	p := collector.NewProcs("testdata/procpids", true)

	var sample netrav1.HostSample
	if err := collectInto(p, &sample); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if sample.ProcessesTotal == nil || *sample.ProcessesTotal != 5 {
		t.Errorf("ProcessesTotal = %v, want 5", sample.ProcessesTotal)
	}
	if got := p.Capabilities()["processes"]; got != "ok" {
		t.Errorf("capability = %q, want %q", got, "ok")
	}
}

// Inside a PID namespace the count is a meaningless 1 or 2. Reporting that as
// the host's process count would look entirely plausible on a dashboard,
// which is exactly what makes it worth refusing.
func TestProcsUnsetWhenProcOneCommMatchesSelf(t *testing.T) {
	p := collector.NewProcs("testdata/procpids-namespaced", false)

	var sample netrav1.HostSample
	if err := collectInto(p, &sample); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if sample.ProcessesTotal != nil {
		t.Errorf("ProcessesTotal = %v, want nil inside a PID namespace", *sample.ProcessesTotal)
	}
	if got := p.Capabilities()["processes"]; got != "namespaced" {
		t.Errorf("capability = %q, want %q", got, "namespaced")
	}
}

// A host always runs kernel threads, so a handful of entries means a
// namespace even when the comm check does not fire.
func TestProcsUnsetWhenTooFewProcessesToBeAHost(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir+"/1/comm", "sh\n")
	writeFile(t, dir+"/7/comm", "netra-agent\n")

	p := collector.NewProcs(dir, false)

	var sample netrav1.HostSample
	if err := collectInto(p, &sample); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if sample.ProcessesTotal != nil {
		t.Errorf("ProcessesTotal = %v, want nil for an implausibly small count",
			*sample.ProcessesTotal)
	}
}

// NETRA_PID_HOST is the operator's own statement, and setup-agent.sh knows
// what it rendered. It must beat every heuristic -- otherwise a host that
// genuinely runs very few processes would be misread as a container forever.
func TestProcsTrustsPidHostConfigOverHeuristics(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir+"/1/comm", "netra-agent\n")
	writeFile(t, dir+"/self/comm", "netra-agent\n")

	// Both the comm check and the low-count check would say "namespaced".
	p := collector.NewProcs(dir, true)

	var sample netrav1.HostSample
	if err := collectInto(p, &sample); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if sample.ProcessesTotal == nil || *sample.ProcessesTotal != 1 {
		t.Errorf("ProcessesTotal = %v, want 1 when pid: host is configured",
			sample.ProcessesTotal)
	}
}

// An unreadable /proc is another collector's problem to report. This one
// leaves its field unset and does not fail the scrape.
//
// It reports unavailable rather than namespaced: a missing bind mount, a
// misconfigured NETRA_PROC_ROOT and a permission error all reach this branch,
// and "namespaced" would send the operator off to add pid: host, which fixes
// none of them.
func TestProcsUnreadableProcRootIsUnsetNotZero(t *testing.T) {
	p := collector.NewProcs("testdata/does-not-exist", false)

	var sample netrav1.HostSample
	if err := collectInto(p, &sample); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if sample.ProcessesTotal != nil {
		t.Errorf("ProcessesTotal = %v, want nil", *sample.ProcessesTotal)
	}
	if got := p.Capabilities()["processes"]; got != "unavailable" {
		t.Errorf("capability = %q, want %q", got, "unavailable")
	}
}

// The two unset reasons must stay distinguishable end to end: the same
// collector reports namespaced only when it could actually read the tree.
func TestProcsUnreadableAndNamespacedReportDifferentCauses(t *testing.T) {
	unreadable := collector.NewProcs("testdata/does-not-exist", false)
	namespaced := collector.NewProcs("testdata/procpids-namespaced", false)

	var sample netrav1.HostSample
	if err := collectInto(unreadable, &sample); err != nil {
		t.Fatalf("Collect (unreadable): %v", err)
	}
	if err := collectInto(namespaced, &sample); err != nil {
		t.Fatalf("Collect (namespaced): %v", err)
	}

	got := unreadable.Capabilities()["processes"]
	want := namespaced.Capabilities()["processes"]
	if got == want {
		t.Errorf("unreadable and namespaced both report %q, want distinct causes", got)
	}
}

// Capabilities is read by the client on every scrape while the collector may
// still be writing to it. Handing out the live map would race.
func TestProcsCapabilitiesReturnsACopy(t *testing.T) {
	p := collector.NewProcs("testdata/procpids", true)

	var sample netrav1.HostSample
	if err := collectInto(p, &sample); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	caps := p.Capabilities()
	caps["processes"] = "tampered"

	if got := p.Capabilities()["processes"]; got != "ok" {
		t.Errorf("capability = %q after mutating the returned map, want %q", got, "ok")
	}
}

func TestProcsName(t *testing.T) {
	p := collector.NewProcs("testdata/procpids", false)

	if got := p.Name(); got != "procs" {
		t.Errorf("Name() = %q, want %q", got, "procs")
	}
}

// The interface is optional, so the wiring that looks for it has to actually
// find it on this type.
func TestProcsImplementsCapabilityReporter(t *testing.T) {
	var _ collector.CapabilityReporter = collector.NewProcs("testdata/procpids", false)
}
