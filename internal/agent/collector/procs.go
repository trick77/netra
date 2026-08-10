package collector

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

// Capability values reported by Procs.
const (
	procsCapOK          = "ok"
	procsCapNamespaced  = "namespaced"
	procsCapUnavailable = "unavailable"
)

// minPlausibleProcs is the count below which a reading is treated as a view
// into a PID namespace rather than a host. A real Linux host runs dozens of
// kernel threads before any userspace starts, so a handful of entries means
// the agent is seeing only its own namespace.
const minPlausibleProcs = 5

// Procs reports the total number of processes on the host.
//
// This is deliberately distinct from the per-name counts a future process
// collector will report: those are a top-N list and cannot answer "how many
// processes exist". It counts numeric entries in /proc and opens none of
// them, except one comm file used to detect a PID namespace.
//
// Without pid: host the agent sees only its own namespace, where the count is
// a meaningless 1 or 2. That case reports nothing at all rather than a
// plausible-looking wrong number.
type Procs struct {
	procRoot string

	// pidHost is the operator's own statement, from NETRA_PID_HOST. The setup
	// script knows whether it rendered pid: host, so on a script-installed
	// host this replaces the heuristics below with a fact.
	pidHost bool

	mu           sync.Mutex
	capabilities map[string]string
}

// NewProcs builds a Procs collector reading from procRoot. pidHost carries
// what the operator configured, and is trusted over the heuristics.
func NewProcs(procRoot string, pidHost bool) *Procs {
	return &Procs{procRoot: procRoot, pidHost: pidHost}
}

// Name implements Collector.
func (p *Procs) Name() string { return "procs" }

// SetProcRootForTest repoints the collector at a different fixture tree.
func (p *Procs) SetProcRootForTest(root string) { p.procRoot = root }

// Capabilities implements CapabilityReporter.
func (p *Procs) Capabilities() map[string]string {
	p.mu.Lock()
	defer p.mu.Unlock()

	out := make(map[string]string, len(p.capabilities))
	for k, v := range p.capabilities {
		out[k] = v
	}
	return out
}

func (p *Procs) setCapability(value string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.capabilities == nil {
		p.capabilities = make(map[string]string, 1)
	}
	p.capabilities["processes"] = value
}

// Collect implements Collector.
func (p *Procs) Collect(_ context.Context) (*Result, error) {
	sample := &netrav1.HostSample{}

	entries, err := os.ReadDir(p.procRoot)
	if err != nil {
		// An unreadable /proc leaves the field unset. It is not an error the
		// scrape should fail on: every other collector reading the same tree
		// will report its own failure.
		//
		// Reported as unavailable, not namespaced: a missing bind mount, a
		// misconfigured NETRA_PROC_ROOT and a permission error all land here,
		// and none of them is fixed by adding pid: host. The namespace verdict
		// is only reached below, where the tree was actually readable.
		p.setCapability(procsCapUnavailable)
		return &Result{Host: sample}, nil
	}

	count := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := strconv.ParseUint(e.Name(), 10, 64); err == nil {
			count++
		}
	}

	if p.namespaced(count) {
		p.setCapability(procsCapNamespaced)
		return &Result{Host: sample}, nil
	}

	p.setCapability(procsCapOK)
	n := uint32(count)
	sample.ProcessesTotal = &n

	return &Result{Host: sample}, nil
}

// namespaced reports whether this reading is a PID namespace's view rather
// than the host's.
//
// The checks are ordered by decreasing confidence and short-circuit, so the
// operator's own configuration wins over every guess. The residual failure
// mode is accepted and documented: a container started with a shell as PID 1,
// no NETRA_PID_HOST, and more than a handful of processes reports an
// implausibly low count as though it were the host. It degrades to a wrong
// number rather than a crash, and setting NETRA_PID_HOST removes the guess.
func (p *Procs) namespaced(count int) bool {
	// 1. The operator said so.
	if p.pidHost {
		return false
	}

	// 2. The agent is itself PID 1, which never happens on a host.
	if os.Getpid() == 1 {
		return true
	}

	// 3. PID 1 has the same comm as this process, i.e. the agent is the
	//    namespace's init. comm is the 15-byte process name; argv is never
	//    read here, and must never be -- see argv_guard_test.go.
	if self, ok := p.readComm("self"); ok {
		if one, ok := p.readComm("1"); ok && one == self {
			return true
		}
	}

	// 4. Too few processes to be a host, which always runs kernel threads.
	return count < minPlausibleProcs
}

func (p *Procs) readComm(pid string) (string, bool) {
	raw, err := os.ReadFile(filepath.Join(p.procRoot, pid, "comm"))
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(raw)), true
}
