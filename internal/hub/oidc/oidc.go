// Package oidc is the hub's OpenID Connect client: browser sign-in against an
// external identity provider, in place of typing the admin token.
//
// Deliberately thin. It resolves an identity and nothing else -- no roles, no
// group mapping, no user records. Netra has a single role, so every user who
// completes a login is an admin, and who is allowed to complete one at all is
// the provider's decision, expressed there as a policy on this client. Adding a
// groups claim here would mean reading something with nothing to decide.
package oidc

import (
	"context"
	"errors"
	"fmt"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// ErrNonceMismatch is returned when the ID token's nonce is not the one minted
// for this login. It is separate from a generic failure because it is the one
// error that means "replay", not "misconfiguration".
var ErrNonceMismatch = errors.New("oidc: id token nonce does not match")

// Identity is what a completed login yields. Subject is the provider's stable
// identifier; the rest is display material and may be empty.
type Identity struct {
	Subject           string
	PreferredUsername string
	Email             string
	Name              string
}

// Username is the name to show and to record in the session. It prefers the
// human-chosen handle, falling back through email to the opaque subject so the
// UI never renders an empty user.
func (i Identity) Username() string {
	switch {
	case i.PreferredUsername != "":
		return i.PreferredUsername
	case i.Email != "":
		return i.Email
	default:
		return i.Subject
	}
}

// Service is a configured OIDC client. The zero value is not usable; call New.
type Service struct {
	oauth    oauth2.Config
	verifier *gooidc.IDTokenVerifier
}

// New performs discovery against the issuer and returns a ready client.
//
// Discovery is a network call made once at startup rather than per login: a
// provider that is unreachable should stop the hub from claiming it supports
// sign-in, not fail the first person who tries. The caller decides whether that
// is fatal.
func New(ctx context.Context, issuer, clientID, clientSecret, redirectURL string) (*Service, error) {
	provider, err := gooidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery: %w", err)
	}

	return &Service{
		oauth: oauth2.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			RedirectURL:  redirectURL,
			Endpoint:     provider.Endpoint(),
			// No "groups": netra has one role, so the claim would be requested
			// and then ignored. Asking for less also means one less thing to
			// configure at the provider.
			Scopes: []string{gooidc.ScopeOpenID, "profile", "email"},
		},
		verifier: provider.Verifier(&gooidc.Config{ClientID: clientID}),
	}, nil
}

// AuthCodeURL is where the browser is sent to sign in. state and nonce are
// generated per login by the caller and echoed back for checking.
func (s *Service) AuthCodeURL(state, nonce string) string {
	return s.oauth.AuthCodeURL(state, gooidc.Nonce(nonce))
}

// Exchange trades the callback's code for a verified identity.
//
// It verifies the ID token's signature, audience and expiry via the provider's
// JWKS, then checks the nonce. The access token is deliberately discarded: the
// hub has no upstream API to call on the user's behalf, so keeping it would be
// storing a credential with no purpose.
func (s *Service) Exchange(ctx context.Context, code, nonce string) (Identity, error) {
	tok, err := s.oauth.Exchange(ctx, code)
	if err != nil {
		return Identity{}, fmt.Errorf("oidc code exchange: %w", err)
	}

	raw, ok := tok.Extra("id_token").(string)
	if !ok || raw == "" {
		return Identity{}, errors.New("oidc: token response carried no id_token")
	}

	idToken, err := s.verifier.Verify(ctx, raw)
	if err != nil {
		return Identity{}, fmt.Errorf("oidc verify id token: %w", err)
	}

	// Checked after verification, never before: an unverified token's nonce
	// proves nothing, and comparing it first would invite treating a forged
	// token as merely stale.
	if idToken.Nonce != nonce {
		return Identity{}, ErrNonceMismatch
	}

	var claims struct {
		PreferredUsername string `json:"preferred_username"`
		Email             string `json:"email"`
		Name              string `json:"name"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return Identity{}, fmt.Errorf("oidc decode claims: %w", err)
	}

	return Identity{
		Subject:           idToken.Subject,
		PreferredUsername: claims.PreferredUsername,
		Email:             claims.Email,
		Name:              claims.Name,
	}, nil
}
