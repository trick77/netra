package auth_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/trick77/netra/internal/hub/auth"
	"github.com/trick77/netra/internal/hub/store"
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

// TestAuthenticateEmpty needs no database: the empty-bearer check short-
// circuits before the pool is touched, which this test also proves by
// passing a nil pool.
func TestAuthenticateEmpty(t *testing.T) {
	a := auth.NewAuthenticator(nil)
	if _, err := a.Authenticate(context.Background(), ""); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("Authenticate(empty) error = %v, want ErrUnauthorized", err)
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

	var hostID2 int32
	if err := s.Pool().QueryRow(ctx,
		`INSERT INTO hosts (hostname) VALUES ('h2') RETURNING id`).Scan(&hostID2); err != nil {
		t.Fatalf("insert second host: %v", err)
	}

	plain, hash, err := auth.Mint()
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO tokens (host_id, token_hash) VALUES ($1, $2)`, hostID, hash); err != nil {
		t.Fatalf("insert token: %v", err)
	}

	plain2, hash2, err := auth.Mint()
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO tokens (host_id, token_hash) VALUES ($1, $2)`, hostID2, hash2); err != nil {
		t.Fatalf("insert second token: %v", err)
	}

	a := auth.NewAuthenticator(s.Pool())

	got, err := a.Authenticate(ctx, plain)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if got != hostID {
		t.Fatalf("host id = %d, want %d", got, hostID)
	}

	got2, err := a.Authenticate(ctx, plain2)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if got2 != hostID2 {
		t.Fatalf("host id = %d, want %d", got2, hostID2)
	}
	if got2 == got {
		t.Fatalf("second token resolved to the first host: %d", got2)
	}

	var lastUsedAt *time.Time
	if err := s.Pool().QueryRow(ctx,
		`SELECT last_used_at FROM tokens WHERE token_hash = $1`, hash).Scan(&lastUsedAt); err != nil {
		t.Fatalf("select last_used_at: %v", err)
	}
	if lastUsedAt == nil {
		t.Fatal("last_used_at was not populated after a successful Authenticate")
	}

	if _, err := a.Authenticate(ctx, "nta_wrong"); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("Authenticate(wrong) error = %v, want ErrUnauthorized", err)
	}
	if _, err := a.Authenticate(ctx, ""); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("Authenticate(empty) error = %v, want ErrUnauthorized", err)
	}
}
