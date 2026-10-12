package httpapi_test

import (
	"net/http"
	"testing"

	"github.com/trick77/netra/internal/hub/web"
)

// The icons and the manifest must sit outside RequireAdmin: a signed-out
// browser on the login page asks for them, and the catch-all answers an
// unauthenticated asset request with a redirect back to that same page -- a
// blank tab on the one screen every user meets first.
//
// The assertion is "not a redirect", not "200". dist/ is tracked as an empty
// .gitkeep, so on a checkout that has not run `make ui` these files genuinely
// are not embedded and 404 is the honest answer; what must never happen is a
// 303 to /login, whatever the build state. Anything else would make this test
// pass or fail on whether someone had built the UI.
func TestIntegrationPublicAssetsAreNotBehindTheAdminGate(t *testing.T) {
	srv, _ := newAdminFixture(t)
	client := noRedirectClient(srv)

	for _, p := range web.PublicAssetPaths {
		resp, err := client.Get(srv.URL + "/" + p)
		if err != nil {
			t.Fatalf("get %s: %v", p, err)
		}
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound {
			t.Errorf("/%s: status = %d, want 200 or 404 -- never the login redirect", p, resp.StatusCode)
		}
		if loc := resp.Header.Get("Location"); loc != "" {
			t.Errorf("/%s: redirected to %q; it must be reachable without a session", p, loc)
		}
	}
}

// The counterpart, so the test above cannot pass by the gate having been
// removed altogether: a path that is NOT on the allowlist still redirects a
// browser with no session to the login page.
func TestIntegrationNonPublicPathStillRedirectsToLogin(t *testing.T) {
	srv, _ := newAdminFixture(t)
	client := noRedirectClient(srv)

	resp, err := client.Get(srv.URL + "/hosts")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}
	if got := resp.Header.Get("Location"); got != "/login" {
		t.Errorf("Location = %q, want /login", got)
	}
}
