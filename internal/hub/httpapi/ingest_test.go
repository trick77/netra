package httpapi_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/trick77/netra/internal/hub/auth"
	"github.com/trick77/netra/internal/hub/httpapi"
	"github.com/trick77/netra/internal/hub/store"
	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
)

func newFixture(t *testing.T) (*httptest.Server, string, *store.Store) {
	t.Helper()
	ctx := context.Background()

	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	var hostID int32
	if err := s.Pool().QueryRow(ctx,
		`INSERT INTO hosts (hostname) VALUES ('h1') RETURNING id`).Scan(&hostID); err != nil {
		t.Fatalf("insert host: %v", err)
	}

	plain, hash, err := auth.Mint()
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO tokens (host_id, token_hash) VALUES ($1, $2)`, hostID, hash); err != nil {
		t.Fatalf("insert token: %v", err)
	}

	h := httpapi.NewIngestHandler(auth.NewAuthenticator(s.Pool()), s)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	return srv, plain, s
}

func post(t *testing.T, srv *httptest.Server, token string, req *netrav1.IngestRequest) *http.Response {
	t.Helper()

	body, err := proto.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	httpReq, err := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	httpReq.Header.Set("Content-Type", "application/x-protobuf")
	if token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := srv.Client().Do(httpReq)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestIntegrationIngestRejectsMissingToken(t *testing.T) {
	srv, _, _ := newFixture(t)
	resp := post(t, srv, "", &netrav1.IngestRequest{Seq: 1})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestIntegrationIngestStoresSamplesAndAcks(t *testing.T) {
	srv, token, s := newFixture(t)

	req := &netrav1.IngestRequest{
		Seq:          7,
		MetadataHash: []byte{1, 2, 3, 4, 5, 6, 7, 8},
		HostSamples: []*netrav1.HostSample{
			{TsMs: 1_700_000_000_000, CpuTotal: proto.Float64(33)},
		},
	}

	resp := post(t, srv, token, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var out netrav1.IngestResponse
	decodeBody(t, resp, &out)

	if out.AckSeq != 7 {
		t.Fatalf("AckSeq = %d, want 7", out.AckSeq)
	}
	// The hub has never seen this host's metadata, so it must ask for it.
	if !out.RequestMetadata {
		t.Fatal("RequestMetadata = false, want true on first contact")
	}

	var count int
	if err := s.Pool().QueryRow(context.Background(),
		`SELECT count(*) FROM host_samples`).Scan(&count); err != nil {
		t.Fatalf("query: %v", err)
	}
	if count != 1 {
		t.Fatalf("stored rows = %d, want 1", count)
	}
}

func TestIntegrationIngestStopsAskingOnceMetadataMatches(t *testing.T) {
	srv, token, _ := newFixture(t)
	hash := []byte{9, 9, 9, 9, 9, 9, 9, 9}

	first := post(t, srv, token, &netrav1.IngestRequest{
		Seq:          1,
		MetadataHash: hash,
		Metadata:     &netrav1.Metadata{Hostname: "h1", AgentVersion: "0.1.0"},
	})
	var out netrav1.IngestResponse
	decodeBody(t, first, &out)
	if out.RequestMetadata {
		t.Fatal("RequestMetadata = true, want false when metadata was supplied")
	}

	second := post(t, srv, token, &netrav1.IngestRequest{Seq: 2, MetadataHash: hash})
	decodeBody(t, second, &out)
	if out.RequestMetadata {
		t.Fatal("RequestMetadata = true, want false when the hash still matches")
	}
}

// Clearing a host's metadata_hash is how a migration gets every agent to say
// something it has already said.
//
// The 0009 migration needs this and could not work without it: it added the
// location columns, but the hash an agent computes covers the whole Metadata
// message and has always included location, so an existing host's stored hash
// still matches exactly what its agent would send. Metadata is sent once and
// then only on change, and not even an agent restart resends it -- so the new
// columns would have stayed NULL on every upgraded hub, which is the bug that
// migration exists to fix. Emptying the hash is the one lever the protocol
// already offers, and this pins that it still works.
func TestIntegrationIngestAsksAgainOnceTheStoredHashIsCleared(t *testing.T) {
	srv, token, s := newFixture(t)
	hash := []byte{9, 9, 9, 9, 9, 9, 9, 9}

	post(t, srv, token, &netrav1.IngestRequest{
		Seq:          1,
		MetadataHash: hash,
		Metadata:     &netrav1.Metadata{Hostname: "h1", Location: "Roubaix, France"},
	})

	// The steady state: same hash, nothing asked for.
	var out netrav1.IngestResponse
	decodeBody(t, post(t, srv, token,
		&netrav1.IngestRequest{Seq: 2, MetadataHash: hash}), &out)
	if out.RequestMetadata {
		t.Fatal("RequestMetadata = true, want false while the hash matches")
	}

	// What the migration does.
	if _, err := s.Pool().Exec(context.Background(),
		`UPDATE hosts SET metadata_hash = NULL`); err != nil {
		t.Fatalf("clear metadata_hash: %v", err)
	}

	decodeBody(t, post(t, srv, token,
		&netrav1.IngestRequest{Seq: 3, MetadataHash: hash}), &out)
	if !out.RequestMetadata {
		t.Fatal("RequestMetadata = false, want true once the stored hash is empty")
	}
}

func TestIntegrationIngestRejectsMalformedBody(t *testing.T) {
	srv, token, s := newFixture(t)

	httpReq, err := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader([]byte{0xff, 0x00, 0x01, 0xfe}))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	httpReq.Header.Set("Content-Type", "application/x-protobuf")
	httpReq.Header.Set("Authorization", "Bearer "+token)

	resp, err := srv.Client().Do(httpReq)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}

	var count int
	if err := s.Pool().QueryRow(context.Background(),
		`SELECT count(*) FROM host_samples`).Scan(&count); err != nil {
		t.Fatalf("query: %v", err)
	}
	if count != 0 {
		t.Fatalf("stored rows = %d, want 0", count)
	}
}

func TestIntegrationIngestRejectsWrongToken(t *testing.T) {
	srv, _, _ := newFixture(t)
	resp := post(t, srv, "nta_wrong", &netrav1.IngestRequest{Seq: 1})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	// The response must not distinguish an unknown host from a bad token:
	// compare it against the missing-token case, which must be identical.
	missing := post(t, srv, "", &netrav1.IngestRequest{Seq: 1})
	missingBody, err := io.ReadAll(missing.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(body) != string(missingBody) {
		t.Fatalf("wrong-token body %q differs from missing-token body %q", body, missingBody)
	}
}

func TestIntegrationIngestRejectsNonPost(t *testing.T) {
	srv, token, _ := newFixture(t)

	httpReq, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)

	resp, err := srv.Client().Do(httpReq)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", resp.StatusCode)
	}
}

func TestIntegrationIngestReasksOnHashDrift(t *testing.T) {
	srv, token, _ := newFixture(t)
	hash := []byte{9, 9, 9, 9, 9, 9, 9, 9}

	first := post(t, srv, token, &netrav1.IngestRequest{
		Seq:          1,
		MetadataHash: hash,
		Metadata:     &netrav1.Metadata{Hostname: "h1", AgentVersion: "0.1.0"},
	})
	var out netrav1.IngestResponse
	decodeBody(t, first, &out)
	if out.RequestMetadata {
		t.Fatal("RequestMetadata = true, want false when metadata was supplied")
	}

	// A different hash means the agent's metadata (e.g. after an upgrade or
	// an edited AGENT_LOCATION) no longer matches what the hub stored.
	drifted := []byte{1, 1, 1, 1, 1, 1, 1, 1}
	second := post(t, srv, token, &netrav1.IngestRequest{Seq: 2, MetadataHash: drifted})
	decodeBody(t, second, &out)
	if !out.RequestMetadata {
		t.Fatal("RequestMetadata = false, want true when the hash has drifted")
	}
}

// TestIntegrationIngestStorageFailureReturns503 forces a real storage error
// rather than mocking the store: the handler's insert path runs over a pool
// that gets closed out from under it, while a second, independent pool keeps
// authentication working so the request reaches InsertHostSamples at all.
// A single shared pool cannot be used for this because closing it would also
// break authentication, turning the request into a 401 instead of the 503
// this test needs to exercise.
func TestIntegrationIngestStorageFailureReturns503(t *testing.T) {
	ctx := context.Background()

	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	var hostID int32
	if err := s.Pool().QueryRow(ctx,
		`INSERT INTO hosts (hostname) VALUES ('h1') RETURNING id`).Scan(&hostID); err != nil {
		t.Fatalf("insert host: %v", err)
	}

	plain, hash, err := auth.Mint()
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO tokens (host_id, token_hash) VALUES ($1, $2)`, hostID, hash); err != nil {
		t.Fatalf("insert token: %v", err)
	}

	authStore := store.OpenTestSibling(t, s)

	h := httpapi.NewIngestHandler(auth.NewAuthenticator(authStore.Pool()), s)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	// Close the store's own pool so InsertHostSamples fails; authStore's pool
	// is untouched, so the request still authenticates.
	s.Close()

	resp := post(t, srv, plain, &netrav1.IngestRequest{
		Seq: 1,
		HostSamples: []*netrav1.HostSample{
			{TsMs: 1_700_000_000_000, CpuTotal: proto.Float64(33)},
		},
	})
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}

	// A 503 must carry retry_after_s so the agent waits at least that long
	// before retrying, instead of falling back to its own backoff blind.
	var out netrav1.IngestResponse
	decodeBody(t, resp, &out)
	if out.RetryAfterS == 0 {
		t.Fatal("RetryAfterS = 0, want non-zero on a storage-failure 503")
	}
}

