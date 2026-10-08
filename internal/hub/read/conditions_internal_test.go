package read

import (
	"testing"
	"time"

	"github.com/trick77/netra/internal/hub/conditions"
)

// A temperature the scan just judged must not read as stale: the scan takes
// readings from a five-scrape window, so the page must too.
func TestTemperatureStalenessMatchesTheScanWindow(t *testing.T) {
	lastSeen := testNow
	for _, tc := range []struct {
		age  time.Duration
		want bool
	}{
		{4 * time.Minute, false},
		{6 * time.Minute, true},
	} {
		measured := lastSeen.Add(-tc.age)
		if got := subjectIsStale(conditions.KindTemperature, &measured, &lastSeen); got != tc.want {
			t.Errorf("measured %v before last_seen: stale = %v, want %v", tc.age, got, tc.want)
		}
	}
}
