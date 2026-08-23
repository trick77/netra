package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// The one error OpenTest turns into a message about what the test role needs,
// rather than into a bare failure. Every other error has to stay a plain
// failure: treating anything else as a permissions problem would print
// misleading advice for a genuine bug.
func TestIsInsufficientPrivilege(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "permission denied is a privilege problem",
			err:  &pgconn.PgError{Code: insufficientPrivilegeSQLState},
			want: true,
		},
		{
			name: "wrapped permission denied is still detected through errors.As",
			err: fmt.Errorf("clone template database: %w",
				&pgconn.PgError{Code: insufficientPrivilegeSQLState}),
			want: true,
		},
		{
			name: "another postgres error is not a privilege problem",
			err:  &pgconn.PgError{Code: "42883"}, // undefined_function
			want: false,
		},
		{
			name: "a lock error is not a privilege problem",
			err:  &pgconn.PgError{Code: deadlockSQLState},
			want: false,
		},
		{
			name: "non-postgres error is not a privilege problem",
			err:  errors.New("connection reset by peer"),
			want: false,
		},
		{
			name: "nil error is not a privilege problem",
			err:  nil,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isInsufficientPrivilege(tt.err); got != tt.want {
				t.Errorf("isInsufficientPrivilege(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// dsnForDatabase is how every clone, and the admin connection that creates it,
// gets addressed. A DSN it cannot rewrite has to fail loudly: silently
// returning the input would point the whole suite at one shared database
// again, which is the bug this harness exists to remove.
func TestDSNForDatabase(t *testing.T) {
	t.Run("rewrites the database name and keeps everything else", func(t *testing.T) {
		got, err := dsnForDatabase(
			"postgres://netra:pw@127.0.0.1:5432/netra_test?sslmode=disable", "clone_7")
		if err != nil {
			t.Fatalf("dsnForDatabase: %v", err)
		}
		want := "postgres://netra:pw@127.0.0.1:5432/clone_7?sslmode=disable"
		if got != want {
			t.Errorf("dsnForDatabase = %q, want %q", got, want)
		}
	})

	t.Run("rejects a keyword/value DSN rather than ignoring it", func(t *testing.T) {
		if _, err := dsnForDatabase("host=127.0.0.1 user=netra dbname=netra_test", "clone_7"); err == nil {
			t.Error("dsnForDatabase accepted a keyword/value DSN; it must say it cannot rewrite one")
		}
	})
}

// The template is reused across runs, so its name is the only thing standing
// between a schema edit and a suite that keeps testing the previous schema.
func TestTemplateDBNameIsStableAndDerivedFromTheMigrations(t *testing.T) {
	first := templateDBName()
	if first != templateDBName() {
		t.Error("templateDBName is not stable across calls")
	}
	if !strings.HasPrefix(first, "netra_tmpl_") {
		t.Errorf("templateDBName = %q, want a netra_tmpl_ prefix", first)
	}
	// A bare prefix would collide with every other schema version.
	if len(first) <= len("netra_tmpl_") {
		t.Errorf("templateDBName = %q carries no hash of the migrations", first)
	}
}

// The harness reaches Postgres through three small helpers, and every one of
// them has to fail by returning rather than by connecting somewhere else.
// That distinction is the whole point: a DSN this code cannot use has to stop
// the run, because the alternative -- quietly falling back to the database
// named in BACKEND_TEST_DSN -- is the shared-database behaviour the per-test
// clone exists to end, and it would look like a pass.
func TestAdminHelpersFailRatherThanGuess(t *testing.T) {
	ctx := context.Background()

	t.Run("a DSN that is not a URL is refused before any connection", func(t *testing.T) {
		const bad = "host=127.0.0.1 user=netra dbname=netra_test"

		if _, err := adminConnect(ctx, bad); err == nil {
			t.Error("adminConnect accepted a keyword/value DSN")
		}
		if err := adminExec(ctx, bad, `SELECT 1`); err == nil {
			t.Error("adminExec accepted a keyword/value DSN")
		}
		if _, err := ensureTemplateDB(ctx, bad); err == nil {
			t.Error("ensureTemplateDB accepted a keyword/value DSN")
		}
	})

	t.Run("a well-formed DSN nothing answers on is reported, not swallowed", func(t *testing.T) {
		// Port 1 so the connection is refused immediately rather than making
		// the suite wait out a timeout.
		const unreachable = "postgres://netra:netra@127.0.0.1:1/netra_test"

		if _, err := adminConnect(ctx, unreachable); err == nil {
			t.Error("adminConnect reported success against a closed port")
		}
		if err := adminExec(ctx, unreachable, `SELECT 1`); err == nil {
			t.Error("adminExec reported success against a closed port")
		}
		if _, err := ensureTemplateDB(ctx, unreachable); err == nil {
			t.Error("ensureTemplateDB reported success against a closed port")
		}
	})
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
