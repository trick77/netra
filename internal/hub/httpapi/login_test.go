package httpapi_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// The login page and the session it mints are all that is left of the
// server-rendered UI: the SPA (internal/hub/web) owns every other page now,
// and posts to this same endpoint with the same form encoding and the same
// cookie. These tests moved here with the handlers when ui.go was retired --
// what they pin is unchanged, and it is the one page that has to keep
// working when the rest of the UI cannot.

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

	// 200 and the SPA's index.html, NOT a 404: every unmatched path is a
	// client-side route now (internal/hub/web serves index.html for anything
	// that is not a file), which is what makes reloading /hosts/3/graphs come
	// back as the app. The SPA renders its own "no such page" for a path it
	// does not recognise -- the server cannot know which those are.
	t.Run("unknown path with a credential is served the SPA", func(t *testing.T) {
		resp := doAdmin(t, srv, http.MethodGet, "/hosts/3/graphs", "")
		if resp.StatusCode != http.StatusOK {
			t.Errorf("status = %d, want 200 so a deep link survives a reload", resp.StatusCode)
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

func TestIntegrationUnauthenticatedRequestRedirectsToLogin(t *testing.T) {
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

func TestIntegrationLoginSetsASessionAndRedirects(t *testing.T) {
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

func TestIntegrationLoginRejectsAWrongTokenWithoutEchoingIt(t *testing.T) {
	srv, _ := newAdminFixture(t)

	resp := postFormUnauthenticated(t, srv, "/login", url.Values{"token": {"hunter2"}})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	if body := readBody(t, resp); strings.Contains(body, "hunter2") {
		t.Error("the login page echoed the submitted token back into the response")
	}
}

func TestIntegrationLoginPageIsReachableWithoutACredential(t *testing.T) {
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

func TestIntegrationLoginFormRedirectsAnAlreadyValidSession(t *testing.T) {
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

func TestIntegrationLogoutClearsTheSession(t *testing.T) {
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
