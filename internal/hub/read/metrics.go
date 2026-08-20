package read

import (
	"context"
	"fmt"
	"slices"
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
	//
	// A value carries its COLUMN'S type, which is not always a number.
	// family=collector is the case that proves it: at raw it yields ok as a
	// boolean and error_code as a string, and at 5m and 1h it yields numeric
	// counts beside the same string error_code. Result.Columns is the
	// authority on what a position holds -- a client indexing points[i][n]
	// without reading it will eventually parse a string as a number.
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
	//
	// The exceptions are columns where the bucket value IS the raw quantity
	// and there is nothing to confuse: last() columns such as uptime_s and
	// error_code, and max() of a monotonic counter such as
	// buffer_dropped_total. Those keep one name at every tier deliberately.
	// TestIntegrationNoValueColumnNameIsSharedBetweenTiers enumerates every
	// family against that exemption list, so a new aggregate that shares a
	// name for any other reason fails the build -- which is how
	// filesystem_samples' total became total_max.
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
	cols, missing, err := narrow(cols, q.Columns, fam.relation(plan.spec))
	if err != nil {
		return Result{}, err
	}
	dropped, err := s.missingColumns(ctx, fam, fam.relation(plan.spec), missing)
	if err != nil {
		return Result{}, err
	}
	plan.Warnings = append(plan.Warnings, dropped...)

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

// missingColumns decides what a base that matched nothing at the chosen tier
// means, and turns it into either a warning or a 400.
//
// The split is between "this family does not measure that here" and "nobody
// has ever heard of that column":
//
//   - Measured at SOME tier of this family -- a warning. That case is real and
//     routine: filesystem_samples has `free` and filesystem_samples_5m has
//     only free_min, so a client naming the quantity once for every range is
//     right and the tier simply has nothing to give it. Warned rather than
//     dropped in silence, so an empty chart says why.
//   - Measured at NO tier -- ErrInvalid, exactly as before this function
//     existed. A name no relation in the family carries is a typo, and the
//     older behaviour was right about it: answering 200 with the column
//     missing is a bug the client cannot see.
func (s *Service) missingColumns(ctx context.Context, fam *family, rel string, missing []string) ([]string, error) {
	if len(missing) == 0 {
		return nil, nil
	}

	known, err := s.familyBases(ctx, fam)
	if err != nil {
		return nil, err
	}

	warnings := make([]string, 0, len(missing))
	for _, m := range missing {
		if !slices.Contains(known, m) {
			return nil, fmt.Errorf("%w: family %s has no column %q at any tier",
				ErrInvalid, fam.name, m)
		}
		warnings = append(warnings, fmt.Sprintf(
			"%s does not carry %q at this tier; it was dropped from the response", rel, m))
	}
	return warnings, nil
}

// familyBases is every quantity the family measures at any tier, with the
// aggregate suffixes stripped -- the vocabulary a caller may name.
//
// Every relation it reads is cached by valueColumns after the first request,
// so this costs one describe per tier over the life of the process and nothing
// after that.
func (s *Service) familyBases(ctx context.Context, fam *family) ([]string, error) {
	var out []string
	for _, spec := range fam.tiers {
		cols, err := s.valueColumns(ctx, fam, spec)
		if err != nil {
			return nil, err
		}
		for _, c := range cols {
			base := unsuffixed(c.name)
			if !slices.Contains(out, base) {
				out = append(out, base)
			}
		}
	}
	return out, nil
}

// HostSeries is one host's share of a FleetResult.
type HostSeries struct {
	HostID int32 `json:"host_id"`
	// Series is empty rather than absent for a host that reported nothing in
	// the window, so a caller can tell a silent host from one it never asked
	// about.
	Series []Series `json:"series"`
}

// FleetResult is a /metrics response for several hosts at once.
//
// Every field above Hosts is the SAME for every host by construction: the tier
// is chosen from the family and the step, never from the data, so one window,
// one column list and one set of warnings describe the whole answer. That is
// the entire reason this endpoint exists -- the fleet page asked N hosts the
// identical question N times and reassembled an answer it could have been
// handed once.
type FleetResult struct {
	Family string `json:"family"`
	Tier   string `json:"tier"`
	StepS  int    `json:"step_s"`

	Window    Window   `json:"window"`
	Requested Window   `json:"requested_window"`
	Warnings  []string `json:"warnings"`

	KeyColumns []string `json:"key_columns"`
	Columns    []string `json:"columns"`

	Hosts []HostSeries `json:"hosts"`

	// Truncated reports that maxPoints was reached. The cap is on the whole
	// response rather than per host -- it bounds what the hub materialises,
	// and that is one number however many hosts share it -- so this is
	// top-level and the hosts after the cut simply carry fewer points.
	Truncated bool `json:"truncated"`
}

// FleetMetricsQuery is a parsed fleet /metrics request.
type FleetMetricsQuery struct {
	HostIDs []int32
	Family  string
	From    time.Time
	To      time.Time
	Step    time.Duration
	StepSet bool
	Columns []string
}

// FleetMetrics answers a metric-series query for several hosts in one pass.
//
// The hosts are NOT checked for existence. An id nobody ever registered simply
// contributes no rows and comes back with an empty series list, which is the
// same thing the caller sees for a host that exists and has been quiet -- and
// the fleet page only ever asks about hosts /api/v1/hosts just handed it.
func (s *Service) FleetMetrics(ctx context.Context, q FleetMetricsQuery, now time.Time) (FleetResult, error) {
	fam, err := lookupFamily(q.Family)
	if err != nil {
		return FleetResult{}, err
	}
	if len(q.HostIDs) == 0 {
		return FleetResult{}, fmt.Errorf("%w: hosts must name at least one host", ErrInvalid)
	}

	plan, err := planQuery(fam, Window{From: q.From, To: q.To}, q.Step, q.StepSet, now)
	if err != nil {
		return FleetResult{}, err
	}

	cols, err := s.valueColumns(ctx, fam, plan.spec)
	if err != nil {
		return FleetResult{}, err
	}
	cols, missing, err := narrow(cols, q.Columns, fam.relation(plan.spec))
	if err != nil {
		return FleetResult{}, err
	}
	dropped, err := s.missingColumns(ctx, fam, fam.relation(plan.spec), missing)
	if err != nil {
		return FleetResult{}, err
	}
	plan.Warnings = append(plan.Warnings, dropped...)

	res := FleetResult{
		Family:     plan.Family,
		Tier:       plan.Tier,
		StepS:      int(plan.Step / time.Second),
		Window:     plan.Window,
		Requested:  plan.Requested,
		Warnings:   plan.Warnings,
		KeyColumns: keyNames(fam),
		Columns:    columnNames(cols),
		Hosts:      emptyHosts(q.HostIDs),
	}
	if res.Warnings == nil {
		res.Warnings = []string{}
	}
	if plan.Empty {
		return res, nil
	}

	byHost, truncated, err := s.queryFleetSeries(ctx, q.HostIDs, fam, plan, cols)
	if err != nil {
		return FleetResult{}, err
	}
	for i := range res.Hosts {
		if series, ok := byHost[res.Hosts[i].HostID]; ok {
			res.Hosts[i].Series = series
		}
	}
	res.Truncated = truncated
	if truncated {
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"the result reached the %d-point limit and is truncated; narrow the window, ask for fewer hosts or ask for fewer columns",
			maxPoints))
	}
	return res, nil
}

