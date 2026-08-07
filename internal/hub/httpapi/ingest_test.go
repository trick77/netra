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

	h := httpapi.NewIngestHandler(auth.NewAuthenticator(s.Pool()), s, time.Minute)
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
	if out.IntervalS != 60 {
		t.Fatalf("IntervalS = %d, want 60", out.IntervalS)
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

	h := httpapi.NewIngestHandler(auth.NewAuthenticator(authStore.Pool()), s, time.Minute)
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
