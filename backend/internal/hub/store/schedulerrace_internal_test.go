package store

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// Which failures unschedulePolicyJobs retries.
//
// The discrimination is the whole safety of the retry: alter_job is idempotent
// so re-running it after a lost race costs nothing, but XX000 is Postgres's
// catch-all internal error and retrying every one of them would turn a genuine
// TimescaleDB bug into three silent retries and a delay before the same
// failure.
func TestSchedulerRaceErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{
			// The one that was actually observed, on the read side of
			// TimescaleDB's catalog scan.
			name: "more than one bgw job stat",
			err:  &pgconn.PgError{Code: "XX000", Message: "more than one bgw job stat found"},
			want: true,
		},
		{
			// Its sibling on the write side.
			name: "tuple concurrently updated",
			err:  &pgconn.PgError{Code: "XX000", Message: "tuple concurrently updated"},
			want: true,
		},
		{
			name: "wrapped, as the pool returns it",
			err: fmt.Errorf("unschedule: %w",
				&pgconn.PgError{Code: "XX000", Message: "more than one bgw job stat found"}),
			want: true,
		},
		{
			name: "a deadlock against a job still in flight",
			err:  &pgconn.PgError{Code: deadlockSQLState, Message: "deadlock detected"},
			want: true,
		},
		{
			name: "a lock timeout, for the same reason",
			err:  &pgconn.PgError{Code: lockNotAvailableSQLState, Message: "canceling statement"},
			want: true,
		},
		{
			// The guard that matters: XX000 is a catch-all, and anything else
			// wearing it is a real bug that must surface now.
			name: "another internal error",
			err:  &pgconn.PgError{Code: "XX000", Message: "cache lookup failed for relation 42"},
			want: false,
		},
		{
			name: "a constraint violation",
			err:  &pgconn.PgError{Code: "23505", Message: "duplicate key value"},
			want: false,
		},
		{
			// A closed pool is not a PgError at all, and must fail on the
			// first attempt rather than after three backoffs.
			name: "not a Postgres error",
			err:  errors.New("closed pool"),
			want: false,
		},
		{
			name: "no error",
			err:  nil,
			want: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isSchedulerRaceError(tc.err); got != tc.want {
				t.Errorf("isSchedulerRaceError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
