package httpapi_test

import (
	"context"
	"net/http"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/trick77/netra/internal/hub/store"
	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
	"github.com/trick77/netra/internal/shared/version"
)

// tooOld is a release below version.MinAgent. A literal rather than something
// derived from the constant, so that raising MinAgent cannot quietly change
// what these tests mean by "old".
const tooOld = "0.0.1"

// aSample is one plausible host sample, so a refused batch has something to
// have failed to store.
func aSample() []*netrav1.HostSample {
	return []*netrav1.HostSample{
		{TsMs: 1_700_000_000_000, CpuTotal: proto.Float64(33)},
	}
}

func storedAgentVersion(t *testing.T, s *store.Store) string {
	t.Helper()
	var v *string
	if err := s.Pool().QueryRow(context.Background(),
		`SELECT agent_version FROM hosts WHERE hostname = 'h1'`).Scan(&v); err != nil {
		t.Fatalf("read agent_version: %v", err)
	}
	if v == nil {
		return ""
	}
	return *v
}

func countHostSamples(t *testing.T, s *store.Store) int {
	t.Helper()
	var n int
	if err := s.Pool().QueryRow(context.Background(),
		`SELECT count(*) FROM host_samples`).Scan(&n); err != nil {
		t.Fatalf("count host_samples: %v", err)
	}
	return n
}

// An agent below the minimum is refused, and writes no samples.
//
// The hub no longer compensates for what an old agent leaves out -- an event
// with no severity field lands as "info", a sensor with no kind lands with
// none -- so accepting the batch would store rows that are quietly wrong
// rather than honestly absent.
func TestIntegrationIngestRefusesAnAgentBelowTheMinimum(t *testing.T) {
	srv, token, s := newFixture(t)

	resp := post(t, srv, token, &netrav1.IngestRequest{
		Seq:          1,
		MetadataHash: []byte{1, 1, 1, 1, 1, 1, 1, 1},
		Metadata:     &netrav1.Metadata{Hostname: "h1", AgentVersion: tooOld},
		HostSamples:  aSample(),
	})

	if resp.StatusCode != http.StatusUpgradeRequired {
		t.Fatalf("status = %d, want 426", resp.StatusCode)
	}
	if n := countHostSamples(t, s); n != 0 {
		t.Errorf("stored %d host samples, want 0: a refused batch must write none", n)
	}
}

// The refused version is recorded anyway, and it is the ONLY thing a rejected
// batch writes.
//
// Otherwise hosts.agent_version stays NULL and the host page shows nothing for
// the one host an operator is trying to diagnose: a rejected agent would be the
// only kind that cannot say what it is running.
func TestIntegrationARefusedAgentStillReportsItsVersion(t *testing.T) {
	srv, token, s := newFixture(t)

	post(t, srv, token, &netrav1.IngestRequest{
		Seq:          1,
		MetadataHash: []byte{1, 1, 1, 1, 1, 1, 1, 1},
		Metadata:     &netrav1.Metadata{Hostname: "h1", AgentVersion: tooOld},
		HostSamples:  aSample(),
	})

	if got := storedAgentVersion(t, s); got != tooOld {
		t.Errorf("agent_version = %q, want %q", got, tooOld)
	}
}

// Having learned the version, the hub refuses the hash-only posts that follow.
//
// The version rides the handshake, so most posts do not carry one. Judging the
// stored version is what makes the gate hold in between.
func TestIntegrationIngestRefusesAHashOnlyPostFromAKnownOldAgent(t *testing.T) {
	srv, token, s := newFixture(t)
	hash := []byte{1, 1, 1, 1, 1, 1, 1, 1}

	// Given: a handshake that named a version below the minimum.
	post(t, srv, token, &netrav1.IngestRequest{
		Seq:          1,
		MetadataHash: hash,
		Metadata:     &netrav1.Metadata{Hostname: "h1", AgentVersion: tooOld},
	})

	// When: the same agent posts again carrying only its hash.
	resp := post(t, srv, token, &netrav1.IngestRequest{
		Seq:          2,
		MetadataHash: hash,
		HostSamples:  aSample(),
	})

	if resp.StatusCode != http.StatusUpgradeRequired {
		t.Fatalf("status = %d, want 426", resp.StatusCode)
	}
	if n := countHostSamples(t, s); n != 0 {
		t.Errorf("stored %d host samples, want 0", n)
	}
}

