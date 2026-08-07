package netrav1_test

import (
	"testing"

	"google.golang.org/protobuf/proto"

	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
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
