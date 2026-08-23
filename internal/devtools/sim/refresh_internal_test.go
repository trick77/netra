package sim

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// aggregatePairs is hand-maintained, and a continuous aggregate missing from
// it fails in the quietest way this simulator can fail: the raw table fills
// normally, every unit test passes, and the tier the UI actually reads at 24h
// and beyond is simply empty. The panel then says "not collected" about a
// host that collected it -- which is the one sentence spec 7.6 forbids, on
// data that is sitting in the database.
//
// That is exactly what host_proto_samples did when it was added: 11520 raw
// rows, an empty 5m tier, and two panels reporting a healthy host as silent.
//
// So the list is pinned against the schema itself. Reading the .sql rather
// than the database keeps this a unit test -- it runs on every `go test ./...`
// rather than only when a TimescaleDB happens to be pointed at.
func TestAggregatePairsCoverEveryContinuousAggregate(t *testing.T) {
	// Given: every continuous aggregate the migrations create.
	declared := aggregatesInMigrations(t)

	// When: compared against the list the refresher walks.
	listed := make(map[string]bool)
	for _, pair := range aggregatePairs {
		for _, agg := range pair {
			listed[agg.view] = true
		}
	}

	// Then: neither side has anything the other does not.
	for _, view := range declared {
		if !listed[view] {
			t.Errorf("%s is a continuous aggregate but aggregatePairs never refreshes it; "+
				"a backfill leaves its rolled-up tiers empty and the UI reports the data as not collected",
				view)
		}
	}
	for view := range listed {
		if !contains(declared, view) {
			t.Errorf("aggregatePairs refreshes %s, which the migrations do not create", view)
		}
	}
}

// The 1h view of a pair reads FROM the 5m view rather than from the raw
// hypertable, so refreshing them in the wrong order materialises nothing at
// all -- silently, because refreshing an empty source is not an error. The
// ordering is asserted here rather than trusted to the comment above the
// list.
func TestEachPairRefreshesTheFiveMinuteTierFirst(t *testing.T) {
	for _, pair := range aggregatePairs {
		if !strings.HasSuffix(pair[0].view, "_5m") {
			t.Errorf("%s is first in its pair but is not the 5m tier", pair[0].view)
		}
		if !strings.HasSuffix(pair[1].view, "_1h") {
			t.Errorf("%s is second in its pair but is not the 1h tier", pair[1].view)
		}
		if pair[0].bucket != tier5m || pair[1].bucket != tier1h {
			t.Errorf("%s/%s carry the wrong bucket widths", pair[0].view, pair[1].view)
		}
	}
}

var continuousAggregate = regexp.MustCompile(
	`(?is)CREATE\s+MATERIALIZED\s+VIEW\s+(?:IF\s+NOT\s+EXISTS\s+)?(\w+)\s+WITH\s*\(\s*timescaledb\.continuous`)

// aggregatesInMigrations reads the migration SQL the hub embeds.
//
// The path is relative because this test and the migrations are in one
// repository and always will be; a schema that moved would break the build
// here rather than silently assert nothing.
func aggregatesInMigrations(t *testing.T) []string {
	t.Helper()

	dir := filepath.Join("..", "..", "hub", "store", "migrations")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}

	var out []string
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		for _, m := range continuousAggregate.FindAllStringSubmatch(string(body), -1) {
			out = append(out, m[1])
		}
	}
	if len(out) == 0 {
		t.Fatal("no continuous aggregates found in the migrations; the pattern has stopped matching")
	}
	return out
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
