package store

import (
	"context"
	"fmt"
)

// RecomputeBaselines rebuilds every subject's baseline from its own history.
//
// A thin call into the procedure 0020_metric_baselines.sql defines, rather
// than a second copy of the same statements in Go. The procedure is what the
// daily job runs, so anything that reimplemented it here would be a version of
// the rule that production never executes -- and the exclusion it carries is
// too load-bearing to be tested in a form nobody ships. This is the seam that
// lets a test drive the real one on demand instead of waiting a day for the
// scheduler.
//
// job_id 0 is the convention TimescaleDB uses for a manual CALL: the procedure
// reads only its config argument, and every netra_* procedure in this schema
// ignores the id entirely.
func (s *Store) RecomputeBaselines(ctx context.Context, config string) error {
	if config == "" {
		config = "{}"
	}
	if _, err := s.pool.Exec(ctx,
		`CALL netra_recompute_baselines(0, $1::jsonb)`, config); err != nil {
		return fmt.Errorf("recompute baselines: %w", err)
	}
	return nil
}
