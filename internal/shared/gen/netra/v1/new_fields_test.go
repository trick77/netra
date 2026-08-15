package netrav1_test

import (
	"testing"

	"google.golang.org/protobuf/proto"

	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
)

// TestHostSampleNewFieldsSet is TestHostSampleAllFieldsSet for the kernel,
// network, process and session fields: every one marshalled, unmarshalled and
// read back through its getter, with a distinct value so a field that came
// back holding its neighbour's value is a failure rather than a coincidence.
func TestHostSampleNewFieldsSet(t *testing.T) {
	in := &netrav1.HostSample{
		TsMs: 1_700_000_000_123,

		CtxtPerS:     proto.Float64(1.5),
		IntrPerS:     proto.Float64(2.5),
		ForksPerS:    proto.Float64(3.5),
		ProcsRunning: proto.Uint32(4),
		ProcsBlocked: proto.Uint32(5),
		BootTimeS:    proto.Uint64(1_699_000_000),

		ProcessesTotal: proto.Uint32(6),
		UsersLoggedIn:  proto.Uint32(7),
		ServicesTotal:  proto.Uint32(8),
		ServicesFailed: proto.Uint32(9),

		TcpRetransSegsPerS:     proto.Float64(10.5),
		TcpOutRstsPerS:         proto.Float64(11.5),
		TcpInErrsPerS:          proto.Float64(12.5),
		TcpActiveOpensPerS:     proto.Float64(13.5),
		TcpPassiveOpensPerS:    proto.Float64(14.5),
		TcpAttemptFailsPerS:    proto.Float64(15.5),
		TcpCurrEstab:           proto.Uint32(16),
		TcpListenOverflowsPerS: proto.Float64(17.5),
		TcpListenDropsPerS:     proto.Float64(18.5),

		UdpInErrorsPerS:     proto.Float64(19.5),
		UdpRcvbufErrorsPerS: proto.Float64(20.5),
		UdpSndbufErrorsPerS: proto.Float64(21.5),
		UdpNoPortsPerS:      proto.Float64(22.5),

		IpReasmReqdsPerS:  proto.Float64(23.5),
		IpReasmFailsPerS:  proto.Float64(24.5),
		IpFragFailsPerS:   proto.Float64(25.5),
		IpFragCreatesPerS: proto.Float64(26.5),

		Udp6InErrorsPerS:     proto.Float64(27.5),
		Udp6RcvbufErrorsPerS: proto.Float64(28.5),
		Udp6SndbufErrorsPerS: proto.Float64(29.5),
		Udp6NoPortsPerS:      proto.Float64(30.5),

		Ip6ReasmReqdsPerS:  proto.Float64(31.5),
		Ip6ReasmFailsPerS:  proto.Float64(32.5),
		Ip6FragFailsPerS:   proto.Float64(33.5),
		Ip6FragCreatesPerS: proto.Float64(34.5),
	}

	raw, err := proto.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var out netrav1.HostSample
	if err := proto.Unmarshal(raw, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	for _, tc := range []struct {
		name string
		got  float64
		want float64
	}{
		{"CtxtPerS", out.GetCtxtPerS(), 1.5},
		{"IntrPerS", out.GetIntrPerS(), 2.5},
		{"ForksPerS", out.GetForksPerS(), 3.5},
		{"TcpRetransSegsPerS", out.GetTcpRetransSegsPerS(), 10.5},
		{"TcpOutRstsPerS", out.GetTcpOutRstsPerS(), 11.5},
		{"TcpInErrsPerS", out.GetTcpInErrsPerS(), 12.5},
		{"TcpActiveOpensPerS", out.GetTcpActiveOpensPerS(), 13.5},
		{"TcpPassiveOpensPerS", out.GetTcpPassiveOpensPerS(), 14.5},
		{"TcpAttemptFailsPerS", out.GetTcpAttemptFailsPerS(), 15.5},
		{"TcpListenOverflowsPerS", out.GetTcpListenOverflowsPerS(), 17.5},
		{"TcpListenDropsPerS", out.GetTcpListenDropsPerS(), 18.5},
		{"UdpInErrorsPerS", out.GetUdpInErrorsPerS(), 19.5},
		{"UdpRcvbufErrorsPerS", out.GetUdpRcvbufErrorsPerS(), 20.5},
		{"UdpSndbufErrorsPerS", out.GetUdpSndbufErrorsPerS(), 21.5},
		{"UdpNoPortsPerS", out.GetUdpNoPortsPerS(), 22.5},
		{"IpReasmReqdsPerS", out.GetIpReasmReqdsPerS(), 23.5},
		{"IpReasmFailsPerS", out.GetIpReasmFailsPerS(), 24.5},
		{"IpFragFailsPerS", out.GetIpFragFailsPerS(), 25.5},
		{"IpFragCreatesPerS", out.GetIpFragCreatesPerS(), 26.5},
		{"Udp6InErrorsPerS", out.GetUdp6InErrorsPerS(), 27.5},
		{"Udp6RcvbufErrorsPerS", out.GetUdp6RcvbufErrorsPerS(), 28.5},
		{"Udp6SndbufErrorsPerS", out.GetUdp6SndbufErrorsPerS(), 29.5},
		{"Udp6NoPortsPerS", out.GetUdp6NoPortsPerS(), 30.5},
		{"Ip6ReasmReqdsPerS", out.GetIp6ReasmReqdsPerS(), 31.5},
		{"Ip6ReasmFailsPerS", out.GetIp6ReasmFailsPerS(), 32.5},
		{"Ip6FragFailsPerS", out.GetIp6FragFailsPerS(), 33.5},
		{"Ip6FragCreatesPerS", out.GetIp6FragCreatesPerS(), 34.5},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %v, want %v", tc.name, tc.got, tc.want)
		}
	}

	for _, tc := range []struct {
		name string
		got  uint32
		want uint32
	}{
		{"ProcsRunning", out.GetProcsRunning(), 4},
		{"ProcsBlocked", out.GetProcsBlocked(), 5},
		{"ProcessesTotal", out.GetProcessesTotal(), 6},
		{"UsersLoggedIn", out.GetUsersLoggedIn(), 7},
		{"ServicesTotal", out.GetServicesTotal(), 8},
		{"ServicesFailed", out.GetServicesFailed(), 9},
		{"TcpCurrEstab", out.GetTcpCurrEstab(), 16},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %v, want %v", tc.name, tc.got, tc.want)
		}
	}

	if got := out.GetBootTimeS(); got != 1_699_000_000 {
		t.Errorf("BootTimeS = %v, want 1699000000", got)
	}
}