// TestIntegrationIngestDropsImplausibleTimestampsButStoresTheRest covers spec
// §7.5/§9 clock skew handling: a far-future or pre-2020 sample must not fail
// the whole batch (which would 503 and make the agent re-send the identical
// poison batch forever), and must not poison host_current, whose upsert
// guard only accepts a later last_seen — a permanently-future value would
// block every real update after it.
func TestIntegrationIngestDropsImplausibleTimestampsButStoresTheRest(t *testing.T) {
	srv, token, s := newFixture(t)

	goodTs := time.Now().Add(-time.Minute).UnixMilli()
	farFutureTs := time.Now().Add(2 * time.Hour).UnixMilli()
	preEpochTs := time.Date(2019, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()

	resp := post(t, srv, token, &netrav1.IngestRequest{
		Seq: 1,
		HostSamples: []*netrav1.HostSample{
			{TsMs: farFutureTs, CpuTotal: proto.Float64(99)},
			{TsMs: preEpochTs, CpuTotal: proto.Float64(1)},
			{TsMs: goodTs, CpuTotal: proto.Float64(42)},
		},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 — the good sample must still be stored", resp.StatusCode)
	}

	var out netrav1.IngestResponse
	decodeBody(t, resp, &out)
	if out.AckSeq != 1 {
		t.Fatalf("AckSeq = %d, want 1 — the batch is still acked in full", out.AckSeq)
	}

	ctx := context.Background()
	var count int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM host_samples`).Scan(&count); err != nil {
		t.Fatalf("query: %v", err)
	}
	if count != 1 {
		t.Fatalf("stored rows = %d, want 1 (only the plausible sample)", count)
	}

	var cpuTotal float64
	if err := s.Pool().QueryRow(ctx,
		`SELECT cpu_total FROM host_samples`).Scan(&cpuTotal); err != nil {
		t.Fatalf("query: %v", err)
	}
	if cpuTotal != 42 {
		t.Fatalf("stored cpu_total = %v, want 42 (the plausible sample)", cpuTotal)
	}

	// host_current must reflect the good sample's timestamp, not the
	// far-future one — that is the poisoning this fix prevents.
	var lastSeen time.Time
	if err := s.Pool().QueryRow(ctx,
		`SELECT last_seen FROM host_current`).Scan(&lastSeen); err != nil {
		t.Fatalf("query host_current: %v", err)
	}
	wantLastSeen := time.UnixMilli(goodTs).UTC()
	if lastSeen.Sub(wantLastSeen).Abs() > time.Second {
		t.Fatalf("host_current.last_seen = %v, want ~%v (the plausible sample, not the far-future one)",
			lastSeen, wantLastSeen)
	}
}

// A broken agent_samples insert still 503s the batch, but must not freeze
// host_current. The host samples the cache summarises landed successfully on
// this very request, and the agent will keep retrying — so leaving last_seen
// stale would show the host as gone while its data is in fact arriving.
func TestIntegrationIngestAgentSampleFailureStillRefreshesHostCurrent(t *testing.T) {
	srv, token, s := newFixture(t)
	ctx := context.Background()

	// Break only agent_samples, and only for writes, leaving the table, its
	// continuous aggregates, host_samples and host_current intact. That
	// isolates the one failing insert rather than simulating a dead pool.
	//
	// A trigger raising a class 53 SQLSTATE, NOT a CHECK constraint. This
	// insert goes through execBatch, and a CHECK violation is 23514 -- class
	// 23, which poisonRow correctly treats as a row that will fail identically
	// forever and quarantines rather than retrying. This test is about the
	// TRANSIENT case, which is the one that must 503.
	breakTableTransiently(t, s, "agent_samples")

	ts := time.Now().Add(-time.Minute).UnixMilli()

	resp := post(t, srv, token, &netrav1.IngestRequest{
		Seq: 1,
		HostSamples: []*netrav1.HostSample{
			{
				TsMs:     ts,
				CpuTotal: proto.Float64(42),
				Agent:    &netrav1.AgentSample{ScrapeDurationMs: proto.Uint32(7)},
			},
		},
	})
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 — a lost agent sample must be retried", resp.StatusCode)
	}

	var count int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM host_samples`).Scan(&count); err != nil {
		t.Fatalf("query host_samples: %v", err)
	}
	if count != 1 {
		t.Fatalf("host_samples rows = %d, want 1 — the primary write did succeed", count)
	}

	var lastSeen time.Time
	if err := s.Pool().QueryRow(ctx,
		`SELECT last_seen FROM host_current`).Scan(&lastSeen); err != nil {
		t.Fatalf("query host_current: %v — want a row, not a host frozen as stale", err)
	}
	want := time.UnixMilli(ts).UTC()
	if lastSeen.Sub(want).Abs() > time.Second {
		t.Fatalf("host_current.last_seen = %v, want ~%v", lastSeen, want)
	}
}

// The mirror of the test above: host_current is a derived cache the next
// scrape rebuilds, so its own failure is logged and swallowed. The batch must
// still be acked -- 503ing here would make the agent re-send samples that are
// already stored, for the sake of a cache that repairs itself.
func TestIntegrationIngestHostCurrentFailureIsLoggedNotFatal(t *testing.T) {
	srv, token, s := newFixture(t)
	ctx := context.Background()

	if _, err := s.Pool().Exec(ctx,
		`ALTER TABLE host_current ADD CONSTRAINT reject_every_upsert CHECK (false) NOT VALID`); err != nil {
		t.Fatalf("break host_current: %v", err)
	}

	resp := post(t, srv, token, &netrav1.IngestRequest{
		Seq: 1,
		HostSamples: []*netrav1.HostSample{
			{TsMs: time.Now().Add(-time.Minute).UnixMilli(), CpuTotal: proto.Float64(42)},
		},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 — a broken cache must not fail the batch", resp.StatusCode)
	}

	var out netrav1.IngestResponse
	decodeBody(t, resp, &out)
	if out.AckSeq != 1 {
		t.Fatalf("AckSeq = %d, want 1", out.AckSeq)
	}

	var count int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM host_samples`).Scan(&count); err != nil {
		t.Fatalf("query host_samples: %v", err)
	}
	if count != 1 {
		t.Fatalf("host_samples rows = %d, want 1", count)
	}
}

// TestIntegrationIngestRejectsOversizedBody covers the failure mode of a
// silent partial accept: io.ReadAll(io.LimitReader(...)) would truncate an
// over-limit body at whatever byte the limit lands on, which can still parse
// as a valid (short) batch — the hub would then ack a seq the agent never
// fully got stored, permanently losing the truncated samples. MaxBytesReader
// must instead fail the read outright.
func TestIntegrationIngestRejectsOversizedBody(t *testing.T) {
	srv, token, s := newFixture(t)

	oversized := bytes.Repeat([]byte{0x0a}, 5<<20) // 5 MiB > 4 MiB maxBodyBytes

	httpReq, err := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader(oversized))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	httpReq.Header.Set("Content-Type", "application/x-protobuf")
	httpReq.Header.Set("Authorization", "Bearer "+token)

	resp, err := srv.Client().Do(httpReq)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })

	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", resp.StatusCode)
	}

	var count int
	if err := s.Pool().QueryRow(context.Background(),
		`SELECT count(*) FROM host_samples`).Scan(&count); err != nil {
		t.Fatalf("query: %v", err)
	}
	if count != 0 {
		t.Fatalf("stored rows = %d, want 0 — an oversized body must not be silently truncated and partially accepted", count)
	}
}

func decodeBody(t *testing.T, resp *http.Response, out proto.Message) {
	t.Helper()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if err := proto.Unmarshal(raw, out); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}
}

// Per-core rows carry their own timestamps, so they need their own bounds
// check. A poison row is dropped on its own: failing the batch would 503, and
// the agent would re-send the identical batch forever.
func TestIntegrationIngestStoresCpuCoreRowsAndDropsImplausibleOnes(t *testing.T) {
	srv, token, s := newFixture(t)

	goodTs := time.Now().Add(-time.Minute).UnixMilli()
	farFutureTs := time.Now().Add(2 * time.Hour).UnixMilli()

	resp := post(t, srv, token, &netrav1.IngestRequest{
		Seq:         1,
		HostSamples: []*netrav1.HostSample{{TsMs: goodTs, CpuTotal: proto.Float64(42)}},
		CpuCores: []*netrav1.CpuCoreSample{
			{TsMs: goodTs, Core: 0, Busy: proto.Float64(75)},
			{TsMs: goodTs, Core: 1, Busy: proto.Float64(25)},
			{TsMs: farFutureTs, Core: 2, Busy: proto.Float64(99)},
		},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 -- one bad row must not fail the batch", resp.StatusCode)
	}

	ctx := context.Background()
	var count int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM cpu_core_samples`).Scan(&count); err != nil {
		t.Fatalf("count cpu_core_samples: %v", err)
	}
	if count != 2 {
		t.Fatalf("cpu_core_samples rows = %d, want 2 -- the far-future row must be dropped", count)
	}

	// The core the far-future row claimed must not be present at all: a row
	// dated past every retention policy would otherwise never be cleaned up.
	var future int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM cpu_core_samples WHERE core = 2`).Scan(&future); err != nil {
		t.Fatalf("count core 2: %v", err)
	}
	if future != 0 {
		t.Errorf("core 2 rows = %d, want 0", future)
	}

	// And the host sample alongside them still landed.
	var hostRows int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM host_samples`).Scan(&hostRows); err != nil {
		t.Fatalf("count host_samples: %v", err)
	}
	if hostRows != 1 {
		t.Errorf("host_samples = %d, want 1", hostRows)
	}
}

