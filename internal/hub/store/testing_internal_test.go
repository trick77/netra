package store

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestIsRetryableLockError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "deadlock detected is retryable",
			err:  &pgconn.PgError{Code: deadlockSQLState},
			want: true,
		},
		{
			name: "lock_timeout is retryable",
			err:  &pgconn.PgError{Code: lockNotAvailableSQLState},
			want: true,
		},
		{
			name: "wrapped deadlock is still detected through errors.As",
			err:  fmt.Errorf("reset schema: %w", &pgconn.PgError{Code: deadlockSQLState}),
			want: true,
		},
		{
			name: "unrelated postgres error is not retryable",
			err:  &pgconn.PgError{Code: "42883"}, // undefined_function
			want: false,
		},
		{
			name: "non-postgres error is not retryable",
			err:  errors.New("connection reset by peer"),
			want: false,
		},
		{
			name: "nil error is not retryable",
			err:  nil,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRetryableLockError(tt.err); got != tt.want {
				t.Errorf("isRetryableLockError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestQuoteIdentifier(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain name", in: "netra_test", want: `"netra_test"`},
		{name: "embedded double quote is doubled", in: `weird"db`, want: `"weird""db"`},
		{name: "empty string still quoted", in: "", want: `""`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := quoteIdentifier(tt.in); got != tt.want {
				t.Errorf("quoteIdentifier(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
