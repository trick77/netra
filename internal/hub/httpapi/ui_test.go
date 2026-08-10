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

// The UI is mounted on "/", which is the whole server's last-resort pattern,
// so every unmatched path lands inside it. This pins what that does: an
// unknown path is gated like any other page and 404s once past the gate,
// while ingest and health -- both on more specific patterns -- keep their own
// behaviour rather than being swallowed by the fallback.
func TestIntegrationRoutingOfUnmatchedAndSiblingPaths(t *testing.T) {
	srv, _ := newAdminFixture(t)

	t.Run("unknown path without a credential goes to login", func(t *testing.T) {
		resp, err := noRedirectClient(srv).Get(srv.URL + "/nonexistent")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		t.Cleanup(func() { _ = resp.Body.Close() })

		if resp.StatusCode != http.StatusSeeOther {
			t.Errorf("status = %d, want 303", resp.StatusCode)
		}
	})

	t.Run("unknown path with a credential is a plain 404", func(t *testing.T) {
		if got := doAdmin(t, srv, http.MethodGet, "/nonexistent", "").StatusCode; got != http.StatusNotFound {
			t.Errorf("status = %d, want 404", got)
		}
	})

	t.Run("unknown admin path is a plain 404", func(t *testing.T) {
		if got := doAdmin(t, srv, http.MethodGet, "/api/v1/bogus", "").StatusCode; got != http.StatusNotFound {
			t.Errorf("status = %d, want 404", got)
		}
	})

	t.Run("health stays unauthenticated", func(t *testing.T) {
		resp, err := srv.Client().Get(srv.URL + "/api/health")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		t.Cleanup(func() { _ = resp.Body.Close() })

		if resp.StatusCode != http.StatusOK {
			t.Errorf("status = %d, want 200 — the compose healthcheck has no token", resp.StatusCode)
		}
	})

	t.Run("ingest keeps its own agent auth", func(t *testing.T) {
		resp, err := srv.Client().Post(srv.URL+"/api/agent/v1/ingest", "application/x-protobuf", nil)
		if err != nil {
			t.Fatalf("Post: %v", err)
		}
		t.Cleanup(func() { _ = resp.Body.Close() })

		// 401 from the agent authenticator, not a redirect to the UI login.
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", resp.StatusCode)
		}
	})
}

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

	if !strings.Contains(body, "--hub-url https://netra.example.com") {
		t.Error("the setup command does not pass the configured hub URL to --hub-url")
	}
}

// The rendered command must curl the script from where it actually lives.
//
// It used to be built as "<hubURL>/setup-agent.sh", and the hub serves no such
// route: it embeds templates and nothing else, and Traefik publishes only
// PathPrefix(/api/agent/). On loopback that path falls through to the UI mount
// and 303s to /login, which `curl -fsSL` FOLLOWS to a 200 — so -f never trips
// and an HTML login page gets piped to sh.
func TestIntegrationUISetupCommandCurlsTheRealScriptURL(t *testing.T) {
	srv, _ := newAdminFixture(t)

	body := readBody(t, postForm(t, srv, "/ui/hosts", url.Values{"hostname": {"web01"}}))

	const want = "https://raw.githubusercontent.com/trick77/netra/master/setup-agent.sh"
	if !strings.Contains(body, want) {
		t.Errorf("the setup command does not curl %s: %s", want, body)
	}
	if strings.Contains(body, "netra.example.com/setup-agent.sh") {
		t.Error("the setup command still curls a path the hub does not serve")
	}
}

// The command hands a live token to whatever answers on the hub URL, so the
// page must ask the operator to confirm that URL — set or unset. compose
// defaults NETRA_HUB_URL from NETRA_HOSTNAME, which means "configured" is not
// evidence that anyone checked it.
func TestIntegrationUITokenPageAlwaysAsksToConfirmTheHubURL(t *testing.T) {
	srv, _ := newAdminFixture(t)

	body := readBody(t, postForm(t, srv, "/ui/hosts", url.Values{"hostname": {"web01"}}))

	if !strings.Contains(body, "Check") || !strings.Contains(body, "netra.example.com") {
		t.Errorf("the token page does not ask the operator to confirm the hub URL: %s", body)
	}
}

