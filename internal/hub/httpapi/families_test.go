package httpapi_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
)

// fullRequest populates every per-entity family, so the tests below exercise
// the whole storage path rather than one family at a time.
func fullRequest(seq uint64, ts int64) *netrav1.IngestRequest {
	return &netrav1.IngestRequest{
		Seq:         seq,
		HostSamples: []*netrav1.HostSample{{TsMs: ts, CpuTotal: proto.Float64(10)}},
		CpuCores:    []*netrav1.CpuCoreSample{{TsMs: ts, Core: 0, Busy: proto.Float64(50)}},
		DiskIo: []*netrav1.DiskIoSample{
			{TsMs: ts, Device: "sda", ReadBytes: proto.Float64(1000)},
		},
		Sensors: []*netrav1.SensorSample{
			{TsMs: ts, Chip: "coretemp", Label: "Package id 0", Temp: proto.Float64(45)},
		},
		Net: []*netrav1.NetSample{
			{TsMs: ts, Iface: "eth0", RxBytes: proto.Float64(2000)},
		},
		Containers: []*netrav1.ContainerSample{
			{TsMs: ts, ContainerKey: "proj/web", Name: "proj-web-1", Image: "nginx:1",
				CpuPct: proto.Float64(25), MemUsed: proto.Uint64(1024)},
		},
		Filesystems: []*netrav1.FilesystemSample{
			{TsMs: ts, Label: "/", Mountpoint: "/", Total: proto.Uint64(1000), Used: proto.Uint64(400)},
		},
		Smart: []*netrav1.SmartAttribute{
			{TsMs: ts, Device: "sda", Model: "Samsung", AttrId: 5, Raw: proto.Int64(0)},
		},
		Collectors: []*netrav1.CollectorSample{
			{TsMs: ts, Collector: "sensors", Ok: true, DurationMs: proto.Uint32(3)},
		},
		Events: []*netrav1.Event{
			{TsMs: ts, Type: "mdraid", Subject: "md0", DetailJson: `{"state":"clean"}`},
		},
		SystemdEvents: []*netrav1.SystemdUnitEvent{
			{TsMs: ts, UnitName: "ssh.service", State: "active", Substate: "running"},
		},
		PackageEvents: []*netrav1.PackageEvent{
			{TsMs: ts, Name: "bash", Action: "upgrade", FromVersion: "5.2", ToVersion: "5.3"},
		},
		Addresses: []*netrav1.HostAddress{
			{Iface: "eth0", IfIndex: proto.Uint32(2), Address: "10.0.0.5", Family: 4},
		},
		Packages: []*netrav1.HostPackage{
			{Name: "bash", Version: "5.3", Arch: "amd64", Format: "dpkg", SizeBytes: proto.Uint64(1024)},
		},
	}
}

// familyTables are every table a full request writes to, so a family added
// without a storage path fails here rather than silently going nowhere.
var familyTables = []string{
	"cpu_core_samples", "disk_io_samples", "sensor_samples", "net_samples",
	"container_samples", "filesystem_samples", "smart_attributes",
	"collector_samples", "events",
	"systemd_unit_events", "package_events", "host_addresses", "host_packages",
}

// Every family in a request must reach its table.
func TestIntegrationIngestStoresEveryFamily(t *testing.T) {
	srv, token, s := newFixture(t)
	ctx := context.Background()
	ts := time.Now().Add(-time.Minute).UnixMilli()

	resp := post(t, srv, token, fullRequest(1, ts))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	for _, table := range familyTables {
		var n int
		if err := s.Pool().QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if n == 0 {
			t.Errorf("%s is empty; the family reached no storage path", table)
		}
	}
}