// A per-core insert that fails must 503 with retry_after_s, exactly like the
// host-sample path above -- not a logged-and-ignored failure. These rows are
// primary data on a key the agent will replay, so a silent drop would lose
// them: the agent takes the 200 as an ack and clears its ring buffer.
//
// The request carries no host samples on purpose. InsertHostSamples returns
// early on an empty batch without touching the pool, so the per-core insert is
// the first statement to meet the closed one -- which is what puts the failure
// on the branch under test rather than an earlier one.
func TestIntegrationIngestCpuCoreStorageFailureReturns503(t *testing.T) {
	ctx := context.Background()

	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	var hostID int32
	if err := s.Pool().QueryRow(ctx,
		`INSERT INTO hosts (hostname) VALUES ('h1') RETURNING id`).Scan(&hostID); err != nil {
		t.Fatalf("insert host: %v", err)
	}

	plain, hash, err := auth.Mint()
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO tokens (host_id, token_hash) VALUES ($1, $2)`, hostID, hash); err != nil {
		t.Fatalf("insert token: %v", err)
	}

	// A second pool for auth, so the request still authenticates after the
	// store's own pool is closed.
	authStore := store.OpenTestSibling(t, s)

	h := httpapi.NewIngestHandler(auth.NewAuthenticator(authStore.Pool()), s)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	s.Close()

	resp := post(t, srv, plain, &netrav1.IngestRequest{
		Seq: 1,
		CpuCores: []*netrav1.CpuCoreSample{
			{TsMs: time.Now().Add(-time.Minute).UnixMilli(), Core: 0, Busy: proto.Float64(50)},
		},
	})
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}

	var out netrav1.IngestResponse
	decodeBody(t, resp, &out)
	if out.RetryAfterS == 0 {
		t.Fatal("RetryAfterS = 0, want non-zero on a storage-failure 503")
	}
}

// Two hosts reporting the SAME hostname must both keep ingesting.
//
// The regression this pins: SaveMetadata used to write `hostname = $2` from the
// agent. hosts_hostname_key is UNIQUE and NULLS NOT DISTINCT, so the second
// host's metadata save raised 23505 -- and that statement has no poisonRow
// quarantine, so it became a 503. The agent answers a 503 by re-sending the
// IDENTICAL batch, which wedges that host permanently.
//
// Two cloned VMs, two Raspberry Pis both called `raspberrypi`, or two agents
// that report no hostname at all (both writing ”) all reach this.
func TestIntegrationIngestTwoHostsReportingOneHostnameBothSucceed(t *testing.T) {
	ctx := context.Background()

	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	h := httpapi.NewIngestHandler(auth.NewAuthenticator(s.Pool()), s)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	// Both with site_id NULL, which is what the UI's create form produces.
	tokenA := newHostWithToken(t, s, "alpha")
	tokenB := newHostWithToken(t, s, "beta")

	for _, tc := range []struct {
		name  string
		token string
		hash  []byte
	}{
		{"first host", tokenA, []byte{1, 1, 1, 1, 1, 1, 1, 1}},
		{"second host", tokenB, []byte{2, 2, 2, 2, 2, 2, 2, 2}},
	} {
		resp := post(t, srv, tc.token, &netrav1.IngestRequest{
			Seq:          1,
			MetadataHash: tc.hash,
			Metadata:     &netrav1.Metadata{Hostname: "raspberrypi", AgentVersion: "0.1.0"},
		})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200 -- a shared reported hostname must not wedge a host",
				tc.name, resp.StatusCode)
		}
	}

	// And the operator's names survive: the agent does not get to rename the
	// row they created.
	rows, err := s.Pool().Query(ctx, `SELECT coalesce(hostname, '') FROM hosts ORDER BY hostname`)
	if err != nil {
		t.Fatalf("query hosts: %v", err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		names = append(names, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate: %v", err)
	}
	if len(names) != 2 || names[0] != "alpha" || names[1] != "beta" {
		t.Errorf("hostnames = %v, want [alpha beta] -- the operator named these, not the agent", names)
	}

	// The rest of the metadata still lands, so dropping hostname did not
	// silently drop the save.
	var agentVersion *string
	if err := s.Pool().QueryRow(ctx,
		`SELECT agent_version FROM hosts WHERE hostname = 'beta'`).Scan(&agentVersion); err != nil {
		t.Fatalf("query agent_version: %v", err)
	}
	if agentVersion == nil || *agentVersion != "0.1.0" {
		t.Errorf("agent_version = %v, want 0.1.0", agentVersion)
	}
}

// host_current must be refreshed before ANY write that can 503, not just
// before the agent_samples insert.
//
// TestIntegrationIngestAgentSampleFailureStillRefreshesHostCurrent covers the
// insert that comes last. This covers storeFamilies, which used to run BETWEEN
// the host_samples insert and the host_current upsert -- so a single broken
// family left the host reading "never seen" in the UI while its samples were in
// fact landing on every retry.
func TestIntegrationIngestFamilyFailureStillRefreshesHostCurrent(t *testing.T) {
	srv, token, s := newFixture(t)
	ctx := context.Background()

	// One family only, transiently -- see breakTableTransiently on why this is
	// not a CHECK constraint.
	breakTableTransiently(t, s, "net_samples")

	ts := time.Now().Add(-time.Minute).UnixMilli()

	resp := post(t, srv, token, &netrav1.IngestRequest{
		Seq:         1,
		HostSamples: []*netrav1.HostSample{{TsMs: ts, CpuTotal: proto.Float64(42)}},
		Net:         []*netrav1.NetSample{{TsMs: ts, Iface: "eth0", RxBytes: proto.Float64(1)}},
	})
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 -- a lost family must be retried", resp.StatusCode)
	}

	var lastSeen time.Time
	if err := s.Pool().QueryRow(ctx,
		`SELECT last_seen FROM host_current`).Scan(&lastSeen); err != nil {
		t.Fatalf("query host_current: %v -- want a row, not a host frozen as stale", err)
	}
	want := time.UnixMilli(ts).UTC()
	if lastSeen.Sub(want).Abs() > time.Second {
		t.Fatalf("host_current.last_seen = %v, want ~%v", lastSeen, want)
	}
}

// newHostWithToken registers a host with no site and returns its plaintext
// token, the shape the UI's create form produces.
func newHostWithToken(t *testing.T, s *store.Store, hostname string) string {
	t.Helper()
	ctx := context.Background()

	var hostID int32
	if err := s.Pool().QueryRow(ctx,
		`INSERT INTO hosts (hostname) VALUES ($1) RETURNING id`, hostname).Scan(&hostID); err != nil {
		t.Fatalf("insert host %s: %v", hostname, err)
	}

	plain, hash, err := auth.Mint()
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO tokens (host_id, token_hash) VALUES ($1, $2)`, hostID, hash); err != nil {
		t.Fatalf("insert token: %v", err)
	}
	return plain
}

