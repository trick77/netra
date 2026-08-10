package collector_test

import (
	"math"
	"testing"

	"github.com/trick77/netra/internal/agent/collector"
	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

func TestLoadReadsLoadavgAndUptime(t *testing.T) {
	c := collector.NewLoad("testdata/proc1")

	var s netrav1.HostSample
	if err := collectInto(c, &s); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if math.Abs(s.GetLoad1()-0.52) > 0.0001 {
		t.Fatalf("Load1 = %v, want 0.52", s.GetLoad1())
	}
	if math.Abs(s.GetLoad5()-0.41) > 0.0001 {
		t.Fatalf("Load5 = %v, want 0.41", s.GetLoad5())
	}
	if math.Abs(s.GetLoad15()-0.38) > 0.0001 {
		t.Fatalf("Load15 = %v, want 0.38", s.GetLoad15())
	}
	// Host uptime, truncated to whole seconds.
	if got, want := s.GetUptimeS(), uint64(123456); got != want {
		t.Fatalf("UptimeS = %d, want %d", got, want)
	}
}

func TestLoadMissingFileIsAnError(t *testing.T) {
	c := collector.NewLoad("testdata/does-not-exist")

	var s netrav1.HostSample
	if err := collectInto(c, &s); err == nil {
		t.Fatal("Collect() succeeded with no /proc tree, want an error")
	}
}