// A replayed batch re-sends every family, not just host samples.
//
// Any family whose insert lacks ON CONFLICT DO NOTHING fails the whole flush,
// and the agent's ring buffer then never drains -- it re-sends the identical
// batch forever.
func TestIntegrationReplayingAWholeScrapeIsAbsorbedByEveryFamily(t *testing.T) {
	srv, token, s := newFixture(t)
	ctx := context.Background()
	ts := time.Now().Add(-time.Minute).UnixMilli()

	before := map[string]int{}
	if resp := post(t, srv, token, fullRequest(1, ts)); resp.StatusCode != http.StatusOK {
		t.Fatalf("first post status = %d, want 200", resp.StatusCode)
	}
	for _, table := range familyTables {
		var n int
		if err := s.Pool().QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		before[table] = n
	}

	// The identical batch again, exactly as a replay after an outage sends it.
	if resp := post(t, srv, token, fullRequest(1, ts)); resp.StatusCode != http.StatusOK {
		t.Fatalf("replay status = %d, want 200 -- a replay must be absorbed, not rejected", resp.StatusCode)
	}

	for _, table := range familyTables {
		var n int
		if err := s.Pool().QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if n != before[table] {
			t.Errorf("%s went from %d to %d rows on replay; the insert must absorb duplicates",
				table, before[table], n)
		}
	}
}

// A natural key the hub has never seen creates its dimension row once, and
// every later scrape reuses it. A second row would split the container's
// history silently across two ids.
func TestIntegrationRepeatedNaturalKeyCreatesOneDimensionRow(t *testing.T) {
	srv, token, s := newFixture(t)
	ctx := context.Background()

	for i := range 3 {
		ts := time.Now().Add(-time.Duration(3-i) * time.Minute).UnixMilli()
		if resp := post(t, srv, token, fullRequest(uint64(i+1), ts)); resp.StatusCode != http.StatusOK {
			t.Fatalf("post %d status = %d, want 200", i, resp.StatusCode)
		}
	}

	for _, d := range []struct{ table, column, value string }{
		{"containers", "container_key", "proj/web"},
		{"filesystems", "label", "/"},
		{"devices", "device", "sda"},
		{"systemd_units", "unit_name", "ssh.service"},
		{"sensors", "chip", "coretemp"},
	} {
		var n int
		if err := s.Pool().QueryRow(ctx,
			`SELECT count(*) FROM `+d.table+` WHERE `+d.column+` = $1`, d.value).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", d.table, err)
		}
		if n != 1 {
			t.Errorf("%s has %d rows for %q, want 1 -- the history must not split across ids",
				d.table, n, d.value)
		}
	}

	// And the samples all point at that one row.
	var containers int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(DISTINCT container_id) FROM container_samples`).Scan(&containers); err != nil {
		t.Fatalf("count distinct container_id: %v", err)
	}
	if containers != 1 {
		t.Errorf("container_samples reference %d containers, want 1", containers)
	}
}

// scope is derived BY THE HUB from the address (spec §5.2). The agent sends
// none, so a stored row with an empty scope means the classification never
// ran -- and every "which hosts are publicly reachable" query would miss.
func TestIntegrationHubDerivesAddressScope(t *testing.T) {
	srv, token, s := newFixture(t)
	ctx := context.Background()
	ts := time.Now().Add(-time.Minute).UnixMilli()

	req := fullRequest(1, ts)
	req.Addresses = []*netrav1.HostAddress{
		{Iface: "eth0", Address: "10.0.0.5", Family: 4},
		{Iface: "eth0", Address: "8.8.8.8", Family: 4},
		{Iface: "eth0", Address: "2001:db8::1", Family: 6},
	}

	if resp := post(t, srv, token, req); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	for _, c := range []struct{ addr, want string }{
		{"10.0.0.5", "private"},
		{"8.8.8.8", "public"},
		{"2001:db8::1", "public"},
	} {
		var scope string
		if err := s.Pool().QueryRow(ctx,
			`SELECT coalesce(scope, '') FROM host_addresses WHERE host(address) = $1`, c.addr).Scan(&scope); err != nil {
			t.Fatalf("select scope for %s: %v", c.addr, err)
		}
		if scope != c.want {
			t.Errorf("scope for %s = %q, want %q", c.addr, scope, c.want)
		}
	}
}

// An address the host no longer has must go. Leaving it behind means a subnet
// query still matches a host that moved off that network months ago.
func TestIntegrationAddressesNoLongerReportedArePruned(t *testing.T) {
	srv, token, s := newFixture(t)
	ctx := context.Background()
	ts := time.Now().Add(-2 * time.Minute).UnixMilli()

	req := fullRequest(1, ts)
	req.Addresses = []*netrav1.HostAddress{
		{Iface: "eth0", Address: "10.0.0.5", Family: 4},
		{Iface: "eth0", Address: "10.0.0.6", Family: 4},
	}
	if resp := post(t, srv, token, req); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	// The host renumbers and reports only one of them.
	req = fullRequest(2, time.Now().Add(-time.Minute).UnixMilli())
	req.Addresses = []*netrav1.HostAddress{
		{Iface: "eth0", Address: "10.0.0.5", Family: 4},
	}
	if resp := post(t, srv, token, req); resp.StatusCode != http.StatusOK {
		t.Fatalf("second status = %d, want 200", resp.StatusCode)
	}

	var n int
	if err := s.Pool().QueryRow(ctx, `SELECT count(*) FROM host_addresses`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("host_addresses = %d, want 1 -- the dropped address must be pruned", n)
	}
}

// One poison timestamp in one family must not fail the batch, and must not
// cost the other families their rows.
func TestIntegrationOneImplausibleRowDoesNotFailTheBatch(t *testing.T) {
	srv, token, s := newFixture(t)
	ctx := context.Background()
	ts := time.Now().Add(-time.Minute).UnixMilli()
	poison := time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()

	req := fullRequest(1, ts)
	req.DiskIo = append(req.DiskIo, &netrav1.DiskIoSample{TsMs: poison, Device: "sdz"})

	if resp := post(t, srv, token, req); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var n int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM disk_io_samples WHERE device = 'sdz'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("the year-2100 row was stored (%d rows); it would outlive every retention policy", n)
	}

	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM disk_io_samples WHERE device = 'sda'`).Scan(&n); err != nil {
		t.Fatalf("count sda: %v", err)
	}
	if n != 1 {
		t.Errorf("sda rows = %d, want 1 -- one poison row must not cost the good ones", n)
	}
}