// breakTableTransiently makes every INSERT into table fail with a SQLSTATE the
// hub must treat as retryable, leaving every other table alone.
//
// Class 53 (insufficient resources) is deliberate. poisonRow quarantines only
// classes 22 and 23 -- rows Postgres will refuse identically forever -- and
// retries everything else, so a CHECK constraint (23514) models a poisoned row
// rather than a database that is briefly unavailable. A trigger also fires on a
// hypertable's chunks, which is what these tables are.
func breakTableTransiently(t *testing.T, s *store.Store, table string) {
	t.Helper()
	ctx := context.Background()

	fn := table + "_reject"
	if _, err := s.Pool().Exec(ctx, fmt.Sprintf(`
		CREATE FUNCTION %s() RETURNS trigger AS $$
		BEGIN
			RAISE EXCEPTION 'simulated transient storage failure' USING ERRCODE = '53100';
		END;
		$$ LANGUAGE plpgsql`, fn)); err != nil {
		t.Fatalf("create %s: %v", fn, err)
	}
	if _, err := s.Pool().Exec(ctx, fmt.Sprintf(
		`CREATE TRIGGER %s BEFORE INSERT ON %s FOR EACH ROW EXECUTE FUNCTION %s()`,
		fn, table, fn)); err != nil {
		t.Fatalf("break %s: %v", table, err)
	}
}

