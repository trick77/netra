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

// aggregateSuffixes are the suffixes a continuous aggregate adds to a base
// column name. They are what makes a column name tier-dependent: the raw
// relation carries cpu_total, the 5m and 1h ones carry cpu_total_avg and
// cpu_total_max, and filesystem_samples_5m carries free_min with no _avg or
// _max peer at all.
//
// The same list the UI resolves against (ui/src/lib/metrics.ts, candidates()),
// and it has to stay the same list: a suffix known to one side and not the
// other is a column one side can ask for and the other cannot answer.
var aggregateSuffixes = []string{"_avg", "_max", "_min"}

// isSuffixed reports whether a requested name already names one aggregate of a
// base rather than the base itself.
func isSuffixed(name string) bool {
	for _, suf := range aggregateSuffixes {
		if strings.HasSuffix(name, suf) {
			return true
		}
	}
	return false
}

// unsuffixed strips an aggregate suffix, so cpu_total_avg and cpu_total_max
// both reduce to the quantity they aggregate.
func unsuffixed(name string) string {
	for _, suf := range aggregateSuffixes {
		if base, found := strings.CutSuffix(name, suf); found {
			return base
		}
	}
	return name
}

// candidates is name plus every aggregate of it, in a fixed order.
func candidates(base string) []string {
	out := make([]string, 0, len(aggregateSuffixes)+1)
	out = append(out, base)
	for _, suf := range aggregateSuffixes {
		out = append(out, base+suf)
	}
	return out
}

// narrow applies a ?columns= filter, preserving the SCHEMA's order rather than
// the request's: two clients asking for the same columns in different orders
// must not get points whose fields are transposed relative to each other.
//
// A requested name is matched one of two ways, and its SUFFIX decides which:
//
//   - Already suffixed (cpu_total_max) -- EXACT. The caller named one tier's
//     aggregate, so a tier that does not carry it is a 400 naming what the
//     tier does carry. This is the honesty rule the column naming exists to
//     enforce (see Result.Columns): answering a request for cpu_total_max with
//     cpu_total would hand back a peak that is really an average.
//   - Not suffixed (cpu_total) -- a BASE, expanded to itself plus every
//     aggregate of it the relation carries. A caller drawing one chart at
//     every range can then name the quantity once instead of tracking which
//     tier is about to answer it, which is what lets the fleet page ask for
//     the dozen columns it draws instead of taking all hundred.
//
// The second return names the bases that matched nothing HERE. A base can be
// real and still be absent from one tier -- filesystem_samples has `free` and
// filesystem_samples_5m has only free_min -- so that alone is not an error.
// The caller decides: a base the family measures at SOME tier becomes a
// warning (see missingColumns), and a base no tier has ever heard of is the
// 400 it was before, because that is a typo and nothing else.
func narrow(cols []column, want []string, rel string) ([]column, []string, error) {
	if len(want) == 0 {
		return cols, nil, nil
	}

	available := columnNames(cols)
	keep := make([]string, 0, len(want)*2)
	var missing []string

	for _, w := range want {
		if isSuffixed(w) {
			if !slices.Contains(available, w) {
				return nil, nil, fmt.Errorf("%w: %s has no column %q; it has %s",
					ErrInvalid, rel, w, strings.Join(available, ", "))
			}
			keep = append(keep, w)
			continue
		}

		found := false
		for _, c := range candidates(w) {
			if slices.Contains(available, c) {
				keep = append(keep, c)
				found = true
			}
		}
		if !found {
			missing = append(missing, w)
		}
	}

	out := make([]column, 0, len(keep))
	for _, c := range cols {
		if slices.Contains(keep, c.name) {
			out = append(out, c)
		}
	}
	return out, missing, nil
}