// emptyHosts seeds the response in the order the caller listed the hosts, so
// every requested host has an entry whether or not the query returns rows for
// it. A duplicate id in the request yields one entry, not two.
func emptyHosts(ids []int32) []HostSeries {
	out := make([]HostSeries, 0, len(ids))
	seen := make(map[int32]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, HostSeries{HostID: id, Series: []Series{}})
	}
	return out
}

// queryFleetSeries is querySeries over several hosts: the same single query,
// with host_id as the outermost ordering level and one more grouping break.
func (s *Service) queryFleetSeries(ctx context.Context, hostIDs []int32, fam *family, plan Plan, cols []column) (map[int32][]Series, bool, error) {
	sql := buildFleetSQL(fam, plan.spec, cols)

	rows, err := s.pool.Query(ctx, sql, hostIDs, plan.Window.From, plan.Window.To, maxPoints+1)
	if err != nil {
		return nil, false, fmt.Errorf("query %s: %w", fam.relation(plan.spec), err)
	}
	defer rows.Close()

	nKeys := len(fam.keys)
	out := make(map[int32][]Series, len(hostIDs))
	var currentHost int32
	var currentKey []string
	var current *Series
	haveHost := false
	total := 0

	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			return nil, false, fmt.Errorf("scan %s: %w", fam.relation(plan.spec), err)
		}

		// host_id is selected first, ahead of the key columns, so the rows of
		// one host arrive together and the key break below only has to
		// consider the host it is already inside.
		hostID, _ := values[0].(int32)
		rest := values[1:]

		key := make([]string, nKeys)
		for i := range nKeys {
			if s, ok := rest[i].(string); ok {
				key[i] = s
			}
		}

		if !haveHost || hostID != currentHost || current == nil || !sameKey(currentKey, key) {
			out[hostID] = append(out[hostID], Series{Key: keyMap(fam, key), Points: [][]any{}})
			current = &out[hostID][len(out[hostID])-1]
			currentHost = hostID
			currentKey = key
			haveHost = true
		}

		ts, _ := rest[nKeys].(time.Time)
		point := make([]any, 0, len(cols)+1)
		point = append(point, ts.UnixMilli())
		point = append(point, rest[nKeys+1:]...)
		current.Points = append(current.Points, point)

		total++
		if total > maxPoints {
			// One row past the cap, dropped -- see querySeries for why it is
			// fetched at all.
			current.Points = current.Points[:len(current.Points)-1]
			if len(current.Points) == 0 {
				out[currentHost] = out[currentHost][:len(out[currentHost])-1]
			}
			return out, true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("iterate %s: %w", fam.relation(plan.spec), err)
	}
	return out, false, nil
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

