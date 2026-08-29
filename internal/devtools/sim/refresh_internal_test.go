package sim

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// aggregateChains is hand-maintained, and a continuous aggregate missing from
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
func TestAggregateChainsCoverEveryContinuousAggregate(t *testing.T) {
	// Given: every continuous aggregate the migrations create.
	declared := aggregatesInMigrations(t)

	// When: compared against the list the refresher walks.
	listed := make(map[string]bool)
	for _, chain := range aggregateChains {
		for _, agg := range chain {
			listed[agg.view] = true
		}
	}

	// Then: neither side has anything the other does not.
	for _, view := range declared {
		if !listed[view] {
			t.Errorf("%s is a continuous aggregate but aggregateChains never refreshes it; "+
				"a backfill leaves its rolled-up tiers empty and the UI reports the data as not collected",
				view)
		}
	}
	for view := range listed {
		if !contains(declared, view) {
			t.Errorf("aggregateChains refreshes %s, which the migrations do not create", view)
		}
	}
}

// Each view in a chain reads FROM the one before it -- _1h from _5m, _1d from
// _1h -- rather than from the raw hypertable, so refreshing them in the wrong
// order materialises nothing at all: silently, because refreshing an empty
// source is not an error. The ordering is asserted here rather than trusted
// to the comment above the list.
func TestEachChainRefreshesFineToCoarse(t *testing.T) {
	want := []struct {
		suffix string
		bucket time.Duration
	}{{"_5m", tier5m}, {"_1h", tier1h}, {"_1d", tier1d}}

	for _, chain := range aggregateChains {
		for i, w := range want {
			if !strings.HasSuffix(chain[i].view, w.suffix) {
				t.Errorf("%s is at position %d of its chain but is not the %s tier",
					chain[i].view, i, strings.TrimPrefix(w.suffix, "_"))
			}
			if chain[i].bucket != w.bucket {
				t.Errorf("%s carries the wrong bucket width", chain[i].view)
			}
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

// Two DriveSpecs with the same Device on one profile silently become ONE
// drive, and the merge is invisible from a browser.
//
// devices is unique on (host_id, device) and resolveDeviceIDs skips a name it
// has already resolved, so the second spec's attributes land under the first
// spec's device_id. Both id spaces then sit on one row: driveKind sees an
// NVMe id and calls the whole thing NVMe, the ATA drive disappears from the
// table, and the result renders exactly like the drive that was intended.
//
// That is what happened when the NVMe drive was added to the bare-metal
// profile, which already had an nvme0n1 -- five specs, four rows, and nothing
// on screen to say so.
func TestProfileDriveNamesAreUniquePerHost(t *testing.T) {
	for _, p := range Fleet() {
		seen := map[string]bool{}
		for _, d := range p.Drives {
			if seen[d.Device] {
				t.Errorf("%s declares %s twice; the two specs merge into one "+
					"devices row and one of the drives vanishes from the table",
					p.Hostname, d.Device)
			}
			seen[d.Device] = true
		}
	}
}

// The same collision, one table over: two DiskSpecs with one name would make
// disk_io_samples' (host_id, ts, device) key discard one of them on every
// scrape.
func TestProfileDiskNamesAreUniquePerHost(t *testing.T) {
	for _, p := range Fleet() {
		seen := map[string]bool{}
		for _, d := range p.Disks {
			if seen[d.Device] {
				t.Errorf("%s declares disk %s twice; one of the two is dropped "+
					"by the samples table's natural key", p.Hostname, d.Device)
			}
			seen[d.Device] = true
		}
	}
}

// A profile that declares drives but does not run the smart collector reports
// SMART the fleet's own capability map says it cannot read. The VPS is the
// case this protects: it declares smart: no-device-access because a hypervisor
// does not pass SMART through.
func TestProfilesWithDrivesRunTheSmartCollector(t *testing.T) {
	for _, p := range Fleet() {
		if len(p.Drives) == 0 {
			continue
		}
		if !p.runsCollector("smart") {
			t.Errorf("%s declares %d drives but does not run the smart collector; "+
				"the data table and the health table would contradict each other",
				p.Hostname, len(p.Drives))
		}
	}
}