// An agent_samples row Postgres will never accept is DROPPED, not retried
// forever.
//
// This is the behaviour routing the insert through execBatch buys. The agent
// answers a 503 by re-sending the IDENTICAL batch and its ring buffer only
// drops a prefix the hub acknowledged, so 503ing a row that fails the same way
// every time wedges that host permanently. Class 22 and 23 are exactly the
// errors that will not change on a retry; the transient case above still 503s.
func TestIntegrationIngestUnstorableAgentSampleIsQuarantinedNotRetriedForever(t *testing.T) {
	srv, token, s := newFixture(t)
	ctx := context.Background()

	if _, err := s.Pool().Exec(ctx,
		`ALTER TABLE agent_samples ADD CONSTRAINT reject_every_insert CHECK (false) NOT VALID`); err != nil {
		t.Fatalf("break agent_samples: %v", err)
	}

	ts := time.Now().Add(-time.Minute).UnixMilli()

	resp := post(t, srv, token, &netrav1.IngestRequest{
		Seq: 1,
		HostSamples: []*netrav1.HostSample{
			{
				TsMs:     ts,
				CpuTotal: proto.Float64(42),
				Agent:    &netrav1.AgentSample{ScrapeDurationMs: proto.Uint32(7)},
			},
		},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 -- a row that can never be stored must not wedge the host",
			resp.StatusCode)
	}

	var out netrav1.IngestResponse
	decodeBody(t, resp, &out)
	if out.AckSeq != 1 {
		t.Fatalf("AckSeq = %d, want 1 -- the batch has to be acked or it is replayed forever", out.AckSeq)
	}

	// The host sample itself still landed: quarantine drops the unstorable row,
	// not the batch around it.
	var count int
	if err := s.Pool().QueryRow(ctx, `SELECT count(*) FROM host_samples`).Scan(&count); err != nil {
		t.Fatalf("query host_samples: %v", err)
	}
	if count != 1 {
		t.Errorf("host_samples rows = %d, want 1", count)
	}
}