// A row Postgres itself rejects must not cost the other eleven families their
// data, and must not 503.
//
// This is the same failure mode maxBatchRows and the timestamp filter exist to
// prevent, arriving through a different door: a 503 makes the agent re-send
// the identical batch forever, so a single unstorable row wedges the ring
// buffer permanently. The timestamp filter only catches implausible ts_ms --
// a NUL byte in an event subject or an address INET will not parse are equally
// unstorable and equally permanent.
//
// Both quarantine paths are exercised on purpose. A NUL in an event subject
// fails inside the family's own INSERT batch; a NUL in a container_key fails
// EARLIER, in the dimension resolver that maps natural keys to ids, which the
// batch quarantine never sees.
func TestIntegrationOneUnstorableRowDoesNotCostTheOtherFamiliesTheirData(t *testing.T) {
	srv, token, s := newFixture(t)
	ctx := context.Background()
	ts := time.Now().Add(-time.Minute).UnixMilli()

	req := fullRequest(1, ts)
	// Batch path: SQLSTATE 22021, invalid byte sequence for encoding UTF8.
	req.Events = append(req.Events, &netrav1.Event{
		TsMs: ts, Type: "mdraid", Subject: "ev\x00il", DetailJson: `{"state":"clean"}`,
	})
	// Resolver path: the same NUL, but in a natural key the hub must resolve
	// to a surrogate id before any sample can reference it. Every dimension is
	// poisoned, because each resolver is its own code path and a quarantine
	// that covered only some of them would still wedge on the rest.
	req.Containers = append(req.Containers, &netrav1.ContainerSample{
		TsMs: ts, ContainerKey: "proj/ev\x00il", Name: "evil", CpuPct: proto.Float64(1),
	})
	req.Sensors = append(req.Sensors, &netrav1.SensorSample{
		TsMs: ts, Chip: "core\x00temp", Label: "evil", Temp: proto.Float64(50),
	})
	req.Filesystems = append(req.Filesystems, &netrav1.FilesystemSample{
		TsMs: ts, Label: "/ev\x00il", Mountpoint: "/evil", Total: proto.Uint64(1),
	})
	req.Smart = append(req.Smart, &netrav1.SmartAttribute{
		TsMs: ts, Device: "sd\x00z", AttrId: 5, Raw: proto.Int64(0),
	})
	req.SystemdEvents = append(req.SystemdEvents, &netrav1.SystemdUnitEvent{
		TsMs: ts, UnitName: "ev\x00il.service", State: "failed", Substate: "failed",
	})
	// Batch path, different SQLSTATE: 22P02, invalid input syntax for inet.
	req.Addresses = append(req.Addresses, &netrav1.HostAddress{
		Iface: "eth0", Address: "not-an-address", Family: 4,
	})

	resp := post(t, srv, token, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 -- a 503 here makes the agent re-send this batch forever", resp.StatusCode)
	}

	// Every family still lands, including the good rows of the three families
	// that carried a poison row.
	for _, table := range familyTables {
		var n int
		if err := s.Pool().QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if n == 0 {
			t.Errorf("%s is empty; one unstorable row cost an unrelated family its data", table)
		}
	}

	// The poisoned rows themselves are dropped, not stored mangled. Every
	// dimension keeps exactly the one good row fullRequest carries.
	for _, c := range []struct{ what, query string }{
		{"event", `SELECT count(*) FROM events WHERE subject LIKE 'ev%il'`},
		{"container", `SELECT count(*) FROM containers WHERE container_key LIKE 'proj/ev%'`},
		{"address", `SELECT count(*) FROM host_addresses WHERE host(address) = 'not-an-address'`},
		{"sensor", `SELECT count(*) FROM sensors WHERE label = 'evil'`},
		{"filesystem", `SELECT count(*) FROM filesystems WHERE mountpoint = '/evil'`},
		{"device", `SELECT count(*) FROM devices WHERE device LIKE 'sd_z'`},
		{"systemd unit", `SELECT count(*) FROM systemd_units WHERE unit_name LIKE 'ev%'`},
	} {
		var n int
		if err := s.Pool().QueryRow(ctx, c.query).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", c.what, err)
		}
		if n != 0 {
			t.Errorf("%s: %d poison rows stored, want 0", c.what, n)
		}
	}

	// And the good row of each poisoned family survived its quarantine.
	for _, c := range []struct{ what, query string }{
		{"event", `SELECT count(*) FROM events WHERE subject = 'md0'`},
		{"container", `SELECT count(*) FROM containers WHERE container_key = 'proj/web'`},
		{"address", `SELECT count(*) FROM host_addresses WHERE host(address) = '10.0.0.5'`},
	} {
		var n int
		if err := s.Pool().QueryRow(ctx, c.query).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", c.what, err)
		}
		if n != 1 {
			t.Errorf("%s: good rows = %d, want 1 -- quarantine dropped more than the poison row", c.what, n)
		}
	}
}

