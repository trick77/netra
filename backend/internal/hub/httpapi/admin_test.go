package httpapi_test

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/trick77/ship/kit/httpx"
	"github.com/trick77/ship/kit/logging"

	"github.com/trick77/netra/internal/hub/auth"
	"github.com/trick77/netra/internal/hub/httpapi"
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

// The body is read under a cap, so an admin session cannot be used to make the
// hub buffer an arbitrarily large request.
func TestIntegrationAdminCreateHostRejectsAnOversizedBody(t *testing.T) {
	srv, _ := newAdminFixture(t)

	body := `{"hostname":"` + strings.Repeat("a", 8<<10) + `"}`
	resp := doAdmin(t, srv, http.MethodPost, "/api/v1/hosts", body)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", resp.StatusCode)
	}
}

// A database failure is a 500 with a fixed phrase, on both halves of the API:
// the driver's error names tables and columns, and it stays in the log.
func TestIntegrationAdminDatabaseFailureIsABare500(t *testing.T) {
	srv, s := newAdminFixture(t)
	if _, err := s.Pool().Exec(context.Background(), `ALTER TABLE hosts RENAME TO hosts_gone`); err != nil {
		t.Fatalf("rename hosts: %v", err)
	}

	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/v1/hosts", `{"hostname":"web01"}`}, // admin
		{http.MethodGet, "/api/v1/hosts", ""},                      // read
	} {
		resp := doAdmin(t, srv, tc.method, tc.path, tc.body)
		if resp.StatusCode != http.StatusInternalServerError {
			t.Errorf("%s %s: status = %d, want 500", tc.method, tc.path, resp.StatusCode)
		}
		if body := readBody(t, resp); strings.Contains(body, "hosts") {
			t.Errorf("%s %s: body leaks the database error: %s", tc.method, tc.path, body)
		}
	}
}

// The 500's log line carries the request id the response echoes, which is
// what joins it to the access line for the same request.
func TestIntegrationAdmin500LogLineCarriesTheRequestID(t *testing.T) {
	// Given a captured default logger, installed before the router is built
	var logs syncBuffer
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	if _, err := logging.Setup(logging.Options{Level: "debug", Writer: &logs}); err != nil {
		t.Fatalf("logging.Setup: %v", err)
	}
	srv, s := newAdminFixture(t)
	if _, err := s.Pool().Exec(context.Background(), `ALTER TABLE hosts RENAME TO hosts_gone`); err != nil {
		t.Fatalf("rename hosts: %v", err)
	}

	// When a request fails on the database
	resp := doAdmin(t, srv, http.MethodGet, "/api/v1/hosts", "")
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", resp.StatusCode)
	}

	// Then the "request failed" line names the response's request id
	id := resp.Header.Get(httpx.RequestIDHeader)
	if id == "" {
		t.Fatalf("response has no %s header", httpx.RequestIDHeader)
	}
	var line string
	for _, l := range strings.Split(logs.String(), "\n") {
		if strings.Contains(l, `msg="request failed"`) {
			line = l
			break
		}
	}
	if line == "" {
		t.Fatalf("no request failed line in:\n%s", logs.String())
	}
	if !strings.Contains(line, "request_id="+id) {
		t.Errorf("request failed line lacks request_id=%s: %s", id, line)
	}
}

// syncBuffer is a bytes.Buffer safe to read while the server goroutine is
// still writing the access line.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// A name already taken at that site is a 409 the operator can act on, not a
// 500 that reads as "the hub is broken".
func TestIntegrationAdminDuplicateHostnameIs409(t *testing.T) {
	srv, _ := newAdminFixture(t)
	createHost(t, srv, "web01")

	resp := doAdmin(t, srv, http.MethodPost, "/api/v1/hosts", `{"hostname":"web01"}`)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	if body := readBody(t, resp); !strings.Contains(body, "already exists") {
		t.Errorf("body does not say what collided: %s", body)
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

// SameSite=Lax still sends the session cookie on a form POST from a sibling
// subdomain, so a cross-origin browser request must be refused before it can
// revoke a host's token. Requests without browser headers (curl, netra-sim)
// and the UI's own same-origin requests must still pass.
func TestIntegrationAdminRefusesCrossOriginMutations(t *testing.T) {
	srv, _ := newAdminFixture(t)
	id, _ := createHost(t, srv, "web01")
	path := fmt.Sprintf("/api/v1/hosts/%d/token", id)

	for _, tc := range []struct {
		name    string
		headers map[string]string
		want    int
	}{
		{"sibling subdomain", map[string]string{"Sec-Fetch-Site": "same-site"}, http.StatusForbidden},
		{"foreign origin", map[string]string{"Origin": "https://evil.example"}, http.StatusForbidden},
		{"same origin", map[string]string{"Sec-Fetch-Site": "same-origin"}, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, srv.URL+path, nil)
			if err != nil {
				t.Fatalf("NewRequest: %v", err)
			}
			req.AddCookie(httpapi.NewSessionCookieForTest(testAdminToken, ""))
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			resp, err := noRedirectClient(srv).Do(req)
			if err != nil {
				t.Fatalf("Do: %v", err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Errorf("status = %d, want %d", resp.StatusCode, tc.want)
			}
		})
	}

	resp := doAdmin(t, srv, http.MethodPost, path, "")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("bearer with no browser headers: status = %d, want 200", resp.StatusCode)
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
