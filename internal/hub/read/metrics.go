package read

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// maxPoints bounds a single response. A seven-day raw query against a host
// with eight hundred core series is ten thousand points apiece, and nothing
// upstream limits how many series a family has -- so without a cap one
// request can ask the hub to materialise hundreds of megabytes.
//
// The cap is on POINTS rather than series because that is what the cost
// actually scales with, and it is reported (see Result.Truncated) rather than
// applied silently: a chart drawn from a quietly truncated series is wrong in
// the same way a chart drawn from the wrong tier is.
//
// A var rather than a const so a test can lower it: proving the truncation
// path works must not mean writing two hundred thousand rows.
var maxPoints = 200_000

// Series is one line of a chart: what distinguishes it, and its points.
type Series struct {
	// Key is empty for the host-level families, which have one series.
	// Values are strings even when the underlying column is an integer, so
	// the shape of a key does not vary by family.
	Key map[string]string `json:"key"`
	// Points is one row per timestamp: the unix millisecond timestamp
	// followed by one value per entry of Result.Columns, in that order. A
	// null in the response is a metric that was not collected, never a zero.
	Points [][]any `json:"points"`
}

// Result is a /metrics response.
type Result struct {
	Family string `json:"family"`
	Tier   string `json:"tier"`
	StepS  int    `json:"step_s"`

	// Window is what this response actually covers; Requested is what was
	// asked. They differ whenever a clamp fired, and Warnings says which.
	Window    Window   `json:"window"`
	Requested Window   `json:"requested_window"`
	Warnings  []string `json:"warnings"`

	// KeyColumns names the components of every Series.Key, in order.
	KeyColumns []string `json:"key_columns"`
	// Columns names the values in every point, in order, and is the reason a
	// client cannot confuse two tiers by accident: at raw the column is busy,
	// at 5m it is busy_avg and busy_max. The names differ per tier BY
	// CONSTRUCTION, so ignoring Tier yields a key the client does not
	// recognise rather than a number that looks plausible and is not.
	Columns []string `json:"columns"`

	Series []Series `json:"series"`

	// Truncated reports that maxPoints was reached and the series are cut
	// short. Never true silently.
	Truncated bool `json:"truncated"`
}

// MetricsQuery is a parsed /metrics request. Zero From or To mean "not given"
// and are defaulted by tier selection.
type MetricsQuery struct {
	HostID  int32
	Family  string
	From    time.Time
	To      time.Time
	Step    time.Duration
	StepSet bool
	// Columns narrows the response to these value columns. Empty means every
	// column of the chosen tier -- honest, but host_samples_5m alone has 66,
	// so a UI drawing one chart should ask for what it draws.
	Columns []string
}

// Metrics answers a metric-series query: it selects the tier, computes the
// window, and returns the points inside it.
func (s *Service) Metrics(ctx context.Context, q MetricsQuery, now time.Time) (Result, error) {
	fam, err := lookupFamily(q.Family)
	if err != nil {
		return Result{}, err
	}
	if err := s.hostExists(ctx, q.HostID); err != nil {
		return Result{}, err
	}

	plan, err := planQuery(fam, Window{From: q.From, To: q.To}, q.Step, q.StepSet, now)
	if err != nil {
		return Result{}, err
	}

	cols, err := s.valueColumns(ctx, fam, plan.spec)
	if err != nil {
		return Result{}, err
	}
	cols, err = narrow(cols, q.Columns, fam.relation(plan.spec))
	if err != nil {
		return Result{}, err
	}

	res := Result{
		Family:     plan.Family,
		Tier:       plan.Tier,
		StepS:      int(plan.Step / time.Second),
		Window:     plan.Window,
		Requested:  plan.Requested,
		Warnings:   plan.Warnings,
		KeyColumns: keyNames(fam),
		Columns:    columnNames(cols),
		Series:     []Series{},
	}
	if res.Warnings == nil {
		res.Warnings = []string{}
	}
	// Every clamp fired and no window survived. A valid answer with no
	// points, and the warning that explains it is already attached.
	if plan.Empty {
		return res, nil
	}

	series, truncated, err := s.querySeries(ctx, q.HostID, fam, plan, cols)
	if err != nil {
		return Result{}, err
	}
	res.Series = series
	res.Truncated = truncated
	if truncated {
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"the result reached the %d-point limit and is truncated; narrow the window or ask for fewer columns",
			maxPoints))
	}
	return res, nil
}

