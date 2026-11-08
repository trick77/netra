package store

import (
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/trick77/ship/kit/schemadoc"
)

// TestSchemaDoc keeps docs/schema.md in step with the migrations and the
// model.go row types. OpenTest hands out a fresh clone of the migrated
// template, skipped without SHIP_TEST_DSN.
func TestSchemaDoc(t *testing.T) {
	s := OpenTest(t)
	// A no-op on the clone, as in every store test; kept so the doc is the
	// schema Migrate produces, not whatever the template happened to hold.
	if err := s.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Registered after OpenTest's cleanup, so it closes before the pool.
	db := stdlib.OpenDBFromPool(s.Pool())
	t.Cleanup(func() { _ = db.Close() })

	sc, err := schemadoc.Describe(t.Context(), db, schemadoc.Postgres)
	if err != nil {
		t.Fatal(err)
	}
	es, err := schemadoc.Entities(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := schemadoc.Render(sc, es)
	if err != nil {
		t.Fatal(err)
	}
	schemadoc.Golden(t, doc, filepath.Join("..", "..", "..", "..", "docs", "schema.md"))
}