// The absent-vs-zero distinction matters more for these fields than for most:
// "no IPv6 on this host" and "zero IPv6 fragmentation failures" are different
// facts, and so are "not confined to a PID namespace" and "zero processes".
func TestHostSampleNewFieldsPreserveAbsentVersusZero(t *testing.T) {
	in := &netrav1.HostSample{
		TsMs: 1_700_000_000_000,
		// Read, and had not moved.
		UdpInErrorsPerS: proto.Float64(0),
		// Nobody logged in, and utmp WAS readable.
		UsersLoggedIn: proto.Uint32(0),
		// Udp6InErrorsPerS and ProcessesTotal deliberately unset.
	}

	raw, err := proto.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var out netrav1.HostSample
	if err := proto.Unmarshal(raw, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if out.UdpInErrorsPerS == nil {
		t.Error("UdpInErrorsPerS round-tripped as absent, want present with value 0")
	}
	if out.UsersLoggedIn == nil {
		t.Error("UsersLoggedIn round-tripped as absent, want present with value 0")
	}
	if out.Udp6InErrorsPerS != nil {
		t.Errorf("Udp6InErrorsPerS = %v, want absent", *out.Udp6InErrorsPerS)
	}
	if out.ProcessesTotal != nil {
		t.Errorf("ProcessesTotal = %v, want absent", *out.ProcessesTotal)
	}
}

// AgentSample rides nested inside HostSample rather than as a sibling
// repeated field, so that a partial ring-buffer drain cannot separate a
// scrape's self-telemetry from the scrape it describes. The nesting has to
// survive the wire for that to hold.
func TestAgentSampleRoundTripsNestedInHostSample(t *testing.T) {
	in := &netrav1.HostSample{
		TsMs: 1_700_000_000_000,
		Agent: &netrav1.AgentSample{
			ScrapeDurationMs:   proto.Uint32(12),
			BufferDepth:        proto.Uint32(3),
			BufferDroppedTotal: proto.Uint64(4),
			// PostLatencyMs unset: the last flush failed.
		},
	}

	raw, err := proto.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var out netrav1.HostSample
	if err := proto.Unmarshal(raw, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if out.GetAgent() == nil {
		t.Fatal("Agent = nil, want the nested self-telemetry preserved")
	}
	if got := out.GetAgent().GetScrapeDurationMs(); got != 12 {
		t.Errorf("ScrapeDurationMs = %d, want 12", got)
	}
	if got := out.GetAgent().GetBufferDepth(); got != 3 {
		t.Errorf("BufferDepth = %d, want 3", got)
	}
	if got := out.GetAgent().GetBufferDroppedTotal(); got != 4 {
		t.Errorf("BufferDroppedTotal = %d, want 4", got)
	}
	if out.GetAgent().PostLatencyMs != nil {
		t.Errorf("PostLatencyMs = %d, want absent after a failed flush",
			*out.GetAgent().PostLatencyMs)
	}

	// The fields the Self collector will fill are absent, not zero.
	if out.GetAgent().UptimeS != nil {
		t.Error("UptimeS is set, want absent until the Self collector lands")
	}
	if out.GetAgent().RssBytes != nil {
		t.Error("RssBytes is set, want absent until the Self collector lands")
	}
	if out.GetAgent().Goroutines != nil {
		t.Error("Goroutines is set, want absent until the Self collector lands")
	}
	if out.GetAgent().PostFailuresTotal != nil {
		t.Error("PostFailuresTotal is set, want absent until the Self collector lands")
	}
}

// Get* on a nil receiver is a path callers hit whenever a sample carries no
// self-telemetry at all, which is every sample from an agent older than this
// change.
func TestAgentSampleNilReceiverGettersReturnZero(t *testing.T) {
	var nilAgent *netrav1.AgentSample

	if got := nilAgent.GetScrapeDurationMs(); got != 0 {
		t.Errorf("nil.GetScrapeDurationMs() = %v, want 0", got)
	}
	if got := nilAgent.GetPostLatencyMs(); got != 0 {
		t.Errorf("nil.GetPostLatencyMs() = %v, want 0", got)
	}
	if got := nilAgent.GetUptimeS(); got != 0 {
		t.Errorf("nil.GetUptimeS() = %v, want 0", got)
	}
	if got := nilAgent.GetRssBytes(); got != 0 {
		t.Errorf("nil.GetRssBytes() = %v, want 0", got)
	}
	if got := nilAgent.GetGoroutines(); got != 0 {
		t.Errorf("nil.GetGoroutines() = %v, want 0", got)
	}
	if got := nilAgent.GetBufferDepth(); got != 0 {
		t.Errorf("nil.GetBufferDepth() = %v, want 0", got)
	}
	if got := nilAgent.GetBufferDroppedTotal(); got != 0 {
		t.Errorf("nil.GetBufferDroppedTotal() = %v, want 0", got)
	}
	if got := nilAgent.GetPostFailuresTotal(); got != 0 {
		t.Errorf("nil.GetPostFailuresTotal() = %v, want 0", got)
	}

	var nilSample *netrav1.HostSample
	if nilSample.GetAgent() != nil {
		t.Error("nil.GetAgent() != nil, want nil")
	}
	if got := nilSample.GetCtxtPerS(); got != 0 {
		t.Errorf("nil.GetCtxtPerS() = %v, want 0", got)
	}
	if got := nilSample.GetUsersLoggedIn(); got != 0 {
		t.Errorf("nil.GetUsersLoggedIn() = %v, want 0", got)
	}
	if got := nilSample.GetBootTimeS(); got != 0 {
		t.Errorf("nil.GetBootTimeS() = %v, want 0", got)
	}
}

// Capabilities ride the metadata block, so a collector that starts or stops
// being able to run changes the hash and the hub asks for a resend.
func TestMetadataCapabilitiesRoundTrip(t *testing.T) {
	in := &netrav1.Metadata{
		Hostname:     "h1",
		Capabilities: map[string]string{"processes": "namespaced", "users": "ok"},
	}

	raw, err := proto.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var out netrav1.Metadata
	if err := proto.Unmarshal(raw, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got := out.GetCapabilities()["processes"]; got != "namespaced" {
		t.Errorf("capabilities[processes] = %q, want %q", got, "namespaced")
	}
	if got := out.GetCapabilities()["users"]; got != "ok" {
		t.Errorf("capabilities[users] = %q, want %q", got, "ok")
	}

	var nilMd *netrav1.Metadata
	if got := nilMd.GetCapabilities(); got != nil {
		t.Errorf("nil.GetCapabilities() = %v, want nil", got)
	}
}