// querySeries runs the plan and groups the rows into series.
//
// The grouping is sequential rather than by map lookup because the query
// orders by key then time, so every row of a series arrives together -- which
// also makes the point order within a series the query's, not a map's.
func (s *Service) querySeries(ctx context.Context, hostID int32, fam *family, plan Plan, cols []column) ([]Series, bool, error) {
	sql := buildSQL(fam, plan.spec, cols)

	rows, err := s.pool.Query(ctx, sql, hostID, plan.Window.From, plan.Window.To, maxPoints+1)
	if err != nil {
		return nil, false, fmt.Errorf("query %s: %w", fam.relation(plan.spec), err)
	}
	defer rows.Close()

	nKeys := len(fam.keys)
	out := []Series{}
	var current *Series
	var currentKey []string
	total := 0

	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			return nil, false, fmt.Errorf("scan %s: %w", fam.relation(plan.spec), err)
		}

		// A dimension column can be NULL -- filesystems.mountpoint and the
		// like are nullable -- and a nil key component must not collapse two
		// different series into one, so it renders as the empty string and
		// stays distinct from any real value by position.
		key := make([]string, nKeys)
		for i := range nKeys {
			if s, ok := values[i].(string); ok {
				key[i] = s
			}
		}

		if current == nil || !sameKey(currentKey, key) {
			out = append(out, Series{Key: keyMap(fam, key), Points: [][]any{}})
			current = &out[len(out)-1]
			currentKey = key
		}

		ts, _ := values[nKeys].(time.Time)
		point := make([]any, 0, len(cols)+1)
		point = append(point, ts.UnixMilli())
		point = append(point, values[nKeys+1:]...)
		current.Points = append(current.Points, point)

		total++
		if total > maxPoints {
			// One row past the cap was fetched deliberately: it is the only
			// way to tell "exactly maxPoints of data" from "more than fits".
			// Drop it and report the truncation.
			current.Points = current.Points[:len(current.Points)-1]
			if len(current.Points) == 0 {
				out = out[:len(out)-1]
			}
			return out, true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("iterate %s: %w", fam.relation(plan.spec), err)
	}
	return out, false, nil
}

// buildSQL assembles the series query.
//
// Every identifier interpolated here is a constant of this package or a name
// read back from information_schema for the relation being queried, so no
// part of the request reaches the statement as text. The host id and the
// window are parameters.
func buildSQL(fam *family, spec tierSpec, cols []column) string {
	rel := fam.relation(spec)

	selects := make([]string, 0, len(fam.keys)+len(cols)+1)
	orders := make([]string, 0, len(fam.keys)+1)
	for i, k := range fam.keys {
		selects = append(selects, k.expr)
		// Ordinal ordering: the key expressions are already the first
		// columns, and repeating a qualified expression in ORDER BY would
		// have to stay character-identical to the SELECT to reuse it.
		orders = append(orders, fmt.Sprintf("%d", i+1))
	}
	selects = append(selects, "s."+spec.tsColumn)
	orders = append(orders, fmt.Sprintf("%d", len(fam.keys)+1))
	for _, c := range cols {
		selects = append(selects, c.selectExpr())
	}

	var b strings.Builder
	fmt.Fprintf(&b, "SELECT %s\n  FROM %s s\n", strings.Join(selects, ", "), rel)
	if fam.join != "" {
		fmt.Fprintf(&b, "  %s\n", fam.join)
	}
	fmt.Fprintf(&b, " WHERE s.host_id = $1 AND s.%s >= $2 AND s.%s <= $3\n", spec.tsColumn, spec.tsColumn)
	fmt.Fprintf(&b, " ORDER BY %s\n LIMIT $4", strings.Join(orders, ", "))
	return b.String()
}

func sameKey(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func keyMap(fam *family, key []string) map[string]string {
	m := make(map[string]string, len(key))
	for i, k := range fam.keys {
		m[k.name] = key[i]
	}
	return m
}

func keyNames(fam *family) []string {
	names := make([]string, 0, len(fam.keys))
	for _, k := range fam.keys {
		names = append(names, k.name)
	}
	return names
}
