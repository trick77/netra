package collector_test

import (
	"context"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/trick77/netra/internal/agent/collector"
	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
)

// collectInto runs c and merges its host row into sample.
//
// It exists so the parsing tests can keep asserting against one sample the way
// they did when collectors wrote into it directly. Those tests are about what
// a collector reads out of /proc, not about how the result is handed back --
// the contract itself is covered by the tests in this file and by the client's
// merge tests. It mirrors the agent's own loop: a failed collector contributes
// nothing.
func collectInto(c collector.Collector, sample *netrav1.HostSample) error {
	res, err := c.Collect(context.Background())
	if err != nil {
		return err
	}
	if res.Host != nil {
		proto.Merge(sample, res.Host)
	}
	return nil
}

// A collector that fails must contribute nothing at all.
//
// Under the previous contract a collector wrote into a shared HostSample, so a
// failure part-way through left the fields it had already written behind. Those
// fields were then stored as though they had been measured, which is
// indistinguishable from a real reading and silently breaks the rule that an
// unset field means the subsystem is absent (spec 5.1 rule 3).
func TestFailingCollectorReturnsNoPartialResult(t *testing.T) {
	// Given: a collector pointed at a tree with no meminfo in it.
	testee := collector.NewMemory(t.TempDir())

	// When: it collects.
	res, err := testee.Collect(context.Background())

	// Then: it reports the failure and hands back nothing.
	if err == nil {
		t.Fatal("Collect succeeded against an empty proc root; want an error")
	}
	if res != nil {
		t.Errorf("Collect returned %+v alongside an error; a failed collector must return nil", res)
	}
}

// "Nothing to report yet" is not a failure. A delta-based collector on its
// first scrape has no baseline, and must say so by returning an empty result
// rather than an error, which the agent would log as a fault every restart.
func TestCollectorWithoutBaselineReturnsEmptyResultNotError(t *testing.T) {
	// Given: a delta-based collector that has never run.
	testee := collector.NewCPU("testdata/proc1")

	// When: it collects for the first time.
	res, err := testee.Collect(context.Background())

	// Then: no error, and nothing measured.
	if err != nil {
		t.Fatalf("first Collect: %v", err)
	}
	if res == nil {
		t.Fatal("Collect returned nil without an error; want an empty result")
	}
	if res.Host != nil && res.Host.CpuTotal != nil {
		t.Error("CpuTotal is set on the first scrape; a rate needs a baseline")
	}
}