// THE DEADLOCK TEST: an upgraded agent must be able to come back.
//
// This is what separates a gate from a trap, and the naive version of this
// feature fails it. An operator upgrades a refused agent; it restarts with
// sendMetadata false, so it posts a NEW hash and no metadata block. A hub that
// judged the stored -- stale, old -- version would refuse before ever comparing
// hashes, never answer RequestMetadata, and so never be told about the upgrade.
// The host would stay locked out by the fix meant to free it.
//
// Hence: a stored version is trusted only while the stored hash still matches.
func TestIntegrationIngestAcceptsAnUpgradedAgentWhoseHashHasChanged(t *testing.T) {
	srv, token, s := newFixture(t)
	oldHash := []byte{1, 1, 1, 1, 1, 1, 1, 1}

	// Given: the hub knows this host as an agent below the minimum.
	post(t, srv, token, &netrav1.IngestRequest{
		Seq:          1,
		MetadataHash: oldHash,
		Metadata:     &netrav1.Metadata{Hostname: "h1", AgentVersion: tooOld},
	})

	// When: it is upgraded and restarts, posting a new hash and no block.
	newHash := []byte{2, 2, 2, 2, 2, 2, 2, 2}
	resp := post(t, srv, token, &netrav1.IngestRequest{
		Seq:          2,
		MetadataHash: newHash,
		HostSamples:  aSample(),
	})

	// Then: it is accepted, and asked to say who it is now.
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: an upgraded agent must not be locked out", resp.StatusCode)
	}
	var out netrav1.IngestResponse
	decodeBody(t, resp, &out)
	if !out.RequestMetadata {
		t.Fatal("RequestMetadata = false: without the ask, the hub never learns the new version")
	}

	// And: once it answers, it is an ordinary host again.
	third := post(t, srv, token, &netrav1.IngestRequest{
		Seq:          3,
		MetadataHash: newHash,
		Metadata:     &netrav1.Metadata{Hostname: "h1", AgentVersion: version.MinAgent},
		HostSamples:  aSample(),
	})
	if third.StatusCode != http.StatusOK {
		t.Fatalf("status after the upgrade handshake = %d, want 200", third.StatusCode)
	}
	if got := storedAgentVersion(t, s); got != version.MinAgent {
		t.Errorf("agent_version = %q, want %q", got, version.MinAgent)
	}
}

// A host that has never handshaken must be able to bootstrap.
//
// Its agent_version is NULL, which says nothing about how old it is. Refusing
// on that would mean no agent could ever complete a first handshake.
func TestIntegrationIngestAcceptsAHostThatHasNeverHandshaken(t *testing.T) {
	srv, token, _ := newFixture(t)

	resp := post(t, srv, token, &netrav1.IngestRequest{
		Seq:          1,
		MetadataHash: []byte{7, 7, 7, 7, 7, 7, 7, 7},
		HostSamples:  aSample(),
	})

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out netrav1.IngestResponse
	decodeBody(t, resp, &out)
	if !out.RequestMetadata {
		t.Error("RequestMetadata = false: a host with no stored hash must be asked")
	}
}

// The minimum itself, and every version that cannot be read, get in.
//
// "sim" is what netra-sim reports and "dev" is any local build; refusing either
// would lock the simulator and development out of their own hub.
func TestIntegrationIngestAcceptsTheMinimumAndTheUnparsable(t *testing.T) {
	for _, name := range []string{version.MinAgent, "sim", "dev", "empty"} {
		t.Run(name, func(t *testing.T) {
			reported := name
			if name == "empty" {
				reported = ""
			}
			srv, token, _ := newFixture(t)
			resp := post(t, srv, token, &netrav1.IngestRequest{
				Seq:          1,
				MetadataHash: []byte{3, 3, 3, 3, 3, 3, 3, 3},
				Metadata:     &netrav1.Metadata{Hostname: "h1", AgentVersion: reported},
				HostSamples:  aSample(),
			})
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200 for agent_version %q", resp.StatusCode, reported)
			}
		})
	}
}
