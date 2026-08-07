package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/trick77/netra/hub/internal/httpapi"
	"github.com/trick77/netra/hub/internal/store"
)

func TestIntegrationHealthReportsOK(t *testing.T) {
	s := store.OpenTest(t)
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	srv := httptest.NewServer(httpapi.NewHealthHandler(s))
	t.Cleanup(srv.Close)

	resp, err := srv.Client().Get(srv.URL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var body struct {
		Status   string `json:"status"`
		Database string `json:"database"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Status != "ok" {
		t.Fatalf("status = %q, want %q", body.Status, "ok")
	}
	if body.Database != "ok" {
		t.Fatalf("database = %q, want %q", body.Database, "ok")
	}
}
