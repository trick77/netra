package httpapi_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
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
		Processes: []*netrav1.ProcessSample{
			{TsMs: ts, Name: "postgres", CpuPct: proto.Float64(12)},
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
	"process_samples", "collector_samples", "events",
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