// A failure that is NOT a poison row must still 503, because a retry is how
// the agent recovers from one.
//
// A column the statement names but the schema does not have is SQLSTATE class
// 42, which is permanent like a poison row but is the HUB's bug rather than
// the agent's. Quarantining it would drop every row of that family one by one
// and answer 200, turning a schema mistake into silent, fleet-wide data loss.
// It stays a 503 an operator can see.
//
// A renamed COLUMN rather than a dropped table, deliberately. events is a
// hypertable: dropping it disturbs TimescaleDB's own catalog and orphans the
// retention and continuous-aggregate jobs the migration registers, which then
// fire against a later test's freshly recreated schema and deadlock against
// whatever it is doing. A rename produces the same SQLSTATE with no catalog
// damage at all.
func TestIntegrationANonPoisonFailureStill503s(t *testing.T) {
	srv, token, s := newFixture(t)
	ctx := context.Background()
	ts := time.Now().Add(-time.Minute).UnixMilli()

	if _, err := s.Pool().Exec(ctx,
		`ALTER TABLE events RENAME COLUMN detail TO detail_gone`); err != nil {
		t.Fatalf("rename events column: %v", err)
	}

	resp := post(t, srv, token, fullRequest(1, ts))
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 -- only rows Postgres rejects identically forever may be quarantined",
			resp.StatusCode)
	}
}

