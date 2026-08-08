package store_test

import (
	"context"
	"testing"

	"google.golang.org/protobuf/proto"

	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
	"github.com/trick77/netra/internal/hub/store"
)

// Every column added for the kernel, network, process and session metrics,
// each with a distinct value, round-tripped through the real INSERT.
//
// Distinct values are the point. A 54-placeholder statement is exactly the
// shape where two adjacent parameters get transposed, and identical test
// values would let that through: udp_in_errors and udp_rcvbuf_errors swapped
// is invisible if both are 1. Every value here is unique, so a transposition
// fails on a named column.
func TestIntegrationNewHostColumnsRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	hostID := seedHost(t, s)

	sample := &netrav1.HostSample{
		TsMs: recentBucket().UnixMilli(),

		CtxtPerS:     proto.Float64(101.5),
		IntrPerS:     proto.Float64(102.5),
		ForksPerS:    proto.Float64(103.5),
		ProcsRunning: proto.Uint32(104),
		ProcsBlocked: proto.Uint32(105),
		BootTimeS:    proto.Uint64(1_699_000_106),

		ProcessesTotal: proto.Uint32(107),
		UsersLoggedIn:  proto.Uint32(108),
		ServicesTotal:  proto.Uint32(109),
		ServicesFailed: proto.Uint32(110),

		TcpRetransSegsPerS:     proto.Float64(111.5),
		TcpOutRstsPerS:         proto.Float64(112.5),
		TcpInErrsPerS:          proto.Float64(113.5),
		TcpActiveOpensPerS:     proto.Float64(114.5),
		TcpPassiveOpensPerS:    proto.Float64(115.5),
		TcpAttemptFailsPerS:    proto.Float64(116.5),
		TcpCurrEstab:           proto.Uint32(117),
		TcpListenOverflowsPerS: proto.Float64(118.5),
		TcpListenDropsPerS:     proto.Float64(119.5),

		UdpInErrorsPerS:     proto.Float64(120.5),
		UdpRcvbufErrorsPerS: proto.Float64(121.5),
		UdpSndbufErrorsPerS: proto.Float64(122.5),
		UdpNoPortsPerS:      proto.Float64(123.5),

		IpReasmReqdsPerS:  proto.Float64(124.5),
		IpReasmFailsPerS:  proto.Float64(125.5),
		IpFragFailsPerS:   proto.Float64(126.5),
		IpFragCreatesPerS: proto.Float64(127.5),

		Udp6InErrorsPerS:     proto.Float64(128.5),
		Udp6RcvbufErrorsPerS: proto.Float64(129.5),
		Udp6SndbufErrorsPerS: proto.Float64(130.5),
		Udp6NoPortsPerS:      proto.Float64(131.5),

		Ip6ReasmReqdsPerS:  proto.Float64(132.5),
		Ip6ReasmFailsPerS:  proto.Float64(133.5),
		Ip6FragFailsPerS:   proto.Float64(134.5),
		Ip6FragCreatesPerS: proto.Float64(135.5),
	}

	if _, err := s.InsertHostSamples(ctx, hostID, []*netrav1.HostSample{sample}); err != nil {
		t.Fatalf("InsertHostSamples: %v", err)
	}

	// Scanned into a map keyed by column name so a failure names the column
	// rather than a position in a 35-variable Scan.
	floatCols := map[string]float64{
		"ctxt_per_s":                 101.5,
		"intr_per_s":                 102.5,
		"forks_per_s":                103.5,
		"tcp_retrans_segs_per_s":     111.5,
		"tcp_out_rsts_per_s":         112.5,
		"tcp_in_errs_per_s":          113.5,
		"tcp_active_opens_per_s":     114.5,
		"tcp_passive_opens_per_s":    115.5,
		"tcp_attempt_fails_per_s":    116.5,
		"tcp_listen_overflows_per_s": 118.5,
		"tcp_listen_drops_per_s":     119.5,
		"udp_in_errors_per_s":        120.5,
		"udp_rcvbuf_errors_per_s":    121.5,
		"udp_sndbuf_errors_per_s":    122.5,
		"udp_no_ports_per_s":         123.5,
		"ip_reasm_reqds_per_s":       124.5,
		"ip_reasm_fails_per_s":       125.5,
		"ip_frag_fails_per_s":        126.5,
		"ip_frag_creates_per_s":      127.5,
		"udp6_in_errors_per_s":       128.5,
		"udp6_rcvbuf_errors_per_s":   129.5,
		"udp6_sndbuf_errors_per_s":   130.5,
		"udp6_no_ports_per_s":        131.5,
		"ip6_reasm_reqds_per_s":      132.5,
		"ip6_reasm_fails_per_s":      133.5,
		"ip6_frag_fails_per_s":       134.5,
		"ip6_frag_creates_per_s":     135.5,
	}
	for col, want := range floatCols {
		var got *float64
		//nolint:gosec // column names are literals from this test's own map
		if err := s.Pool().QueryRow(ctx,
			`SELECT `+col+` FROM host_samples WHERE host_id = $1`, hostID).Scan(&got); err != nil {
			t.Fatalf("select %s: %v", col, err)
		}
		if got == nil {
			t.Errorf("%s is NULL, want %v", col, want)
			continue
		}
		if *got != want {
			t.Errorf("%s = %v, want %v", col, *got, want)
		}
	}

	intCols := map[string]int64{
		"procs_running":   104,
		"procs_blocked":   105,
		"boot_time_s":     1_699_000_106,
		"processes_total": 107,
		"users_logged_in": 108,
		"services_total":  109,
		"services_failed": 110,
		"tcp_curr_estab":  117,
	}
	for col, want := range intCols {
		var got *int64
		//nolint:gosec // column names are literals from this test's own map
		if err := s.Pool().QueryRow(ctx,
			`SELECT `+col+` FROM host_samples WHERE host_id = $1`, hostID).Scan(&got); err != nil {
			t.Fatalf("select %s: %v", col, err)
		}
		if got == nil {
			t.Errorf("%s is NULL, want %d", col, want)
			continue
		}
		if *got != want {
			t.Errorf("%s = %d, want %d", col, *got, want)
		}
	}
}

