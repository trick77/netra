package read

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// column is one value column of one tier's relation.
type column struct {
	name string
	// numeric marks a Postgres numeric column, which pgx decodes into a
	// pgtype.Numeric rather than a Go float. avg() over a bigint returns
	// numeric, so mem_used_avg and every other averaged integer lands here
	// while the raw mem_used does not.
	//
	// It is cast in SQL rather than converted in Go on purpose: the
	// conversion would have to run per value on the way out, and a cast the
	// database applies once per row is both cheaper and impossible to forget
	// on a new column.
	numeric bool
}

func (c column) selectExpr() string {
	if c.numeric {
		return fmt.Sprintf("s.%s::double precision", c.name)
	}
	return "s." + c.name
}

func columnNames(cols []column) []string {
	names := make([]string, 0, len(cols))
	for _, c := range cols {
		names = append(names, c.name)
	}
	return names
}

// valueColumns returns the measured columns of one tier's relation, in the
// order the schema declares them, with the identity columns removed.
//
// Discovered from information_schema and cached per relation. See the comment
// on Service.columns for why they are not written out in Go: the three host
// tiers alone carry 190 columns between them, and a hand-kept copy would
// drift silently -- a column missing from the copy reads as a metric nobody
// collected rather than as an error.
func (s *Service) valueColumns(ctx context.Context, fam *family, spec tierSpec) ([]column, error) {
	rel := fam.relation(spec)

	s.mu.Lock()
	cached, ok := s.columns[rel]
	s.mu.Unlock()
	if ok {
		return decodeCached(cached), nil
	}

	rows, err := s.pool.Query(ctx, `
		SELECT column_name, data_type
		  FROM information_schema.columns
		 WHERE table_schema = 'public' AND table_name = $1
		 ORDER BY ordinal_position`, rel)
	if err != nil {
		return nil, fmt.Errorf("describe %s: %w", rel, err)
	}
	defer rows.Close()

	// host_id identifies the host the whole request is about, the time column
	// becomes the point's timestamp, and the dimension columns become the
	// series key. None of the three measures anything.
	skip := append([]string{"host_id", spec.tsColumn}, fam.dimensionColumns...)

	var cols []column
	for rows.Next() {
		var name, dataType string
		if err := rows.Scan(&name, &dataType); err != nil {
			return nil, fmt.Errorf("scan %s columns: %w", rel, err)
		}
		if slices.Contains(skip, name) {
			continue
		}
		cols = append(cols, column{name: name, numeric: dataType == "numeric"})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate %s columns: %w", rel, err)
	}
	if len(cols) == 0 {
		// The relation is missing or holds nothing but identity columns.
		// Either way there is no series to return, and answering 500 with the
		// relation named beats an empty 200 that looks like an idle host.
		return nil, fmt.Errorf("relation %s has no value columns", rel)
	}

	s.mu.Lock()
	s.columns[rel] = encodeCached(cols)
	s.mu.Unlock()
	return cols, nil
}

// The cache stores names with a marker rather than the struct, so the cached
// value is a plain []string and cannot be mutated through a shared backing
// array by a caller that narrows it.
const numericMarker = "::"

func encodeCached(cols []column) []string {
	out := make([]string, 0, len(cols))
	for _, c := range cols {
		if c.numeric {
			out = append(out, c.name+numericMarker)
			continue
		}
		out = append(out, c.name)
	}
	return out
}

func decodeCached(names []string) []column {
	out := make([]column, 0, len(names))
	for _, n := range names {
		if after, found := strings.CutSuffix(n, numericMarker); found {
			out = append(out, column{name: after, numeric: true})
			continue
		}
		out = append(out, column{name: n})
	}
	return out
}

// narrow applies a ?columns= filter, preserving the SCHEMA's order rather than
// the request's: two clients asking for the same columns in different orders
// must not get points whose fields are transposed relative to each other.
//
// An unknown column is a 400 naming what the tier does have. Silently dropping
// it would answer with a column the caller did not ask for and omit the one
// they did, which a chart cannot notice.
func narrow(cols []column, want []string, rel string) ([]column, error) {
	if len(want) == 0 {
		return cols, nil
	}

	available := columnNames(cols)
	for _, w := range want {
		if !slices.Contains(available, w) {
			return nil, fmt.Errorf("%w: %s has no column %q; it has %s",
				ErrInvalid, rel, w, strings.Join(available, ", "))
		}
	}

	out := make([]column, 0, len(want))
	for _, c := range cols {
		if slices.Contains(want, c.name) {
			out = append(out, c)
		}
	}
	return out, nil
}
