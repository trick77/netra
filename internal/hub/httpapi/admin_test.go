package httpapi_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/trick77/netra/internal/hub/auth"
)

// "Shown once" is a property of the whole system, not of one handler: it is
// worth nothing if the plaintext is also readable from the database or from
// the list endpoint.
func TestIntegrationAdminCreateHostReturnsTheTokenOnce(t *testing.T) {
	srv, s := newAdminFixture(t)

	resp := doAdmin(t, srv, http.MethodPost, "/api/v1/hosts", `{"hostname":"web01"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}

	var created struct {
		ID       int32  `json:"id"`
		Hostname string `json:"hostname"`
		Token    string `json:"token"`
	}
	decodeJSON(t, resp, &created)

	if !strings.HasPrefix(created.Token, auth.TokenPrefix) {
		t.Fatalf("token = %q, want the %q prefix", created.Token, auth.TokenPrefix)
	}
	if created.Hostname != "web01" {
		t.Errorf("hostname = %q, want web01", created.Hostname)
	}

	var stored []byte
	if err := s.Pool().QueryRow(context.Background(),
		`SELECT token_hash FROM tokens WHERE host_id = $1`, created.ID).Scan(&stored); err != nil {
		t.Fatalf("query token: %v", err)
	}
	if string(stored) == created.Token {
		t.Fatal("the plaintext token was stored in the database")
	}

	list := readBody(t, doAdmin(t, srv, http.MethodGet, "/api/v1/hosts", ""))
	if strings.Contains(list, created.Token) {
		t.Fatal("GET /api/v1/hosts leaked the plaintext token")
	}
}

// A token in a cached response is a leaked token.
func TestIntegrationAdminCreateHostIsNotCacheable(t *testing.T) {
	srv, _ := newAdminFixture(t)

	resp := doAdmin(t, srv, http.MethodPost, "/api/v1/hosts", `{"hostname":"web01"}`)

	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want %q", got, "no-store")
	}
}

func TestIntegrationAdminCreateHostRejectsAnEmptyHostname(t *testing.T) {
	srv, _ := newAdminFixture(t)

	resp := doAdmin(t, srv, http.MethodPost, "/api/v1/hosts", `{"hostname":""}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestIntegrationAdminCreateHostRejectsMalformedJSON(t *testing.T) {
	srv, _ := newAdminFixture(t)

	resp := doAdmin(t, srv, http.MethodPost, "/api/v1/hosts", `{"hostname":`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestIntegrationAdminListHostsReportsLastSeen(t *testing.T) {
	srv, s := newAdminFixture(t)
	id, _ := createHost(t, srv, "web01")

	if _, err := s.Pool().Exec(context.Background(),
		`INSERT INTO host_current (host_id, last_seen) VALUES ($1, now())`, id); err != nil {
		t.Fatalf("insert host_current: %v", err)
	}

	var hosts []struct {
		ID       int32   `json:"id"`
		Hostname string  `json:"hostname"`
		LastSeen *string `json:"last_seen"`
	}
	decodeJSON(t, doAdmin(t, srv, http.MethodGet, "/api/v1/hosts", ""), &hosts)

	if len(hosts) != 1 {
		t.Fatalf("len = %d, want 1", len(hosts))
	}
	if hosts[0].LastSeen == nil {
		t.Error("last_seen is null, want the host_current timestamp")
	}
}

// A host that has never posted has no last_seen. Absent is null, never a zero
// timestamp that would read as 1970.
func TestIntegrationAdminListHostsReportsNullLastSeenForANewHost(t *testing.T) {
	srv, _ := newAdminFixture(t)
	createHost(t, srv, "web01")

	body := readBody(t, doAdmin(t, srv, http.MethodGet, "/api/v1/hosts", ""))
	if !strings.Contains(body, `"last_seen":null`) {
		t.Errorf("body = %s, want a null last_seen", body)
	}
}

func TestIntegrationAdminRotateReturnsANewWorkingToken(t *testing.T) {
	srv, s := newAdminFixture(t)
	id, first := createHost(t, srv, "web01")

	resp := doAdmin(t, srv, http.MethodPost, fmt.Sprintf("/api/v1/hosts/%d/token", id), "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var out struct {
		Token string `json:"token"`
	}
	decodeJSON(t, resp, &out)

	a := auth.NewAuthenticator(s.Pool())
	if _, err := a.Authenticate(context.Background(), out.Token); err != nil {
		t.Errorf("the rotated token does not authenticate: %v", err)
	}
	if _, err := a.Authenticate(context.Background(), first); err == nil {
		t.Error("the pre-rotation token still authenticates")
	}
}

func TestIntegrationAdminRotateUnknownHostIs404(t *testing.T) {
	srv, _ := newAdminFixture(t)

	resp := doAdmin(t, srv, http.MethodPost, "/api/v1/hosts/4242/token", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestIntegrationAdminRotateRejectsANonNumericID(t *testing.T) {
	srv, _ := newAdminFixture(t)

	resp := doAdmin(t, srv, http.MethodPost, "/api/v1/hosts/abc/token", "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestIntegrationAdminDeleteHostIs204ThenGone(t *testing.T) {
	srv, _ := newAdminFixture(t)
	id, _ := createHost(t, srv, "web01")

	del := doAdmin(t, srv, http.MethodDelete, fmt.Sprintf("/api/v1/hosts/%d", id), "")
	if del.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", del.StatusCode)
	}

	again := doAdmin(t, srv, http.MethodDelete, fmt.Sprintf("/api/v1/hosts/%d", id), "")
	if again.StatusCode != http.StatusNotFound {
		t.Errorf("second delete status = %d, want 404", again.StatusCode)
	}
}

func TestIntegrationAdminDeleteRejectsANonNumericID(t *testing.T) {
	srv, _ := newAdminFixture(t)

	resp := doAdmin(t, srv, http.MethodDelete, "/api/v1/hosts/abc", "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// The admin API is gated even though the port is loopback-only: the binding
// is a second line of defence, not the only one.
func TestIntegrationAdminRequiresACredential(t *testing.T) {
	srv, _ := newAdminFixture(t)

	resp, err := srv.Client().Get(srv.URL + "/api/v1/hosts")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 without a credential", resp.StatusCode)
	}
}

// Agent ingest keeps its own per-host tokens. The admin token must not be a
// skeleton key for it, and an agent token must not reach the admin API.
func TestIntegrationAdminTokenDoesNotAuthenticateIngest(t *testing.T) {
	srv, _ := newAdminFixture(t)
	_, agentToken := createHost(t, srv, "web01")

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/hosts", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+agentToken)

	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 — an agent token must not reach the admin API", resp.StatusCode)
	}
}