// A host with no IPv6 and no utmp sends a sample where most of the new fields
// are unset. Every one must land as NULL: a 0 would assert that the counter
// was measured and found to be zero, which is a different claim.
func TestIntegrationUnsetNewColumnsBecomeNull(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	hostID := seedHost(t, s)

	sample := &netrav1.HostSample{
		TsMs: recentBucket().UnixMilli(),
		// Present and zero: the counter was read and had not moved.
		UdpInErrorsPerS: proto.Float64(0),
		// Everything else unset.
	}

	if _, err := s.InsertHostSamples(ctx, hostID, []*netrav1.HostSample{sample}); err != nil {
		t.Fatalf("InsertHostSamples: %v", err)
	}

	var (
		udpInErrors  *float64
		udp6InErrors *float64
		usersLoggedIn,
		processesTotal,
		servicesTotal *int64
	)
	if err := s.Pool().QueryRow(ctx, `
		SELECT udp_in_errors_per_s, udp6_in_errors_per_s,
		       users_logged_in, processes_total, services_total
		  FROM host_samples WHERE host_id = $1`, hostID).Scan(
		&udpInErrors, &udp6InErrors,
		&usersLoggedIn, &processesTotal, &servicesTotal); err != nil {
		t.Fatalf("query: %v", err)
	}

	if udpInErrors == nil || *udpInErrors != 0 {
		t.Errorf("udp_in_errors_per_s = %v, want 0 — a present zero must survive", udpInErrors)
	}
	if udp6InErrors != nil {
		t.Errorf("udp6_in_errors_per_s = %v, want NULL — this host has no IPv6", *udp6InErrors)
	}
	if usersLoggedIn != nil {
		t.Errorf("users_logged_in = %v, want NULL — no utmp on this host", *usersLoggedIn)
	}
	if processesTotal != nil {
		t.Errorf("processes_total = %v, want NULL — no pid: host", *processesTotal)
	}
	if servicesTotal != nil {
		t.Errorf("services_total = %v, want NULL — no systemd collector yet", *servicesTotal)
	}
}
