package collector_test

import (
	"testing"

	"github.com/trick77/netra/internal/agent/collector"
	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
)

// A container set larger than one scrape can carry must be truncated, not
// emitted whole.
//
// maxBatchRows keeps a multi-scrape body inside the hub's 4 MiB cap, but the
// flush lets a single oversized scrape through alone -- and the agent now drops
// what the hub refuses. Dropping beats wedging, but a host that produces an
// oversized scrape every tick would report nothing forever. Containers was the
// one family with no bound at all.
func TestContainerRowsAreCappedAndStableAcrossScrapes(t *testing.T) {
	rows := make([]*netrav1.ContainerSample, collector.MaxContainerRowsForTest+50)
	for i := range rows {
		rows[i] = &netrav1.ContainerSample{TsMs: 1}
	}

	got := collector.CapContainerRowsForTest(rows)
	if len(got) != collector.MaxContainerRowsForTest {
		t.Errorf("kept %d rows, want %d", len(got), collector.MaxContainerRowsForTest)
	}

	// Truncating a prefix of an id-sorted slice keeps the SAME containers from
	// one scrape to the next, so a chart does not flicker between them.
	again := collector.CapContainerRowsForTest(rows)
	for i := range got {
		if got[i] != again[i] {
			t.Fatalf("row %d differs between scrapes; the survivors are not stable", i)
		}
	}
}

// A normal host must pass through untouched -- the cap is a backstop, not a
// routine truncation.
func TestContainerRowsUnderTheCapAreUntouched(t *testing.T) {
	rows := make([]*netrav1.ContainerSample, 12)
	for i := range rows {
		rows[i] = &netrav1.ContainerSample{TsMs: 1}
	}
	if got := collector.CapContainerRowsForTest(rows); len(got) != len(rows) {
		t.Errorf("kept %d rows, want all %d", len(got), len(rows))
	}
}
