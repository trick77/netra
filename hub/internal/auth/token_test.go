package auth_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/trick77/netra/hub/internal/auth"
	"github.com/trick77/netra/hub/internal/store"
)

func TestMintProducesPrefixedTokenAndMatchingHash(t *testing.T) {
	plain, hash, err := auth.Mint()
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if !strings.HasPrefix(plain, "nta_") {
		t.Fatalf("token = %q, want the nta_ prefix", plain)
	}
	if len(hash) != 32 {
		t.Fatalf("len(hash) = %d, want 32", len(hash))
	}
	if !bytes.Equal(hash, auth.Hash(plain)) {
		t.Fatal("Hash(plain) does not match the hash returned by Mint")
	}
}

func TestMintIsUnique(t *testing.T) {
	a, _, err := auth.Mint()
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	b, _, err := auth.Mint()
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if a == b {
		t.Fatal("two Mint calls produced the same token")
	}
}

func TestIntegrationAuthenticateResolvesHostID(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	var hostID int32
	if err := s.Pool().QueryRow(ctx,
		`INSERT INTO hosts (hostname) VALUES ('h1') RETURNING id`).Scan(&hostID); err != nil {
		t.Fatalf("insert host: %v", err)
	}

	plain, hash, err := auth.Mint()
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO tokens (host_id, token_hash) VALUES ($1, $2)`, hostID, hash); err != nil {
		t.Fatalf("insert token: %v", err)
	}

	a := auth.NewAuthenticator(s.Pool())

	got, err := a.Authenticate(ctx, plain)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if got != hostID {
		t.Fatalf("host id = %d, want %d", got, hostID)
	}

	if _, err := a.Authenticate(ctx, "nta_wrong"); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("Authenticate(wrong) error = %v, want ErrUnauthorized", err)
	}
	if _, err := a.Authenticate(ctx, ""); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("Authenticate(empty) error = %v, want ErrUnauthorized", err)
	}
}
