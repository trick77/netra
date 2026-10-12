package httpapi_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/trick77/netra/internal/hub/httpapi"
	"github.com/trick77/netra/internal/hub/oidc"
)

// stubProvider is a minimal but genuine OpenID provider: real RSA signature,
// real JWKS, real discovery. Stubbing at the package boundary instead would
// test the stub -- these tests exist to pin the handler's behaviour against
// tokens that actually verify, and against ones that deliberately do not.
type stubProvider struct {
	server *httptest.Server
	nonce  func() string
}

func newStubProvider(t *testing.T, clientID string) *stubProvider {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	p := &stubProvider{}
	mux := http.NewServeMux()

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                p.server.URL,
			"authorization_endpoint":                p.server.URL + "/authorize",
			"token_endpoint":                        p.server.URL + "/token",
			"jwks_uri":                              p.server.URL + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
			Key: key.Public(), KeyID: "k", Algorithm: "RS256", Use: "sig",
		}}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, _ *http.Request) {
		signer, _ := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key},
			(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "k"))
		raw, _ := jwt.Signed(signer).Claims(jwt.Claims{
			Issuer:   p.server.URL,
			Subject:  "subject-1",
			Audience: jwt.Audience{clientID},
			Expiry:   jwt.NewNumericDate(time.Now().Add(time.Hour)),
			IssuedAt: jwt.NewNumericDate(time.Now()),
		}).Claims(map[string]any{
			"nonce":              p.nonce(),
			"preferred_username": "jan",
		}).Serialize()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": "unused", "token_type": "Bearer", "id_token": raw,
		})
	})

	p.server = httptest.NewServer(mux)
	t.Cleanup(p.server.Close)
	return p
}

// signedInHandler wires a login handler to a working provider.
func signedInHandler(t *testing.T, p *stubProvider) http.Handler {
	t.Helper()
	svc, err := oidc.New(context.Background(), p.server.URL, "netra", "secret", "https://hub/auth/callback")
	if err != nil {
		t.Fatalf("oidc.New: %v", err)
	}
	return httpapi.NewLoginHandler("admin-token", svc)
}

func TestSignInStartRedirectsAndMintsFlowCookies(t *testing.T) {
	p := newStubProvider(t, "netra")
	p.nonce = func() string { return "" }
	srv := httptest.NewServer(signedInHandler(t, p))
	t.Cleanup(srv.Close)

	resp, err := noRedirect(t).Get(srv.URL + "/auth/login")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}

	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatalf("parse location: %v", err)
	}
	if loc.Query().Get("state") == "" || loc.Query().Get("nonce") == "" {
		t.Error("authorization URL is missing state or nonce")
	}

	var state, nonce *http.Cookie
	for _, c := range resp.Cookies() {
		switch c.Name {
		case "netra_oidc_state":
			state = c
		case "netra_oidc_nonce":
			nonce = c
		}
	}
	if state == nil || nonce == nil {
		t.Fatal("flow cookies were not set")
	}
	// Lax, not Strict: the callback is a top-level navigation from the
	// provider's origin, and Strict would withhold these exactly then.
	if state.SameSite != http.SameSiteLaxMode {
		t.Errorf("state cookie SameSite = %v, want Lax", state.SameSite)
	}
	if !state.HttpOnly || !state.Secure {
		t.Error("flow cookies must be HttpOnly and Secure")
	}
	if state.Value != loc.Query().Get("state") {
		t.Error("state cookie does not match the state sent to the provider")
	}
}

func TestCallbackCompletesAndMintsASignedInSession(t *testing.T) {
	p := newStubProvider(t, "netra")
	srv := httptest.NewServer(signedInHandler(t, p))
	t.Cleanup(srv.Close)

	jar, _ := cookiejar.New(nil)
	client := &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	start, err := client.Get(srv.URL + "/auth/login")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	start.Body.Close()

	loc, _ := url.Parse(start.Header.Get("Location"))
	state := loc.Query().Get("state")
	// The provider echoes back the nonce it was given, as a real one does.
	p.nonce = func() string { return loc.Query().Get("nonce") }

	resp, err := client.Get(srv.URL + "/auth/callback?code=the-code&state=" + url.QueryEscape(state))
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}
	if got := resp.Header.Get("Location"); got != "/" {
		t.Errorf("Location = %q, want /", got)
	}

	var session *http.Cookie
	for _, c := range resp.Cookies() {
		switch c.Name {
		case "netra_session":
			session = c
		case "netra_oidc_state", "netra_oidc_nonce":
			// Single-use: leaving them set would let a captured callback URL
			// be replayed for as long as the cookies lived.
			if c.MaxAge >= 0 {
				t.Errorf("%s was not cleared after the flow", c.Name)
			}
		}
	}
	if session == nil {
		t.Fatal("no session cookie was minted")
	}

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(session)
	user, ok := httpapi.SessionUserForTest("admin-token", r, time.Now())
	if !ok {
		t.Fatal("minted session does not validate")
	}
	if user != "jan" {
		t.Errorf("session user = %q, want jan", user)
	}
}

func TestCallbackRejectsAForgedState(t *testing.T) {
	// The state cookie is what proves the flow started here. A callback whose
	// state does not match it is someone else's redirect, or an attack.
	p := newStubProvider(t, "netra")
	srv := httptest.NewServer(signedInHandler(t, p))
	t.Cleanup(srv.Close)

	jar, _ := cookiejar.New(nil)
	client := &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	start, err := client.Get(srv.URL + "/auth/login")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	start.Body.Close()
	p.nonce = func() string { return "irrelevant" }

	resp, err := client.Get(srv.URL + "/auth/callback?code=the-code&state=not-the-state")
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
	for _, c := range resp.Cookies() {
		if c.Name == "netra_session" && c.Value != "" {
			t.Fatal("a forged state minted a session")
		}
	}
}

func TestCallbackSurfacesAProviderRefusal(t *testing.T) {
	// access_denied is the user being told no, not a fault to debug.
	p := newStubProvider(t, "netra")
	p.nonce = func() string { return "" }
	srv := httptest.NewServer(signedInHandler(t, p))
	t.Cleanup(srv.Close)

	resp, err := noRedirect(t).Get(srv.URL + "/auth/callback?error=access_denied")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestLoginPageOffersSignInWhenConfigured(t *testing.T) {
	p := newStubProvider(t, "netra")
	p.nonce = func() string { return "" }
	srv := httptest.NewServer(signedInHandler(t, p))
	t.Cleanup(srv.Close)

	resp, err := noRedirect(t).Get(srv.URL + "/login")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	body := string(raw)
	if !strings.Contains(body, "/auth/login") {
		t.Error("login page does not offer sign-in")
	}
	// The token form stays reachable: it is the way back in when the provider
	// is the thing that is down.
	if !strings.Contains(body, `name="token"`) {
		t.Error("login page must keep the admin token form")
	}
}
