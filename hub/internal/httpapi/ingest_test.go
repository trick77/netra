package httpapi_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/trick77/netra/hub/internal/auth"
	"github.com/trick77/netra/hub/internal/httpapi"
	"github.com/trick77/netra/hub/internal/store"
	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
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
