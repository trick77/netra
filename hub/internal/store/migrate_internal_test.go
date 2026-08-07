package store

import (
	"context"
	"reflect"
	"testing"
)

func TestSplitStatements(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []string
	}{
		{
			name: "two plain statements",
			body: "CREATE TABLE a (id INT);\nCREATE TABLE b (id INT);",
			want: []string{"CREATE TABLE a (id INT)", "CREATE TABLE b (id INT)"},
		},
		{
			name: "semicolon inside single-quoted literal does not split",
			body: "INSERT INTO a (name) VALUES ('foo;bar');\nCREATE TABLE b (id INT);",
			want: []string{"INSERT INTO a (name) VALUES ('foo;bar')", "CREATE TABLE b (id INT)"},
		},
		{
			name: "escaped quote inside literal does not confuse the parser",
			body: "INSERT INTO a (name) VALUES ('it''s; here');\nCREATE TABLE b (id INT);",
			want: []string{"INSERT INTO a (name) VALUES ('it''s; here')", "CREATE TABLE b (id INT)"},
		},
		{
			name: "semicolon inside line comment does not split",
			body: "CREATE TABLE a (id INT); -- comment; with semicolon\nCREATE TABLE b (id INT);",
			want: []string{"CREATE TABLE a (id INT)", "-- comment; with semicolon\nCREATE TABLE b (id INT)"},
		},
		{
			name: "semicolon inside block comment does not split",
			body: "CREATE TABLE a (id INT); /* comment; with semicolon */\nCREATE TABLE b (id INT);",
			want: []string{"CREATE TABLE a (id INT)", "/* comment; with semicolon */\nCREATE TABLE b (id INT)"},
		},
		{
			name: "semicolon inside dollar-quoted block does not split",
			body: "CREATE FUNCTION f() RETURNS void AS $$ BEGIN PERFORM 1; END; $$ LANGUAGE plpgsql;\nCREATE TABLE b (id INT);",
			want: []string{
				"CREATE FUNCTION f() RETURNS void AS $$ BEGIN PERFORM 1; END; $$ LANGUAGE plpgsql",
				"CREATE TABLE b (id INT)",
			},
		},
		{
			name: "semicolon inside tagged dollar-quoted block does not split",
			body: "CREATE FUNCTION f() RETURNS void AS $tag$ BEGIN PERFORM 1; END; $tag$ LANGUAGE plpgsql;\nCREATE TABLE b (id INT);",
			want: []string{
				"CREATE FUNCTION f() RETURNS void AS $tag$ BEGIN PERFORM 1; END; $tag$ LANGUAGE plpgsql",
				"CREATE TABLE b (id INT)",
			},
		},
		{
			name: "trailing semicolon produces no empty statement",
			body: "CREATE TABLE a (id INT);\n",
			want: []string{"CREATE TABLE a (id INT)"},
		},
		{
			name: "trailing comment produces no empty statement",
			body: "CREATE TABLE a (id INT);\n-- trailing comment, nothing after it\n",
			want: []string{"CREATE TABLE a (id INT)"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitStatements(tt.body)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("splitStatements(%q) = %#v, want %#v", tt.body, got, tt.want)
			}
		})
	}
}

// TestIntegrationApplyMigrationNoTransactionMultiStatement proves the actual
// defect this file fixes: a no-transaction migration with more than one
// statement must apply each statement in its own implicit transaction, not
// as a single multi-statement Exec (which Postgres wraps in one implicit
// transaction block regardless).
//
// CREATE INDEX CONCURRENTLY is a faithful stand-in for Task 5's continuous
// aggregates: Postgres rejects it outright inside any transaction block
// ("CREATE INDEX CONCURRENTLY cannot run inside a transaction block"), so
// this test fails loudly without the fix instead of silently doing the
// wrong thing.
func TestIntegrationApplyMigrationNoTransactionMultiStatement(t *testing.T) {
	ctx := context.Background()
	s := OpenTest(t)

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate (setup): %v", err)
	}

	body := noTxMarker + `
CREATE TABLE concurrent_index_target (id INTEGER PRIMARY KEY, name TEXT);
CREATE INDEX CONCURRENTLY idx_concurrent_index_target_name ON concurrent_index_target (name);
`

	if err := s.applyMigration(ctx, "9999_no_tx_multi_statement.sql", body); err != nil {
		t.Fatalf("applyMigration: %v", err)
	}

	var tableCount int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM information_schema.tables
		 WHERE table_schema = 'public' AND table_name = 'concurrent_index_target'`).Scan(&tableCount); err != nil {
		t.Fatalf("query table: %v", err)
	}
	if tableCount != 1 {
		t.Fatalf("concurrent_index_target table count = %d, want 1", tableCount)
	}

	var indexCount int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM pg_indexes
		 WHERE schemaname = 'public' AND indexname = 'idx_concurrent_index_target_name'`).Scan(&indexCount); err != nil {
		t.Fatalf("query index: %v", err)
	}
	if indexCount != 1 {
		t.Fatalf("idx_concurrent_index_target_name index count = %d, want 1", indexCount)
	}
}