// A package the host no longer has installed must go, and the multiarch case
// must survive the pruning.
//
// "apt remove nginx" writes a remove row to package_events. An inventory that
// still lists nginx contradicts it, and the two halves of the same answer
// disagreeing is worse than either being absent.
func TestIntegrationPackagesNoLongerInstalledArePruned(t *testing.T) {
	srv, token, s := newFixture(t)
	ctx := context.Background()

	req := fullRequest(1, time.Now().Add(-2*time.Minute).UnixMilli())
	req.Packages = []*netrav1.HostPackage{
		{Name: "bash", Version: "5.3", Arch: "amd64", Format: "dpkg"},
		{Name: "nginx", Version: "1.24", Arch: "amd64", Format: "dpkg"},
		// The same package for a second architecture is a SEPARATE
		// installation with its own version, so pruning on name alone would
		// delete one of these every time the other was reported.
		{Name: "zlib1g", Version: "1.3", Arch: "amd64", Format: "dpkg"},
		{Name: "zlib1g", Version: "1.3", Arch: "i386", Format: "dpkg"},
	}
	if resp := post(t, srv, token, req); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	// The package is removed, and the next inventory omits it.
	req = fullRequest(2, time.Now().Add(-time.Minute).UnixMilli())
	req.Packages = []*netrav1.HostPackage{
		{Name: "bash", Version: "5.3", Arch: "amd64", Format: "dpkg"},
		{Name: "zlib1g", Version: "1.3", Arch: "amd64", Format: "dpkg"},
		{Name: "zlib1g", Version: "1.3", Arch: "i386", Format: "dpkg"},
	}
	if resp := post(t, srv, token, req); resp.StatusCode != http.StatusOK {
		t.Fatalf("second status = %d, want 200", resp.StatusCode)
	}

	var n int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM host_packages WHERE name = 'nginx'`).Scan(&n); err != nil {
		t.Fatalf("count nginx: %v", err)
	}
	if n != 0 {
		t.Errorf("nginx rows = %d, want 0 -- a removed package must not stay in the inventory", n)
	}

	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM host_packages WHERE name = 'zlib1g'`).Scan(&n); err != nil {
		t.Fatalf("count zlib1g: %v", err)
	}
	if n != 2 {
		t.Errorf("zlib1g rows = %d, want 2 -- amd64 and i386 are separate installations", n)
	}

	if err := s.Pool().QueryRow(ctx, `SELECT count(*) FROM host_packages`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 3 {
		t.Errorf("host_packages = %d, want 3", n)
	}
}

// An empty package set means "the agent did not re-parse", NOT "this host has
// no packages", so it must never prune.
//
// The collector parses only when the database mtime moved or the daily floor
// elapsed, and sends nothing otherwise -- so most scrapes carry no inventory
// at all. Treating those as an empty set would wipe the whole inventory on
// every ordinary scrape.
func TestIntegrationAnAbsentPackageSetDoesNotPruneTheInventory(t *testing.T) {
	srv, token, s := newFixture(t)
	ctx := context.Background()

	req := fullRequest(1, time.Now().Add(-2*time.Minute).UnixMilli())
	req.Packages = []*netrav1.HostPackage{
		{Name: "bash", Version: "5.3", Arch: "amd64", Format: "dpkg"},
		{Name: "nginx", Version: "1.24", Arch: "amd64", Format: "dpkg"},
	}
	if resp := post(t, srv, token, req); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	// The ordinary scrape: nothing re-parsed, so no inventory rides along.
	req = fullRequest(2, time.Now().Add(-time.Minute).UnixMilli())
	req.Packages = nil
	if resp := post(t, srv, token, req); resp.StatusCode != http.StatusOK {
		t.Fatalf("second status = %d, want 200", resp.StatusCode)
	}

	var n int
	if err := s.Pool().QueryRow(ctx, `SELECT count(*) FROM host_packages`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Errorf("host_packages = %d, want 2 -- a scrape that did not re-parse must not prune", n)
	}
}

