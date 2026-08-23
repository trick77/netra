package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/trick77/netra/internal/hub/auth"
	"github.com/trick77/netra/internal/hub/config"
	"github.com/trick77/netra/internal/hub/httpapi"
	"github.com/trick77/netra/internal/hub/store"
)

// testAdminToken is the NETRA_ADMIN_TOKEN the admin fixture is built with.
const testAdminToken = "test-admin-token"

// newAdminFixture builds a hub serving the full router -- ingest, health,
// admin API and UI -- against a fresh schema.
//
// It deliberately does not pre-create a host: the admin tests create their
// own through the API under test, so nothing passes because of fixture setup.
func newAdminFixture(t *testing.T) (*httptest.Server, *store.Store) {
	t.Helper()

	s := store.OpenTest(t)
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	cfg := config.Config{AdminToken: testAdminToken, HubURL: "https://netra.example.com"}
	h := httpapi.NewRouter(auth.NewAuthenticator(s.Pool()), s, cfg, nil)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	return srv, s
}

// noRedirectClient returns a client that surfaces 3xx responses instead of
// following them, so a redirect is observable.
func noRedirectClient(srv *httptest.Server) *http.Client {
	c := srv.Client()
	return &http.Client{
		Transport: c.Transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// doAdmin issues an authenticated admin request. Every admin test is
// authenticated unless it deliberately is not.
func doAdmin(t *testing.T, srv *httptest.Server, method, path, body string) *http.Response {
	t.Helper()

	req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+testAdminToken)

	resp, err := noRedirectClient(srv).Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// postForm submits a urlencoded form to the UI, authenticated.
func postForm(t *testing.T, srv *httptest.Server, path string, form url.Values) *http.Response {
	t.Helper()

	req, err := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+testAdminToken)

	resp, err := noRedirectClient(srv).Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// postFormUnauthenticated submits a form with no credential, for the login
// and redirect paths.
func postFormUnauthenticated(t *testing.T, srv *httptest.Server, path string, form url.Values) *http.Response {
	t.Helper()

	req, err := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := noRedirectClient(srv).Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(raw)
}

func decodeJSON(t *testing.T, resp *http.Response, out any) {
	t.Helper()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
}

// createHost is the shorthand the tests that are not about creation use.
func createHost(t *testing.T, srv *httptest.Server, hostname string) (int32, string) {
	t.Helper()

	resp := doAdmin(t, srv, http.MethodPost, "/api/v1/hosts", `{"hostname":"`+hostname+`"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create %s: status = %d, want 201", hostname, resp.StatusCode)
	}

	var out struct {
		ID    int32  `json:"id"`
		Token string `json:"token"`
	}
	decodeJSON(t, resp, &out)
	return out.ID, out.Token
}