// buildSQL assembles the series query for one host.
func buildSQL(fam *family, spec tierSpec, cols []column) string {
	return buildSeriesSQL(fam, spec, cols, false)
}

// buildFleetSQL is buildSQL for several hosts: host_id joins the select list
// as the FIRST column and the OUTERMOST ordering level, so the rows of one
// host still arrive contiguously and every series inside a host still arrives
// in one run. $1 becomes the id list rather than a single id.
func buildFleetSQL(fam *family, spec tierSpec, cols []column) string {
	return buildSeriesSQL(fam, spec, cols, true)
}

// buildSeriesSQL assembles the series query for one host or for many.
//
// One function rather than two: every identifier interpolated here is a
// constant of this package or a name read back from information_schema for the
// relation being queried, and that argument is only worth making once. The
// host id (or ids) and the window are parameters.
func buildSeriesSQL(fam *family, spec tierSpec, cols []column, fleet bool) string {
	rel := fam.relation(spec)

	selects := make([]string, 0, len(fam.keys)+len(cols)+2)
	orders := make([]string, 0, len(fam.keys)+2)
	if fleet {
		selects = append(selects, "s.host_id")
		orders = append(orders, "1")
	}
	// The ordinal every key sits at, which the host_id column shifts by one.
	offset := len(selects)
	for i, k := range fam.keys {
		selects = append(selects, k.expr)
		// Ordinal ordering: the key expressions are already the first
		// columns, and repeating a qualified expression in ORDER BY would
		// have to stay character-identical to the SELECT to reuse it.
		orders = append(orders, fmt.Sprintf("%d", offset+i+1))
	}
	selects = append(selects, "s."+spec.tsColumn)
	orders = append(orders, fmt.Sprintf("%d", offset+len(fam.keys)+1))
	for _, c := range cols {
		selects = append(selects, c.selectExpr())
	}

	hostPredicate := "s.host_id = $1"
	if fleet {
		hostPredicate = "s.host_id = ANY($1)"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "SELECT %s\n  FROM %s s\n", strings.Join(selects, ", "), rel)
	if fam.join != "" {
		fmt.Fprintf(&b, "  %s\n", fam.join)
	}
	fmt.Fprintf(&b, " WHERE %s AND s.%s >= $2 AND s.%s <= $3\n", hostPredicate, spec.tsColumn, spec.tsColumn)
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