// One POST fans out to three host-level tables: host_samples, plus the two
// families that exist only because a continuous aggregate cannot gain a
// column. A counter routed to the wrong statement -- or to none -- is stored
// nowhere and surfaces much later as a panel that will not draw.
func TestIntegrationIngestFansOutToEveryHostLevelTable(t *testing.T) {
	srv, token, s := newFixture(t)

	req := &netrav1.IngestRequest{
		Seq: 11,
		HostSamples: []*netrav1.HostSample{{
			TsMs: 1_700_000_000_000,
			// host_samples
			CpuTotal: proto.Float64(33),
			// host_snmp_samples
			IpInReceivesPerS: proto.Float64(1016),
			// host_proto_samples
			TcpInSegsPerS:      proto.Float64(4200),
			UdpInDatagramsPerS: proto.Float64(640),
		}},
	}

	resp := post(t, srv, token, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	ctx := context.Background()
	for table, column := range map[string]string{
		"host_samples":       "cpu_total",
		"host_snmp_samples":  "ip_in_receives_per_s",
		"host_proto_samples": "tcp_in_segs_per_s",
	} {
		var n int
		if err := s.Pool().QueryRow(ctx,
			"SELECT count(*) FROM "+table+" WHERE "+column+" IS NOT NULL").Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if n != 1 {
			t.Errorf("%s has %d rows with %s set, want 1", table, n, column)
		}
	}
}

// The interfaces inventory rides the same POST as the addresses, and the hub
// stores both. An interface with no address is the row the separate table
// exists for, so it is the one asserted here.
func TestIntegrationIngestStoresInterfaceInventory(t *testing.T) {
	srv, token, s := newFixture(t)

	req := &netrav1.IngestRequest{
		Seq: 12,
		HostSamples: []*netrav1.HostSample{
			{TsMs: 1_700_000_000_000, CpuTotal: proto.Float64(1)},
		},
		Addresses: []*netrav1.HostAddress{
			{Iface: "eth0", Address: "10.0.0.5", Family: 4},
		},
		Interfaces: []*netrav1.HostInterface{
			{
				Iface:     "eth0",
				OperState: "up",
				SpeedMbps: proto.Uint64(1000),
				Duplex:    "full",
				Mtu:       proto.Uint32(1500),
				Mac:       "52:54:00:3a:1c:07",
			},
			// No address anywhere in this request.
			{Iface: "bond0", OperState: "lowerlayerdown", Mtu: proto.Uint32(9000)},
		},
	}

	resp := post(t, srv, token, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	ctx := context.Background()
	var state string
	if err := s.Pool().QueryRow(ctx,
		`SELECT oper_state FROM host_interfaces WHERE iface = 'bond0'`).Scan(&state); err != nil {
		t.Fatalf("bond0 was not stored; an interface with no address is the case this "+
			"table exists for: %v", err)
	}
	if state != "lowerlayerdown" {
		t.Errorf("bond0 oper_state = %q, want lowerlayerdown", state)
	}

	var speed *int64
	if err := s.Pool().QueryRow(ctx,
		`SELECT speed_mbps FROM host_interfaces WHERE iface = 'bond0'`).Scan(&speed); err != nil {
		t.Fatalf("query bond0 speed: %v", err)
	}
	if speed != nil {
		t.Errorf("bond0 speed_mbps = %d, want NULL", *speed)
	}
}
