package httpapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/trick77/netra/internal/hub/auth"
	"github.com/trick77/netra/internal/hub/config"
	"github.com/trick77/netra/internal/hub/httpapi"
	"github.com/trick77/netra/internal/hub/store"
)

// A hub that cannot reach Postgres must stop reporting ready, so it is not
// sent ingest it can only 503, while staying live: a restart does not bring a
// database back.
func TestIntegrationReadyzFailsWithoutTheDatabase(t *testing.T) {
	s := store.OpenTest(t)
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	gone := store.OpenTestSibling(t, s)

	cfg := config.Config{AdminToken: testAdminToken}
	srv := httptest.NewServer(httpapi.NewRouter(auth.NewAuthenticator(gone.Pool()), gone, cfg, nil))
	t.Cleanup(srv.Close)
	gone.Close()

	for path, want := range map[string]int{"/healthz": http.StatusOK, "/readyz": http.StatusServiceUnavailable} {
		resp, err := srv.Client().Get(srv.URL + path)
		if err != nil {
			t.Fatalf("Get %s: %v", path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("%s: status = %d, want %d", path, resp.StatusCode, want)
		}
	}
}