// A poison value in an INVENTORY set also poisons the prune that follows it,
// because the deleted-set comparison carries the same value. Skipping that
// prune must not 503.
//
// A stale row outliving its address is a wrong answer to a subnet query, which
// is bad. A 503 is a permanently wedged agent, which is worse -- and the next
// scrape prunes correctly anyway, since by then the quarantine has dropped the
// offending row from the set the agent keeps sending.
func TestIntegrationAPoisonInventoryValueDoesNotWedgeThePrune(t *testing.T) {
	srv, token, s := newFixture(t)
	ctx := context.Background()
	ts := time.Now().Add(-time.Minute).UnixMilli()

	req := fullRequest(1, ts)
	req.Addresses = []*netrav1.HostAddress{
		{Iface: "eth0", Address: "10.0.0.5", Family: 4},
		{Iface: "et\x00h0", Address: "10.0.0.9", Family: 4},
	}
	req.Packages = []*netrav1.HostPackage{
		{Name: "bash", Version: "5.3", Arch: "amd64", Format: "dpkg"},
		{Name: "ba\x00d", Version: "1.0", Arch: "amd64", Format: "dpkg"},
	}

	if resp := post(t, srv, token, req); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 -- a poisoned prune must not wedge the agent", resp.StatusCode)
	}

	// The good half of each inventory still landed.
	var n int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM host_addresses WHERE host(address) = '10.0.0.5'`).Scan(&n); err != nil {
		t.Fatalf("count address: %v", err)
	}
	if n != 1 {
		t.Errorf("good address rows = %d, want 1", n)
	}
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM host_packages WHERE name = 'bash'`).Scan(&n); err != nil {
		t.Fatalf("count package: %v", err)
	}
	if n != 1 {
		t.Errorf("good package rows = %d, want 1", n)
	}
}

// A non-poison failure inside a DIMENSION RESOLVER must 503 too.
//
// The resolvers run before the family's own INSERT, so they are a second place
// a failure can arise -- and the one the batch quarantine cannot see. Each is
// its own code path, so each is checked: a resolver that swallowed a real
// database failure would report 200 and silently drop every sample keyed on
// that dimension.
func TestIntegrationANonPoisonResolverFailureStill503s(t *testing.T) {
	// A renamed column rather than a dropped table, for the reason spelled out
	// on TestIntegrationANonPoisonFailureStill503s: dropping these CASCADEs
	// into the hypertables that reference them and leaves TimescaleDB's
	// background jobs pointing at a schema that no longer matches.
	for _, d := range []struct{ table, column string }{
		{"sensors", "chip"},
		{"containers", "container_key"},
		{"filesystems", "mountpoint"},
		{"devices", "model"},
		{"systemd_units", "unit_name"},
	} {
		t.Run(d.table, func(t *testing.T) {
			srv, token, s := newFixture(t)
			ctx := context.Background()
			ts := time.Now().Add(-time.Minute).UnixMilli()

			if _, err := s.Pool().Exec(ctx,
				`ALTER TABLE `+d.table+` RENAME COLUMN `+d.column+` TO `+d.column+`_gone`); err != nil {
				t.Fatalf("rename %s.%s: %v", d.table, d.column, err)
			}

			resp := post(t, srv, token, fullRequest(1, ts))
			if resp.StatusCode != http.StatusServiceUnavailable {
				t.Errorf("status = %d, want 503 -- a %s column the hub names but the schema lacks is the hub's bug, not a poison row",
					resp.StatusCode, d.table)
			}
		})
	}
}

