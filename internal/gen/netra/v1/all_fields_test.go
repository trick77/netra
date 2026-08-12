package netrav1_test

import (
	"testing"

	"google.golang.org/protobuf/proto"

	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

// TestHostSampleAllFieldsSet exercises every optional field's getter and the
// marshal/unmarshal path for each scalar type HostSample carries (double,
// uint64), rather than just the one or two fields the other round-trip tests
// touch. Every metric is optional per the schema comment in ingest.proto, so
// each one needs its own present-with-value proof, not just the absent case
// covered by TestOptionalFieldsPreserveAbsentVersusZero.
func TestHostSampleAllFieldsSet(t *testing.T) {
	in := &netrav1.HostSample{
		TsMs:            1_700_000_000_123,
		CpuTotal:        proto.Float64(12.5),
		CpuUser:         proto.Float64(5.5),
		CpuSystem:       proto.Float64(3.5),
		CpuIowait:       proto.Float64(1.5),
		CpuSteal:        proto.Float64(0.5),
		CpuIdle:         proto.Float64(76.5),
		MemTotal:        proto.Uint64(16_000_000_000),
		MemUsed:         proto.Uint64(8_000_000_000),
		MemAvailable:    proto.Uint64(7_000_000_000),
		MemBuffcache:    proto.Uint64(1_000_000_000),
		MemZfsArc:       proto.Uint64(500_000_000),
		MemFree:         proto.Uint64(4_000_000_000),
		MemBuffers:      proto.Uint64(300_000_000),
		MemCached:       proto.Uint64(600_000_000),
		MemShared:       proto.Uint64(100_000_000),
		MemSreclaimable: proto.Uint64(200_000_000),
		SwapTotal:       proto.Uint64(2_000_000_000),
		SwapUsed:        proto.Uint64(100_000_000),
		Load1:           proto.Float64(0.1),
		Load5:           proto.Float64(0.2),
		Load15:          proto.Float64(0.3),
		UptimeS:         proto.Uint64(86_400),
	}

	raw, err := proto.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var out netrav1.HostSample
	if err := proto.Unmarshal(raw, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if out.TsMs != in.TsMs {
		t.Errorf("TsMs = %d, want %d", out.TsMs, in.TsMs)
	}
	if got := out.GetCpuTotal(); got != 12.5 {
		t.Errorf("CpuTotal = %v, want 12.5", got)
	}
	if got := out.GetCpuUser(); got != 5.5 {
		t.Errorf("CpuUser = %v, want 5.5", got)
	}
	if got := out.GetCpuSystem(); got != 3.5 {
		t.Errorf("CpuSystem = %v, want 3.5", got)
	}
	if got := out.GetCpuIowait(); got != 1.5 {
		t.Errorf("CpuIowait = %v, want 1.5", got)
	}
	if got := out.GetCpuSteal(); got != 0.5 {
		t.Errorf("CpuSteal = %v, want 0.5", got)
	}
	if got := out.GetCpuIdle(); got != 76.5 {
		t.Errorf("CpuIdle = %v, want 76.5", got)
	}
	if got := out.GetMemTotal(); got != 16_000_000_000 {
		t.Errorf("MemTotal = %v, want 16000000000", got)
	}
	if got := out.GetMemUsed(); got != 8_000_000_000 {
		t.Errorf("MemUsed = %v, want 8000000000", got)
	}
	if got := out.GetMemAvailable(); got != 7_000_000_000 {
		t.Errorf("MemAvailable = %v, want 7000000000", got)
	}
	if got := out.GetMemBuffcache(); got != 1_000_000_000 {
		t.Errorf("MemBuffcache = %v, want 1000000000", got)
	}
	if got := out.GetMemZfsArc(); got != 500_000_000 {
		t.Errorf("MemZfsArc = %v, want 500000000", got)
	}
	if got := out.GetMemFree(); got != 4_000_000_000 {
		t.Errorf("MemFree = %v, want 4000000000", got)
	}
	if got := out.GetMemBuffers(); got != 300_000_000 {
		t.Errorf("MemBuffers = %v, want 300000000", got)
	}
	if got := out.GetMemCached(); got != 600_000_000 {
		t.Errorf("MemCached = %v, want 600000000", got)
	}
	if got := out.GetMemShared(); got != 100_000_000 {
		t.Errorf("MemShared = %v, want 100000000", got)
	}
	if got := out.GetMemSreclaimable(); got != 200_000_000 {
		t.Errorf("MemSreclaimable = %v, want 200000000", got)
	}
	if got := out.GetSwapTotal(); got != 2_000_000_000 {
		t.Errorf("SwapTotal = %v, want 2000000000", got)
	}
	if got := out.GetSwapUsed(); got != 100_000_000 {
		t.Errorf("SwapUsed = %v, want 100000000", got)
	}
	if got := out.GetLoad1(); got != 0.1 {
		t.Errorf("Load1 = %v, want 0.1", got)
	}
	if got := out.GetLoad5(); got != 0.2 {
		t.Errorf("Load5 = %v, want 0.2", got)
	}
	if got := out.GetLoad15(); got != 0.3 {
		t.Errorf("Load15 = %v, want 0.3", got)
	}
	if got := out.GetUptimeS(); got != 86_400 {
		t.Errorf("UptimeS = %v, want 86400", got)
	}
}

// TestHostSampleAllFieldsAbsent proves every getter returns the proto3 zero
// value when its field was never set, distinguishing "field present but a
// nil receiver" handling (Get* on a nil *HostSample) from "field absent on a
// populated message" — both paths a caller can hit.
func TestHostSampleAllFieldsAbsent(t *testing.T) {
	var nilSample *netrav1.HostSample

	if got := nilSample.GetCpuTotal(); got != 0 {
		t.Errorf("nil.GetCpuTotal() = %v, want 0", got)
	}
	if got := nilSample.GetMemTotal(); got != 0 {
		t.Errorf("nil.GetMemTotal() = %v, want 0", got)
	}
	if got := nilSample.GetUptimeS(); got != 0 {
		t.Errorf("nil.GetUptimeS() = %v, want 0", got)
	}

	empty := &netrav1.HostSample{TsMs: 1}
	raw, err := proto.Marshal(empty)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var out netrav1.HostSample
	if err := proto.Unmarshal(raw, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	fields := map[string]bool{
		"CpuTotal":        out.CpuTotal != nil,
		"CpuUser":         out.CpuUser != nil,
		"CpuSystem":       out.CpuSystem != nil,
		"CpuIowait":       out.CpuIowait != nil,
		"CpuSteal":        out.CpuSteal != nil,
		"CpuIdle":         out.CpuIdle != nil,
		"MemTotal":        out.MemTotal != nil,
		"MemUsed":         out.MemUsed != nil,
		"MemAvailable":    out.MemAvailable != nil,
		"MemBuffcache":    out.MemBuffcache != nil,
		"MemZfsArc":       out.MemZfsArc != nil,
		"MemFree":         out.MemFree != nil,
		"MemBuffers":      out.MemBuffers != nil,
		"MemCached":       out.MemCached != nil,
		"MemShared":       out.MemShared != nil,
		"MemSreclaimable": out.MemSreclaimable != nil,
		"SwapTotal":       out.SwapTotal != nil,
		"SwapUsed":        out.SwapUsed != nil,
		"Load1":           out.Load1 != nil,
		"Load5":           out.Load5 != nil,
		"Load15":          out.Load15 != nil,
		"UptimeS":         out.UptimeS != nil,
	}
	for name, present := range fields {
		if present {
			t.Errorf("%s round-tripped as present, want absent", name)
		}
	}
}

// TestMetadataRoundTrip covers Metadata's own field set (strings and
// unsigned integers, none of them optional), which no other test in this
// package touches.
func TestMetadataRoundTrip(t *testing.T) {
	in := &netrav1.Metadata{
		AgentVersion: "1.2.3",
		GoVersion:    "go1.26",
		BuildCommit:  "deadbeef",
		Hostname:     "host-a",
		Kernel:       "6.9.0",
		OsName:       "linux",
		Arch:         "amd64",
		CpuModel:     "Ryzen",
		Cores:        8,
		Threads:      16,
		MemoryTotal:  34_000_000_000,
		Location:     "fra1",
		Provider:     "hetzner",
		Facility:     "fsn1-dc14",
		HostType:     "cx41",
		Fingerprint:  "abc123",
	}

	raw, err := proto.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var out netrav1.Metadata
	if err := proto.Unmarshal(raw, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	switch {
	case out.GetAgentVersion() != in.AgentVersion:
		t.Errorf("AgentVersion = %q, want %q", out.GetAgentVersion(), in.AgentVersion)
	case out.GetGoVersion() != in.GoVersion:
		t.Errorf("GoVersion = %q, want %q", out.GetGoVersion(), in.GoVersion)
	case out.GetBuildCommit() != in.BuildCommit:
		t.Errorf("BuildCommit = %q, want %q", out.GetBuildCommit(), in.BuildCommit)
	case out.GetHostname() != in.Hostname:
		t.Errorf("Hostname = %q, want %q", out.GetHostname(), in.Hostname)
	case out.GetKernel() != in.Kernel:
		t.Errorf("Kernel = %q, want %q", out.GetKernel(), in.Kernel)
	case out.GetOsName() != in.OsName:
		t.Errorf("OsName = %q, want %q", out.GetOsName(), in.OsName)
	case out.GetArch() != in.Arch:
		t.Errorf("Arch = %q, want %q", out.GetArch(), in.Arch)
	case out.GetCpuModel() != in.CpuModel:
		t.Errorf("CpuModel = %q, want %q", out.GetCpuModel(), in.CpuModel)
	case out.GetCores() != in.Cores:
		t.Errorf("Cores = %d, want %d", out.GetCores(), in.Cores)
	case out.GetThreads() != in.Threads:
		t.Errorf("Threads = %d, want %d", out.GetThreads(), in.Threads)
	case out.GetMemoryTotal() != in.MemoryTotal:
		t.Errorf("MemoryTotal = %d, want %d", out.GetMemoryTotal(), in.MemoryTotal)
	case out.GetLocation() != in.Location:
		t.Errorf("Location = %q, want %q", out.GetLocation(), in.Location)
	case out.GetProvider() != in.Provider:
		t.Errorf("Provider = %q, want %q", out.GetProvider(), in.Provider)
	case out.GetFacility() != in.Facility:
		t.Errorf("Facility = %q, want %q", out.GetFacility(), in.Facility)
	case out.GetHostType() != in.HostType:
		t.Errorf("HostType = %q, want %q", out.GetHostType(), in.HostType)
	case out.GetFingerprint() != in.Fingerprint:
		t.Errorf("Fingerprint = %q, want %q", out.GetFingerprint(), in.Fingerprint)
	}
}

// TestIngestRequestWithMetadataRoundTrip covers the nested Metadata field on
// IngestRequest, sent only when the hub previously asked for a resend — the
// other IngestRequest test in this package leaves it nil.
func TestIngestRequestWithMetadataRoundTrip(t *testing.T) {
	in := &netrav1.IngestRequest{
		Seq:          7,
		MetadataHash: []byte{9, 9, 9},
		Metadata: &netrav1.Metadata{
			AgentVersion: "1.0.0",
			Hostname:     "host-b",
		},
		Backfill: false,
	}

	raw, err := proto.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var out netrav1.IngestRequest
	if err := proto.Unmarshal(raw, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if out.GetMetadata() == nil {
		t.Fatal("Metadata round-tripped as nil, want present")
	}
	if got := out.GetMetadata().GetAgentVersion(); got != "1.0.0" {
		t.Errorf("Metadata.AgentVersion = %q, want %q", got, "1.0.0")
	}
	if got := out.GetMetadata().GetHostname(); got != "host-b" {
		t.Errorf("Metadata.Hostname = %q, want %q", got, "host-b")
	}
	if out.GetBackfill() {
		t.Error("Backfill = true, want false")
	}
}

// TestIngestResponseRoundTrip covers IngestResponse, untouched by every other
// test in this package — it is what the hub sends back, not what the agent sends.
func TestIngestResponseRoundTrip(t *testing.T) {
	in := &netrav1.IngestResponse{
		AckSeq:          99,
		RequestMetadata: true,
		RetryAfterS:     30,
	}

	raw, err := proto.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var out netrav1.IngestResponse
	if err := proto.Unmarshal(raw, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if out.GetAckSeq() != 99 {
		t.Errorf("AckSeq = %d, want 99", out.GetAckSeq())
	}
	if !out.GetRequestMetadata() {
		t.Error("RequestMetadata = false, want true")
	}
	if out.GetRetryAfterS() != 30 {
		t.Errorf("RetryAfterS = %d, want 30", out.GetRetryAfterS())
	}
}

// TestHostSampleStringAndReset exercise the generated String() and Reset()
// methods, both boilerplate but both reachable and part of the proto.Message
// contract this package promises its callers.
func TestHostSampleStringAndReset(t *testing.T) {
	s := &netrav1.HostSample{TsMs: 1, CpuTotal: proto.Float64(1.5)}
	if s.String() == "" {
		t.Error("String() returned empty string for a populated message")
	}
	s.Reset()
	if s.TsMs != 0 || s.CpuTotal != nil {
		t.Error("Reset() did not clear fields")
	}
}
