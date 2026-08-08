package store

import (
	"context"
	"embed"
	"fmt"
	"sort"
	"strings"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// noTxMarker opts a migration out of the surrounding transaction.
//
// TimescaleDB refuses to create a continuous aggregate inside a transaction
// block, so those migrations carry this marker on their first line.
const noTxMarker = "-- netra:no-transaction"

// migrateLockID is the advisory-lock key held for the whole of Migrate.
//
// An arbitrary but FIXED constant: pg_advisory_lock namespaces by value alone,
// so every hub replica must use the same one. Chosen from "netra" so it does
// not collide with an application lock added later.
const migrateLockID int64 = 0x6E65747261 // "netra"

// Migrate applies every pending migration in filename order, exactly once.
//
// Serialised across replicas by a session advisory lock. Without it, two hubs
// starting together — a first deploy, or a restart racing a slow start — both
// read applied=false for the same file and both apply it. The DDL survives that
// (every statement is IF NOT EXISTS / if_not_exists => TRUE), but
// `INSERT INTO schema_migrations` against a TEXT PRIMARY KEY does not: the
// loser gets SQLSTATE 23505, which propagates out through run() to os.Exit(1).
// The second replica died on first deploy.
//
// The lock is taken on a DEDICATED connection held for the duration, because a
// session-level advisory lock belongs to one session and s.pool hands out a
// different connection per call. It is released explicitly rather than left to
// connection teardown, so the next replica proceeds immediately.
func (s *Store) Migrate(ctx context.Context) error {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrateLockID); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer func() {
		// Best effort: a failure here means the connection is already gone, and
		// Postgres drops session advisory locks when the session ends.
		_, _ = conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, migrateLockID)
	}()

	if _, err := s.pool.Exec(ctx,
		`CREATE TABLE IF NOT EXISTS schema_migrations (
			name       TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)

	for _, name := range names {
		var applied bool
		if err := s.pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE name = $1)`,
			name).Scan(&applied); err != nil {
			return fmt.Errorf("check %s: %w", name, err)
		}
		if applied {
			continue
		}

		body, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}

		if err := s.applyMigration(ctx, name, string(body)); err != nil {
			return fmt.Errorf("apply %s: %w", name, err)
		}
	}

	return nil
}

func (s *Store) applyMigration(ctx context.Context, name, body string) error {
	if strings.HasPrefix(strings.TrimSpace(body), noTxMarker) {
		// Outside a transaction: the statements are individually atomic and a
		// partial failure leaves the migration unrecorded, so it is retried.
		//
		// Each statement is Exec'd separately rather than passed as one
		// multi-statement body. pgx sends a multi-statement Exec via the
		// simple-query protocol, and Postgres wraps that in an implicit
		// transaction block regardless of the caller not opening one — which
		// defeats the whole point of this branch, since TimescaleDB refuses
		// to create a continuous aggregate inside any transaction block.
		for _, stmt := range splitStatements(body) {
			if _, err := s.pool.Exec(ctx, stmt); err != nil {
				return err
			}
		}
		_, err := s.pool.Exec(ctx,
			`INSERT INTO schema_migrations (name) VALUES ($1)`, name)
		return err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, body); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO schema_migrations (name) VALUES ($1)`, name); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// splitStatements splits body into individual SQL statements on top-level
// semicolons. It does not split on a semicolon inside a single-quoted string
// literal, a dollar-quoted block (tagged or untagged), a line comment, or a
// block comment. Statements are trimmed and empty or comment-only statements
// are dropped, so a trailing semicolon or a file-ending comment does not
// yield a blank Exec.
func splitStatements(body string) []string {
	var stmts []string
	var cur strings.Builder

	inSingleQuote := false
	inLineComment := false
	inBlockComment := false
	dollarTag := "" // non-empty while inside a $tag$ ... $tag$ block

	n := len(body)
	for i := 0; i < n; i++ {
		c := body[i]

		if inLineComment {
			cur.WriteByte(c)
			if c == '\n' {
				inLineComment = false
			}
			continue
		}

		if inBlockComment {
			cur.WriteByte(c)
			if c == '*' && i+1 < n && body[i+1] == '/' {
				cur.WriteByte('/')
				i++
				inBlockComment = false
			}
			continue
		}

		if dollarTag != "" {
			cur.WriteByte(c)
			if c == '$' && strings.HasPrefix(body[i:], dollarTag) {
				cur.WriteString(dollarTag[1:])
				i += len(dollarTag) - 1
				dollarTag = ""
			}
			continue
		}

		if inSingleQuote {
			cur.WriteByte(c)
			if c == '\'' {
				// A doubled quote ('') is an escaped quote, not the closing
				// delimiter.
				if i+1 < n && body[i+1] == '\'' {
					cur.WriteByte('\'')
					i++
				} else {
					inSingleQuote = false
				}
			}
			continue
		}

		// Not inside any quoted/commented region.
		switch {
		case c == '\'':
			inSingleQuote = true
			cur.WriteByte(c)
		case c == '-' && i+1 < n && body[i+1] == '-':
			inLineComment = true
			cur.WriteByte(c)
		case c == '/' && i+1 < n && body[i+1] == '*':
			inBlockComment = true
			cur.WriteByte(c)
		case c == '$':
			if tag, ok := dollarQuoteTag(body[i:]); ok {
				dollarTag = tag
				cur.WriteString(tag)
				i += len(tag) - 1
			} else {
				cur.WriteByte(c)
			}
		case c == ';':
			stmts = appendStatement(stmts, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	stmts = appendStatement(stmts, cur.String())

	return stmts
}

// dollarQuoteTag reports whether s begins with a dollar-quote opening
// delimiter ($$ or $tag$) and returns that delimiter.
func dollarQuoteTag(s string) (string, bool) {
	if len(s) < 2 || s[0] != '$' {
		return "", false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		if c == '$' {
			return s[:i+1], true
		}
		// Valid dollar-quote tag characters: letters, digits, underscore.
		if !(c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')) {
			return "", false
		}
	}
	return "", false
}

// appendStatement trims stmt and appends it to stmts unless it is empty or
// consists only of comments.
func appendStatement(stmts []string, stmt string) []string {
	trimmed := strings.TrimSpace(stmt)
	if trimmed == "" || isCommentOnly(trimmed) {
		return stmts
	}
	return append(stmts, trimmed)
}

// isCommentOnly reports whether s contains only whitespace and comments
// (line or block), i.e. no executable SQL.
func isCommentOnly(s string) bool {
	i := 0
	n := len(s)
	for i < n {
		switch {
		case s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r':
			i++
		case s[i] == '-' && i+1 < n && s[i+1] == '-':
			for i < n && s[i] != '\n' {
				i++
			}
		case s[i] == '/' && i+1 < n && s[i+1] == '*':
			end := strings.Index(s[i+2:], "*/")
			if end == -1 {
				return true
			}
			i = i + 2 + end + 2
		default:
			return false
		}
	}
	return true
}
