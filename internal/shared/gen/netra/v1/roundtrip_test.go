package netrav1_test

import (
	"testing"

	"google.golang.org/protobuf/proto"

	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
)

// The absent-vs-zero distinction is the whole reason these fields are
// optional. Losing it silently turns "this host has no swap" into
// "this host has 0 bytes of swap used".
func TestOptionalFieldsPreserveAbsentVersusZero(t *testing.T) {
	in := &netrav1.HostSample{
		TsMs:     1_700_000_000_000,
		SwapUsed: proto.Uint64(0), // present, and zero
		// MemZfsArc deliberately left unset: absent
	}

	raw, err := proto.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var out netrav1.HostSample
	if err := proto.Unmarshal(raw, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if out.SwapUsed == nil {
		t.Fatal("SwapUsed round-tripped as absent, want present with value 0")
	}
	if *out.SwapUsed != 0 {
		t.Fatalf("SwapUsed = %d, want 0", *out.SwapUsed)
	}
	if out.MemZfsArc != nil {
		t.Fatalf("MemZfsArc = %d, want absent", *out.MemZfsArc)
	}
}

func TestIngestRequestRoundTrip(t *testing.T) {
	in := &netrav1.IngestRequest{
		Seq:          42,
		MetadataHash: []byte{1, 2, 3, 4, 5, 6, 7, 8},
		Backfill:     true,
		HostSamples: []*netrav1.HostSample{
			{TsMs: 1_700_000_000_000, CpuTotal: proto.Float64(12.5)},
		},
	}

	raw, err := proto.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var out netrav1.IngestRequest
	if err := proto.Unmarshal(raw, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if out.Seq != 42 {
		t.Fatalf("Seq = %d, want 42", out.Seq)
	}
	if !out.Backfill {
		t.Fatal("Backfill = false, want true")
	}
	if len(out.HostSamples) != 1 {
		t.Fatalf("len(HostSamples) = %d, want 1", len(out.HostSamples))
	}
	if got := out.HostSamples[0].GetCpuTotal(); got != 12.5 {
		t.Fatalf("CpuTotal = %v, want 12.5", got)
	}
}

// A per-entity family rides on IngestRequest, not on HostSample: a request
// carries a batch of scrapes, so these rows span several timestamps and carry
// their own ts_ms rather than being positionally tied to a host row.
func TestCpuCoreSampleRoundTrip(t *testing.T) {
	in := &netrav1.IngestRequest{
		Seq: 7,
		CpuCores: []*netrav1.CpuCoreSample{
			{TsMs: 1000, Core: 0, Busy: proto.Float64(41.5)},
			{TsMs: 1000, Core: 1},
		},
	}

	raw, err := proto.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var out netrav1.IngestRequest
	if err := proto.Unmarshal(raw, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if len(out.GetCpuCores()) != 2 {
		t.Fatalf("len(CpuCores) = %d, want 2", len(out.GetCpuCores()))
	}
	if got := out.GetCpuCores()[0].GetBusy(); got != 41.5 {
		t.Errorf("core 0 Busy = %v, want 41.5", got)
	}
	if got := out.GetCpuCores()[0].GetCore(); got != 0 {
		t.Errorf("core 0 Core = %d, want 0", got)
	}
	// An unset busy must stay unset: "core present, utilisation not
	// computable" is a different fact from "core was 0% busy".
	if out.GetCpuCores()[1].Busy != nil {
		t.Error("core 1 Busy is set; an unmeasured value must stay nil")
	}
}

// Every per-entity family carries its own ts_ms. A family that inherited a
// host row's timestamp would be wrong the moment a replayed batch spanned more
// than one scrape, which is the normal case after an outage.
func TestEveryPerEntityFamilyCarriesItsOwnTimestamp(t *testing.T) {
	in := &netrav1.IngestRequest{
		Seq:           1,
		CpuCores:      []*netrav1.CpuCoreSample{{TsMs: 10, Core: 0}},
		DiskIo:        []*netrav1.DiskIoSample{{TsMs: 20, Device: "sda"}},
		Sensors:       []*netrav1.SensorSample{{TsMs: 30, Chip: "coretemp", Label: "Package id 0"}},
		Net:           []*netrav1.NetSample{{TsMs: 40, Iface: "eth0"}},
		Collectors:    []*netrav1.CollectorSample{{TsMs: 50, Collector: "sensors", Ok: true}},
		Events:        []*netrav1.Event{{TsMs: 60, Type: "mdraid", Subject: "md0"}},
		Containers:    []*netrav1.ContainerSample{{TsMs: 70, ContainerKey: "proj/svc"}},
		Filesystems:   []*netrav1.FilesystemSample{{TsMs: 80, Label: "root"}},
		Smart:         []*netrav1.SmartAttribute{{TsMs: 90, Device: "sda", AttrId: 5}},
		SystemdEvents: []*netrav1.SystemdUnitEvent{{TsMs: 110, UnitName: "ssh.service", State: "failed"}},
		PackageEvents: []*netrav1.PackageEvent{{TsMs: 120, Name: "bash", Action: "upgrade"}},
	}

	raw, err := proto.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var out netrav1.IngestRequest
	if err := proto.Unmarshal(raw, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	for name, ts := range map[string]int64{
		"cpu_cores":      out.GetCpuCores()[0].GetTsMs(),
		"disk_io":        out.GetDiskIo()[0].GetTsMs(),
		"sensors":        out.GetSensors()[0].GetTsMs(),
		"net":            out.GetNet()[0].GetTsMs(),
		"collectors":     out.GetCollectors()[0].GetTsMs(),
		"events":         out.GetEvents()[0].GetTsMs(),
		"containers":     out.GetContainers()[0].GetTsMs(),
		"filesystems":    out.GetFilesystems()[0].GetTsMs(),
		"smart":          out.GetSmart()[0].GetTsMs(),
		"systemd_events": out.GetSystemdEvents()[0].GetTsMs(),
		"package_events": out.GetPackageEvents()[0].GetTsMs(),
	} {
		if ts == 0 {
			t.Errorf("%s row lost its ts_ms in the round trip", name)
		}
	}

	// Inventory carries no timestamp by design: addresses and packages describe
	// what the host HAS, reported on change rather than measured per scrape.
	// The hub stamps first_seen and last_seen itself.
	if len(out.GetAddresses()) != 0 || len(out.GetPackages()) != 0 {
		t.Error("inventory unexpectedly populated; this case sends none")
	}
}
