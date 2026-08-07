// Package auth mints and verifies the bearer tokens agents use.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TokenPrefix marks a netra agent token so a leaked one is identifiable.
const TokenPrefix = "nta_"

// ErrUnauthorized is returned for any authentication failure. It is
// deliberately opaque: the caller must not learn whether the host exists.
var ErrUnauthorized = errors.New("unauthorized")

// Mint generates a new agent token, returning the plaintext (shown to the
// operator exactly once) and the hash to store.
func Mint() (string, []byte, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, fmt.Errorf("read random: %w", err)
	}
	plain := TokenPrefix + base64.RawURLEncoding.EncodeToString(raw)
	return plain, Hash(plain), nil
}

// Hash reduces a token to the value stored in the database. Tokens are high
// entropy random strings, so a plain SHA-256 is appropriate — a slow KDF
// would only add per-request cost against an unguessable secret.
func Hash(plain string) []byte {
	sum := sha256.Sum256([]byte(plain))
	return sum[:]
}

// Authenticator resolves bearer tokens to host ids.
type Authenticator struct {
	pool *pgxpool.Pool
}

// NewAuthenticator builds an Authenticator over the given pool.
func NewAuthenticator(pool *pgxpool.Pool) *Authenticator {
	return &Authenticator{pool: pool}
}

// Authenticate returns the host id owning the token, or ErrUnauthorized.
func (a *Authenticator) Authenticate(ctx context.Context, bearer string) (int32, error) {
	if bearer == "" {
		return 0, ErrUnauthorized
	}

	want := Hash(bearer)

	var (
		hostID int32
		stored []byte
	)
	err := a.pool.QueryRow(ctx,
		`SELECT host_id, token_hash FROM tokens WHERE token_hash = $1`,
		want).Scan(&hostID, &stored)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrUnauthorized
	}
	if err != nil {
		return 0, fmt.Errorf("lookup token: %w", err)
	}

	// The lookup already matched on equality; the constant-time compare guards
	// against a future change that widens the query.
	if subtle.ConstantTimeCompare(stored, want) != 1 {
		return 0, ErrUnauthorized
	}

	if _, err := a.pool.Exec(ctx,
		`UPDATE tokens SET last_used_at = now() WHERE token_hash = $1`, want); err != nil {
		return 0, fmt.Errorf("touch token: %w", err)
	}

	return hostID, nil
}
