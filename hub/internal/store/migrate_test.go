package store_test

import (
	"context"
	"testing"

	"github.com/trick77/netra/hub/internal/store"
)

func TestIntegrationMigrateIsIdempotent(t *testing.T) {
	ctx := context.Background()
	s := store.OpenTest(t)

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("first Migrate: %v", err)
	}
	// Running again must be a no-op, not an error: the hub migrates on every
	// start, so a restart with no new migrations has to succeed.
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}

	var n int
	err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM information_schema.tables
		 WHERE table_schema = 'public' AND table_name = 'hosts'`).Scan(&n)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if n != 1 {
		t.Fatalf("hosts table count = %d, want 1", n)
	}
}
