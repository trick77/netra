package httpapi_test

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/trick77/netra/internal/hub/auth"
)

func TestIntegrationUIListRendersAHost(t *testing.T) {
	srv, _ := newAdminFixture(t)
	createHost(t, srv, "web01")

	body := readBody(t, doAdmin(t, srv, http.MethodGet, "/", ""))
	if !strings.Contains(body, "web01") {
		t.Error("the host list page does not mention the host")
	}
}

func TestIntegrationUIListSaysNeverForAHostThatHasNotPosted(t *testing.T) {
	srv, _ := newAdminFixture(t)
	createHost(t, srv, "web01")

	body := readBody(t, doAdmin(t, srv, http.MethodGet, "/", ""))
	if !strings.Contains(body, "never") {
		t.Error("a host that has never posted is not shown as never seen")
	}
}

// The payoff over curl: the operator copies one line instead of transcribing a
// secret into a flag by hand.
func TestIntegrationUICreateShowsTheTokenAndTheSetupCommand(t *testing.T) {
	srv, _ := newAdminFixture(t)

	body := readBody(t, postForm(t, srv, "/ui/hosts", url.Values{"hostname": {"web01"}}))

	if !strings.Contains(body, auth.TokenPrefix) {
		t.Error("the create page does not show the minted token")
	}
	if !strings.Contains(body, "--token") {
		t.Error("the create page does not show a ready-to-paste setup-agent.sh command")
	}
	if !strings.Contains(body, "shown once") {
		t.Error("the create page does not warn that the token is not recoverable")
	}
}

// NETRA_HUB_URL is what the rendered command points at. The browser reaches
// the hub on loopback, so the request cannot reveal the name agents use.
func TestIntegrationUICreateUsesTheConfiguredHubURL(t *testing.T) {
	srv, _ := newAdminFixture(t)

	body := readBody(t, postForm(t, srv, "/ui/hosts", url.Values{"hostname": {"web01"}}))

	if !strings.Contains(body, "https://netra.example.com") {
		t.Error("the setup command does not use the configured hub URL")
	}
}

func TestIntegrationUICreateRejectsAnEmptyHostname(t *testing.T) {
	srv, _ := newAdminFixture(t)

	resp := postForm(t, srv, "/ui/hosts", url.Values{"hostname": {"   "}})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if body := readBody(t, resp); !strings.Contains(body, "hostname is required") &&
		!strings.Contains(body, "A hostname is required") {
		t.Errorf("body does not explain the problem: %s", body)
	}
}

func TestIntegrationUIRotateShowsANewToken(t *testing.T) {
	srv, _ := newAdminFixture(t)
	id, first := createHost(t, srv, "web01")

	body := readBody(t, postForm(t, srv, fmt.Sprintf("/ui/hosts/%d/token", id), url.Values{}))

	if strings.Contains(body, first) {
		t.Error("the rotate page showed the old token")
	}
	if !strings.Contains(body, auth.TokenPrefix) {
		t.Error("the rotate page does not show the new token")
	}
	if !strings.Contains(body, "previous token stopped working") {
		t.Error("the rotate page does not say the old token is dead")
	}
}

func TestIntegrationUIRotateRejectsANonNumericID(t *testing.T) {
	srv, _ := newAdminFixture(t)

	resp := postForm(t, srv, "/ui/hosts/abc/token", url.Values{})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestIntegrationUIDeleteRemovesTheHostAndRedirects(t *testing.T) {
	srv, s := newAdminFixture(t)
	id, _ := createHost(t, srv, "web01")

	resp := postForm(t, srv, fmt.Sprintf("/ui/hosts/%d/delete", id), url.Values{})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}

	var count int
	if err := s.Pool().QueryRow(context.Background(),
		`SELECT count(*) FROM hosts WHERE id = $1`, id).Scan(&count); err != nil {
		t.Fatalf("query: %v", err)
	}
	if count != 0 {
		t.Errorf("host rows = %d, want 0", count)
	}
}

// A page carrying a token must not be cacheable, and neither must the list
// that reflects state changing under the operator.
func TestIntegrationUIPagesAreNotCacheable(t *testing.T) {
	srv, _ := newAdminFixture(t)

	for _, resp := range []*http.Response{
		doAdmin(t, srv, http.MethodGet, "/", ""),
		postForm(t, srv, "/ui/hosts", url.Values{"hostname": {"web01"}}),
	} {
		if got := resp.Header.Get("Cache-Control"); got != "no-store" {
			t.Errorf("Cache-Control = %q, want no-store", got)
		}
	}
}