// The host list must stay usable when the database is unreachable: the page
// exists to say so. An untyped nil in the template data makes {{len .Hosts}}
// fail, which truncates the response after the heading and shows nothing.
func TestIntegrationUIListRendersItsOwnFailure(t *testing.T) {
	srv, s := newAdminFixture(t)
	s.Close()

	resp := doAdmin(t, srv, http.MethodGet, "/", "")
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", resp.StatusCode)
	}
	if body := readBody(t, resp); !strings.Contains(body, "Could not read the host list") {
		t.Errorf("the error page does not explain the failure: %s", body)
	}
}

// A hostname is unique per site, not globally, so two rows can still share a
// name across sites. The id is what tells them apart, and it is what rotate
// and delete act on.
func TestIntegrationUIListShowsTheIDForSameNamedHostsAtDifferentSites(t *testing.T) {
	srv, s := newAdminFixture(t)
	ctx := context.Background()

	var zrh, fsn int32
	if err := s.Pool().QueryRow(ctx,
		`INSERT INTO sites (name) VALUES ('zrh') RETURNING id`).Scan(&zrh); err != nil {
		t.Fatalf("insert site: %v", err)
	}
	if err := s.Pool().QueryRow(ctx,
		`INSERT INTO sites (name) VALUES ('fsn1') RETURNING id`).Scan(&fsn); err != nil {
		t.Fatalf("insert site: %v", err)
	}

	var first, second struct {
		ID int32 `json:"id"`
	}
	decodeJSON(t, doAdmin(t, srv, http.MethodPost, "/api/v1/hosts",
		fmt.Sprintf(`{"hostname":"web01","site_id":%d}`, zrh)), &first)
	decodeJSON(t, doAdmin(t, srv, http.MethodPost, "/api/v1/hosts",
		fmt.Sprintf(`{"hostname":"web01","site_id":%d}`, fsn)), &second)

	body := readBody(t, doAdmin(t, srv, http.MethodGet, "/", ""))

	if !strings.Contains(body, fmt.Sprintf("<td class=\"muted\">%d</td>", first.ID)) {
		t.Errorf("the list does not show id %d", first.ID)
	}
	if !strings.Contains(body, fmt.Sprintf("<td class=\"muted\">%d</td>", second.ID)) {
		t.Errorf("the list does not show id %d", second.ID)
	}
}

// A database error that is nobody's input mistake still has to produce a page,
// not a blank response -- and it must not show the operator the SQL. Broken
// with a check constraint rather than a dead pool, so the failure is the
// insert itself and nothing else.
func TestIntegrationUICreateReportsAnUnexpectedFailure(t *testing.T) {
	srv, s := newAdminFixture(t)

	if _, err := s.Pool().Exec(context.Background(),
		`ALTER TABLE hosts ADD CONSTRAINT reject_every_insert CHECK (false) NOT VALID`); err != nil {
		t.Fatalf("break hosts: %v", err)
	}

	resp := postForm(t, srv, "/ui/hosts", url.Values{"hostname": {"web01"}})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}

	body := readBody(t, resp)
	if !strings.Contains(body, "Could not create the host") {
		t.Errorf("the page does not report the failure: %s", body)
	}
	if strings.Contains(body, "reject_every_insert") {
		t.Error("the page leaked the constraint name to the operator")
	}
}

// The operator can only fix a name collision if the page says that is what
// happened, rather than "could not create the host".
func TestIntegrationUICreateReportsANameCollision(t *testing.T) {
	srv, _ := newAdminFixture(t)
	createHost(t, srv, "web01")

	resp := postForm(t, srv, "/ui/hosts", url.Values{"hostname": {"web01"}})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if body := readBody(t, resp); !strings.Contains(body, "already exists") {
		t.Errorf("the page does not say the name is taken: %s", body)
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