// A poison natural key must be attempted ONCE per batch, not once per row.
//
// The dedupe guard used to be "is this key already in the resolved map", which
// a poison key never enters -- so every row carrying it issued another failed
// round trip and another warning. A host reporting many samples for one
// unstorable unit name would make that many failed queries on every ingest,
// and keep doing it, because the agent re-sends the batch it was acknowledged
// for.
func TestIntegrationAPoisonNaturalKeyIsResolvedOncePerBatch(t *testing.T) {
	srv, token, s := newFixture(t)
	ctx := context.Background()
	ts := time.Now().Add(-time.Minute).UnixMilli()

	req := fullRequest(1, ts)

	// Forty rows sharing one poison key, across the dimensions that resolve
	// natural keys to ids.
	for i := range 10 {
		at := ts + int64(i)
		req.Containers = append(req.Containers, &netrav1.ContainerSample{
			TsMs: at, ContainerKey: "proj/ev\x00il", Name: "evil", CpuPct: proto.Float64(1),
		})
		req.Filesystems = append(req.Filesystems, &netrav1.FilesystemSample{
			TsMs: at, Label: "/ev\x00il", Mountpoint: "/evil", Total: proto.Uint64(1),
		})
		req.Smart = append(req.Smart, &netrav1.SmartAttribute{
			TsMs: at, Device: "sd\x00z", AttrId: 5, Raw: proto.Int64(0),
		})
		req.SystemdEvents = append(req.SystemdEvents, &netrav1.SystemdUnitEvent{
			TsMs: at, UnitName: "ev\x00il.service", State: "failed", Substate: "failed",
		})
	}

	if resp := post(t, srv, token, req); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	// The good rows still landed, and none of the poison keys created a row.
	for _, c := range []struct{ what, query string }{
		{"container", `SELECT count(*) FROM containers WHERE container_key LIKE 'proj/ev%'`},
		{"filesystem", `SELECT count(*) FROM filesystems WHERE mountpoint = '/evil'`},
		{"device", `SELECT count(*) FROM devices WHERE device LIKE 'sd_z'`},
		{"systemd unit", `SELECT count(*) FROM systemd_units WHERE unit_name LIKE 'ev%'`},
	} {
		var n int
		if err := s.Pool().QueryRow(ctx, c.query).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", c.what, err)
		}
		if n != 0 {
			t.Errorf("%s: %d poison rows stored, want 0", c.what, n)
		}
	}

	// A replay of the same batch must still be absorbed rather than 503.
	if resp := post(t, srv, token, req); resp.StatusCode != http.StatusOK {
		t.Errorf("replay status = %d, want 200", resp.StatusCode)
	}
}

// A snapshot stamped in the future is dropped WHOLE, and the units it would
// have touched are left exactly as they were.
//
// Unlike a poison sample row, which costs one row, a poison snapshot timestamp
// is a permanent wedge: state_ts is stored and every later write is guarded by
// `ts > state_ts`, so a year-2100 snapshot would make this host's unit states
// unwritable until real time caught up. That is precisely the failure the
// timestamp filter exists to prevent, so the snapshot goes through the same
// gate as the sample families -- but all-or-nothing, because a snapshot is one
// statement about a whole host rather than a bag of independent rows.
func TestIntegrationImplausibleSystemdSnapshotIsDroppedWhole(t *testing.T) {
	srv, token, s := newFixture(t)
	ctx := context.Background()
	ts := time.Now().Add(-time.Minute).UnixMilli()

	// A host already tracking a failed unit.
	req := fullRequest(1, ts)
	req.SystemdSnapshot = &netrav1.SystemdSnapshot{
		TsMs:     ts,
		Complete: true,
		Units: []*netrav1.SystemdUnitState{
			{UnitName: "exim4.service", State: "failed", Substate: "failed"},
		},
	}
	if resp := post(t, srv, token, req); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	// The same host, a snapshot from the year 2100 claiming all is well.
	poison := time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	req = fullRequest(2, time.Now().UnixMilli())
	req.SystemdSnapshot = &netrav1.SystemdSnapshot{
		TsMs:     poison,
		Complete: true,
		Units: []*netrav1.SystemdUnitState{
			{UnitName: "ssh.service", State: "active", Substate: "running"},
		},
	}
	if resp := post(t, srv, token, req); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 -- a poison snapshot must not 503 the batch", resp.StatusCode)
	}

	var state string
	var stateTs time.Time
	if err := s.Pool().QueryRow(ctx,
		`SELECT state, state_ts FROM systemd_units WHERE unit_name = 'exim4.service'`).
		Scan(&state, &stateTs); err != nil {
		t.Fatalf("read unit: %v", err)
	}
	if state != "failed" {
		t.Errorf("exim4.service state = %q, want failed -- the future snapshot must not "+
			"clear a warning, and it must not delete the unit either", state)
	}
	if stateTs.After(time.Now().Add(time.Hour)) {
		t.Errorf("state_ts = %v is in the future; every later update would be skipped "+
			"until real time caught up, freezing this host's units", stateTs)
	}
}