// html/template escapes by default. This test exists so that reaching for
// text/template later fails loudly rather than silently.
func TestIntegrationUIEscapesAHostnameThatLooksLikeMarkup(t *testing.T) {
	srv, s := newAdminFixture(t)

	if _, err := s.Pool().Exec(context.Background(),
		`INSERT INTO hosts (hostname) VALUES ('<script>alert(1)</script>')`); err != nil {
		t.Fatalf("insert host: %v", err)
	}

	body := readBody(t, doAdmin(t, srv, http.MethodGet, "/", ""))
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Error("hostname rendered unescaped")
	}
}

func TestIntegrationUIUnauthenticatedRequestRedirectsToLogin(t *testing.T) {
	srv, _ := newAdminFixture(t)

	resp, err := noRedirectClient(srv).Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })

	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}
	if got := resp.Header.Get("Location"); got != "/login" {
		t.Errorf("Location = %q, want /login", got)
	}
}

func TestIntegrationUILoginSetsASessionAndRedirects(t *testing.T) {
	srv, _ := newAdminFixture(t)

	resp := postFormUnauthenticated(t, srv, "/login", url.Values{"token": {testAdminToken}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}

	var session *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "netra_session" {
			session = c
		}
	}
	if session == nil {
		t.Fatal("no session cookie was set")
	}

	// The cookie alone must be enough to reach a guarded page.
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.AddCookie(session)

	page, err := noRedirectClient(srv).Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	t.Cleanup(func() { _ = page.Body.Close() })

	if page.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 with the session cookie", page.StatusCode)
	}
}

// A wrong token is often a right token for something else. It must not come
// back in the response.
func TestIntegrationUILoginRejectsAWrongTokenWithoutEchoingIt(t *testing.T) {
	srv, _ := newAdminFixture(t)

	resp := postFormUnauthenticated(t, srv, "/login", url.Values{"token": {"hunter2"}})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	if body := readBody(t, resp); strings.Contains(body, "hunter2") {
		t.Error("the login page echoed the submitted token back into the response")
	}
}

func TestIntegrationUILoginPageIsReachableWithoutACredential(t *testing.T) {
	srv, _ := newAdminFixture(t)

	resp, err := noRedirectClient(srv).Get(srv.URL + "/login")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 — gating the login page would loop", resp.StatusCode)
	}
}

// A browser that already has a session has no business seeing the form again:
// bouncing it to the host list is the difference between "logged in" and
// "logged in but still looking at a login box".
func TestIntegrationUILoginFormRedirectsAnAlreadyValidSession(t *testing.T) {
	srv, _ := newAdminFixture(t)

	login := postFormUnauthenticated(t, srv, "/login", url.Values{"token": {testAdminToken}})

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/login", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	for _, c := range login.Cookies() {
		req.AddCookie(c)
	}

	resp, err := noRedirectClient(srv).Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })

	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}
	if got := resp.Header.Get("Location"); got != "/" {
		t.Errorf("Location = %q, want /", got)
	}
}

// Deleting a host that is already gone is not an error worth showing: the
// operator asked for it to be absent, and it is.
func TestIntegrationUIDeleteOfAnAlreadyDeletedHostStillRedirects(t *testing.T) {
	srv, _ := newAdminFixture(t)
	id, _ := createHost(t, srv, "web01")

	first := postForm(t, srv, fmt.Sprintf("/ui/hosts/%d/delete", id), url.Values{})
	if first.StatusCode != http.StatusSeeOther {
		t.Fatalf("first delete status = %d, want 303", first.StatusCode)
	}

	second := postForm(t, srv, fmt.Sprintf("/ui/hosts/%d/delete", id), url.Values{})
	if second.StatusCode != http.StatusSeeOther {
		t.Errorf("second delete status = %d, want 303", second.StatusCode)
	}
}

// Rotating a host that was deleted in another tab must say so, not 500.
func TestIntegrationUIRotateOfADeletedHostExplainsItself(t *testing.T) {
	srv, _ := newAdminFixture(t)
	id, _ := createHost(t, srv, "web01")

	postForm(t, srv, fmt.Sprintf("/ui/hosts/%d/delete", id), url.Values{})

	resp := postForm(t, srv, fmt.Sprintf("/ui/hosts/%d/token", id), url.Values{})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if body := readBody(t, resp); !strings.Contains(body, "no longer exists") {
		t.Errorf("body does not explain the problem: %s", body)
	}
}

func TestIntegrationUILogoutClearsTheSession(t *testing.T) {
	srv, _ := newAdminFixture(t)

	resp := postFormUnauthenticated(t, srv, "/logout", url.Values{})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}

	for _, c := range resp.Cookies() {
		if c.Name == "netra_session" && c.MaxAge >= 0 {
			t.Errorf("session cookie MaxAge = %d, want negative to clear it", c.MaxAge)
		}
	}
}
