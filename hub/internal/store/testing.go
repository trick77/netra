package store

import (
	"context"
	"os"
	"testing"
)

// OpenTest connects to the database named by NETRA_TEST_DSN and drops the
// public schema so each test starts from nothing.
//
// Integration tests run against real TimescaleDB rather than a mock: the
// continuous aggregates and their start_offset behaviour are the risky part
// of this schema and a fake would verify nothing about them.
func OpenTest(t *testing.T) *Store {
	t.Helper()

	dsn := os.Getenv("NETRA_TEST_DSN")
	if dsn == "" {
		t.Skip("NETRA_TEST_DSN not set; skipping integration test")
	}

	ctx := context.Background()
	s, err := Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}

	if _, err := s.pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public;`); err != nil {
		s.Close()
		t.Fatalf("reset schema: %v", err)
	}

	t.Cleanup(s.Close)
	return s
}
