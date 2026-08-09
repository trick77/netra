package httpapi_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
	"github.com/trick77/netra/internal/hub/auth"
	"github.com/trick77/netra/internal/hub/httpapi"
	"github.com/trick77/netra/internal/hub/store"
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
	// an edited NETRA_LOCATION) no longer matches what the hub stored.
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

	dsn := os.Getenv("NETRA_TEST_DSN")
	authStore, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open second pool for auth: %v", err)
	}
	t.Cleanup(authStore.Close)

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

	// Break only agent_samples, and only for writes: a constraint no row can
	// satisfy fails every insert while leaving the table, its continuous
	// aggregates, host_samples and host_current intact. That isolates the one
	// failing insert rather than simulating a dead pool.
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
	authStore, err := store.Open(ctx, os.Getenv("NETRA_TEST_DSN"))
	if err != nil {
		t.Fatalf("open second pool for auth: %v", err)
	}
	t.Cleanup(authStore.Close)

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
