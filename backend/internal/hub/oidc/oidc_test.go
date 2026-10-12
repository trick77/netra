package oidc_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/trick77/netra/internal/hub/oidc"
)

// fakeProvider is the smallest thing go-oidc will accept as an issuer:
// discovery, a JWKS, and a token endpoint that mints an ID token we control.
//
// A real provider is not reachable from a unit test, and stubbing at the
// package boundary would test the stub. This exercises the parts that actually
// go wrong -- signature verification, audience, nonce -- against a token that
// really is signed.
type fakeProvider struct {
	server *httptest.Server
	key    *rsa.PrivateKey
	// claims is what the next token endpoint call will mint. Tests mutate it.
	claims jwt.Claims
	nonce  string
	extra  map[string]any
}

func newFakeProvider(t *testing.T, clientID string) *fakeProvider {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	p := &fakeProvider{key: key, extra: map[string]any{}}
	mux := http.NewServeMux()

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"issuer":                                p.server.URL,
			"authorization_endpoint":                p.server.URL + "/authorize",
			"token_endpoint":                        p.server.URL + "/token",
			"jwks_uri":                              p.server.URL + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})

	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
			Key: key.Public(), KeyID: "test", Algorithm: "RS256", Use: "sig",
		}}})
	})

	mux.HandleFunc("/token", func(w http.ResponseWriter, _ *http.Request) {
		signer, err := jose.NewSigner(
			jose.SigningKey{Algorithm: jose.RS256, Key: key},
			(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "test"))
		if err != nil {
			t.Errorf("signer: %v", err)
			return
		}

		claims := p.claims
		if claims.Issuer == "" {
			claims = jwt.Claims{
				Issuer:   p.server.URL,
				Subject:  "user-subject",
				Audience: jwt.Audience{clientID},
				Expiry:   jwt.NewNumericDate(time.Now().Add(time.Hour)),
				IssuedAt: jwt.NewNumericDate(time.Now()),
			}
		}

		private := map[string]any{"nonce": p.nonce}
		for k, v := range p.extra {
			private[k] = v
		}

		raw, err := jwt.Signed(signer).Claims(claims).Claims(private).Serialize()
		if err != nil {
			t.Errorf("sign: %v", err)
			return
		}
		writeJSON(w, map[string]any{
			"access_token": "unused-by-netra",
			"token_type":   "Bearer",
			"id_token":     raw,
		})
	})

	p.server = httptest.NewServer(mux)
	t.Cleanup(p.server.Close)
	return p
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func TestNewRejectsUnreachableIssuer(t *testing.T) {
	// Discovery happens at construction, so an issuer that does not answer is
	// caught at startup rather than by whoever tries to log in first.
	_, err := oidc.New(context.Background(), "http://127.0.0.1:1", "id", "secret", "https://hub/auth/callback")
	if err == nil {
		t.Fatal("expected discovery against a dead issuer to fail")
	}
}

func TestAuthCodeURLCarriesStateAndNonce(t *testing.T) {
	p := newFakeProvider(t, "netra")
	svc, err := oidc.New(context.Background(), p.server.URL, "netra", "secret", "https://hub/auth/callback")
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	raw := svc.AuthCodeURL("the-state", "the-nonce")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	q := u.Query()

	if got := q.Get("state"); got != "the-state" {
		t.Errorf("state = %q, want %q", got, "the-state")
	}
	if got := q.Get("nonce"); got != "the-nonce" {
		t.Errorf("nonce = %q, want %q", got, "the-nonce")
	}
	if got := q.Get("redirect_uri"); got != "https://hub/auth/callback" {
		t.Errorf("redirect_uri = %q", got)
	}
	// No groups scope: netra has one role, so the claim would be requested and
	// then ignored. Asserted so re-adding it is a deliberate act.
	if scope := q.Get("scope"); strings.Contains(scope, "groups") {
		t.Errorf("scope %q should not request groups", scope)
	}
}

func TestExchangeReturnsIdentity(t *testing.T) {
	p := newFakeProvider(t, "netra")
	p.nonce = "the-nonce"
	p.extra = map[string]any{
		"preferred_username": "jan",
		"email":              "jan@example.com",
		"name":               "Jan Saner",
	}

	svc, err := oidc.New(context.Background(), p.server.URL, "netra", "secret", "https://hub/auth/callback")
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	id, err := svc.Exchange(context.Background(), "the-code", "the-nonce")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}

	if id.Subject != "user-subject" {
		t.Errorf("subject = %q", id.Subject)
	}
	if id.Username() != "jan" {
		t.Errorf("username = %q, want jan", id.Username())
	}
	if id.Email != "jan@example.com" {
		t.Errorf("email = %q", id.Email)
	}
}

func TestExchangeRejectsNonceMismatch(t *testing.T) {
	p := newFakeProvider(t, "netra")
	p.nonce = "minted-for-another-login"

	svc, err := oidc.New(context.Background(), p.server.URL, "netra", "secret", "https://hub/auth/callback")
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	_, err = svc.Exchange(context.Background(), "the-code", "the-nonce")
	if !errors.Is(err, oidc.ErrNonceMismatch) {
		t.Fatalf("err = %v, want ErrNonceMismatch", err)
	}
}

func TestExchangeRejectsWrongAudience(t *testing.T) {
	// A token minted for a different client must not authenticate here, even
	// though it is correctly signed by the same provider.
	p := newFakeProvider(t, "netra")
	p.nonce = "the-nonce"
	p.claims = jwt.Claims{
		Issuer:   "", // filled below
		Subject:  "user-subject",
		Audience: jwt.Audience{"some-other-client"},
		Expiry:   jwt.NewNumericDate(time.Now().Add(time.Hour)),
		IssuedAt: jwt.NewNumericDate(time.Now()),
	}

	svc, err := oidc.New(context.Background(), p.server.URL, "netra", "secret", "https://hub/auth/callback")
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	p.claims.Issuer = p.server.URL

	if _, err := svc.Exchange(context.Background(), "the-code", "the-nonce"); err == nil {
		t.Fatal("expected a token for another audience to be rejected")
	}
}

func TestUsernameFallsBack(t *testing.T) {
	// The UI must never render an empty user, so Username walks down to the
	// opaque subject rather than returning "".
	cases := []struct {
		name string
		id   oidc.Identity
		want string
	}{
		{"prefers preferred_username", oidc.Identity{Subject: "s", PreferredUsername: "jan", Email: "e"}, "jan"},
		{"falls back to email", oidc.Identity{Subject: "s", Email: "e@x"}, "e@x"},
		{"falls back to subject", oidc.Identity{Subject: "s"}, "s"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.id.Username(); got != tc.want {
				t.Errorf("Username() = %q, want %q", got, tc.want)
			}
		})
	}
}
