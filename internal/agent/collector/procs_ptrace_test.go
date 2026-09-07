package collector_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/trick77/netra/internal/agent/collector"
)

// writeComm puts a process name at <root>/<pid>/comm, creating the directory.
func writeComm(t *testing.T, root, pid, name string) {
	t.Helper()
	dir := filepath.Join(root, pid)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "comm"), []byte(name+"\n"), 0o644); err != nil {
		t.Fatalf("write %s/comm: %v", dir, err)
	}
}

// procsCapability runs one scrape and returns the processes capability.
func procsCapability(t *testing.T, testee *collector.Procs) string {
	t.Helper()
	if _, err := testee.Collect(context.Background()); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	return testee.Capabilities()["processes"]
}

// The PID-namespace heuristic asks its fixed questions ONCE.
//
// The regression this pins is not a slow collector. Step 3 reads /proc/1/comm,
// and with `pid: host` granted PID 1 is the HOST's own init -- an unconfined
// process that a docker-default container may not ptrace. Asking on every
// scrape produced one AppArmor denial per minute in the host's kernel log,
// forever: 1440 audit lines a day per agent, and on one surveyed host they
// were the most frequent kernel message on the box by an order of magnitude,
// 508 of the entries its ring buffer still held.
//
// The answer was never wrong. What was wrong was how often it was asked, so no
// assertion on the reported VALUE could have caught this. What can be observed
// from here is that changing an input the first scrape already read does not
// change the verdict -- which is only true if it was not read again.
func TestProcsAsksThePidNamespaceQuestionOnce(t *testing.T) {
	root := t.TempDir()
	// PID 1 shares this process's comm, so step 3 fires: the agent is the
	// namespace's own init.
	writeComm(t, root, "self", "netra-agent")
	writeComm(t, root, "1", "netra-agent")
	// Comfortably past minPlausibleProcs, so step 4 could not reach the same
	// verdict on its own and the assertion is about step 3 alone.
	for _, pid := range []string{"2", "3", "4", "5", "6", "7", "8"} {
		writeComm(t, root, pid, "something")
	}

	testee := collector.NewProcs(root, false)
	if got := procsCapability(t, testee); got != "namespaced" {
		t.Fatalf("capability = %q, want namespaced: PID 1 shares this process's comm", got)
	}

	// Now make step 3 say the opposite. A collector that re-reads /proc/1/comm
	// flips to "not namespaced"; one that asked once does not.
	writeComm(t, root, "1", "systemd")

	if got := procsCapability(t, testee); got != "namespaced" {
		t.Errorf("capability = %q after /proc/1/comm changed, want namespaced still: "+
			"a process cannot change its PID namespace, so the file must not be read again", got)
	}
}

// Failing to READ the comm is not a verdict.
//
// "could not read /proc/1/comm" is the absence of an answer, not a no. Caching
// it as "not namespaced" would make a container with two processes in it report
// a plausible-looking count of 2 as though it were the host -- exactly the
// wrong number this collector reports nothing rather than produce.
func TestProcsFallsThroughToTheCountWhenTheCommIsUnreadable(t *testing.T) {
	root := t.TempDir()
	// No comm files at all, and only two processes: step 3 cannot answer, so
	// step 4 has to.
	for _, pid := range []string{"1", "2"} {
		if err := os.MkdirAll(filepath.Join(root, pid), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}

	testee := collector.NewProcs(root, false)
	if got := procsCapability(t, testee); got != "namespaced" {
		t.Errorf("capability = %q, want namespaced: two processes is not a host", got)
	}

	// And the count check keeps running every scrape, unlike steps 1 to 3.
	for _, pid := range []string{"3", "4", "5", "6", "7", "8"} {
		if err := os.MkdirAll(filepath.Join(root, pid), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	if got := procsCapability(t, testee); got != "ok" {
		t.Errorf("capability = %q once the count became plausible, want ok: "+
			"the process count is the one input that changes, and is still read every scrape", got)
	}
}

// The operator's own statement short-circuits before any /proc read.
//
// A host that sets AGENT_PID_HOST=1 never reaches the comm comparison, which
// is what makes setting it the documented remedy for the audit noise as well
// as for the wrong count.
func TestProcsReadsNoCommWhenTheOperatorAnswered(t *testing.T) {
	root := t.TempDir()
	// Two processes and no comm files: both of the other checks would say
	// "namespaced" if they ran.
	for _, pid := range []string{"1", "2"} {
		if err := os.MkdirAll(filepath.Join(root, pid), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}

	testee := collector.NewProcs(root, true)
	if got := procsCapability(t, testee); got != "ok" {
		t.Errorf("capability = %q, want ok: the operator said this is the host", got)
	}
}
