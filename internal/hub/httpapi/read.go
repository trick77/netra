package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/trick77/netra/internal/hub/read"
)

// readHandler serves the read half of spec 8: the host inventory, the
// dimension listings behind each host, the metric series, and the event log.
//
// It shares NewAdminHandler's mux rather than mounting its own. NewRouter
// already registers "/api/v1/", and a second mount of the same prefix -- or a
// second "GET /api/v1/hosts" -- panics http.ServeMux at startup rather than
// at request time, so the two halves of /api/v1 must be registered together.
type readHandler struct {
	svc *read.Service
	// now is the clock. Every window in this package is relative to it --
	// retention horizons, materialisation lag, the default event window --
	// so a test can pin it rather than racing the wall clock across a
	// boundary.
	now func() time.Time
}

func (h *readHandler) register(mux *http.ServeMux) {
	mux.Handle("GET /api/v1/hosts", http.HandlerFunc(h.hosts))
	mux.Handle("GET /api/v1/hosts/{id}", http.HandlerFunc(h.host))
	mux.Handle("GET /api/v1/hosts/{id}/containers", listing(h.svc.Containers))
	mux.Handle("GET /api/v1/hosts/{id}/filesystems", listing(h.svc.Filesystems))
	mux.Handle("GET /api/v1/hosts/{id}/addresses", listing(h.svc.Addresses))
	mux.Handle("GET /api/v1/hosts/{id}/interfaces", listing(h.svc.Interfaces))
	mux.Handle("GET /api/v1/hosts/{id}/drives", listing(h.svc.Drives))
	mux.Handle("GET /api/v1/hosts/{id}/packages", listing(h.svc.Packages))
	mux.Handle("GET /api/v1/hosts/{id}/units", listing(h.svc.Units))
	mux.Handle("GET /api/v1/hosts/{id}/metrics", http.HandlerFunc(h.metrics))
	// The fleet form of the line above, and NOT under /hosts/: it is one
	// question about many hosts rather than a collection under one of them.
	// It answers to the same admin credential as everything else on
	// /api/v1/ -- see NewRouter, which mounts the whole prefix behind
	// RequireAdmin.
	mux.Handle("GET /api/v1/metrics", http.HandlerFunc(h.fleetMetrics))
	mux.Handle("GET /api/v1/events", http.HandlerFunc(h.events))
}

// listing adapts the seven dimension listings, which differ only in their row
// type. A generic wrapper rather than seven near-identical handlers, so a fix
// to the id parsing or the error mapping cannot land on six of them.
//
// A free function rather than a method because Go has no generic methods.
func listing[T any](q func(context.Context, int32) ([]T, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		rows, err := q(r.Context(), id)
		if err != nil {
			writeReadError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, rows)
	})
}

func (h *readHandler) hosts(w http.ResponseWriter, r *http.Request) {
	hosts, err := h.svc.ListHosts(r.Context())
	if err != nil {
		writeReadError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, hosts)
}

func (h *readHandler) host(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	host, err := h.svc.Host(r.Context(), id)
	if err != nil {
		writeReadError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, host)
}

func (h *readHandler) metrics(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()

	from, err := parseTime(q.Get("from"), "from")
	if err != nil {
		writeReadError(w, r, err)
		return
	}
	to, err := parseTime(q.Get("to"), "to")
	if err != nil {
		writeReadError(w, r, err)
		return
	}
	step, stepSet, err := parseStep(q.Get("step"))
	if err != nil {
		writeReadError(w, r, err)
		return
	}

	res, err := h.svc.Metrics(r.Context(), read.MetricsQuery{
		HostID:  id,
		Family:  q.Get("family"),
		From:    from,
		To:      to,
		Step:    step,
		StepSet: stepSet,
		Columns: splitColumns(q["columns"]),
	}, h.now())
	if err != nil {
		writeReadError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (h *readHandler) fleetMetrics(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	hostIDs, err := parseHostIDs(q["hosts"])
	if err != nil {
		writeReadError(w, r, err)
		return
	}

	from, err := parseTime(q.Get("from"), "from")
	if err != nil {
		writeReadError(w, r, err)
		return
	}
	to, err := parseTime(q.Get("to"), "to")
	if err != nil {
		writeReadError(w, r, err)
		return
	}
	step, stepSet, err := parseStep(q.Get("step"))
	if err != nil {
		writeReadError(w, r, err)
		return
	}

	res, err := h.svc.FleetMetrics(r.Context(), read.FleetMetricsQuery{
		HostIDs: hostIDs,
		Family:  q.Get("family"),
		From:    from,
		To:      to,
		Step:    step,
		StepSet: stepSet,
		Columns: splitColumns(q["columns"]),
	}, h.now())
	if err != nil {
		writeReadError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// parseHostIDs reads ?hosts=1,2,3 -- and ?hosts=1&hosts=2, on splitColumns'
// reasoning that a caller building the query from a list will reach for one
// form or the other and neither is wrong.
//
// An empty list is a 400 rather than an empty 200. "Every host" is a tempting
// default and the wrong one here: it would turn a caller's own bug -- a page
// rendering before its host list arrived -- into the most expensive query the
// hub can run, silently.
func parseHostIDs(values []string) ([]int32, error) {
	raw := splitColumns(values)
	if len(raw) == 0 {
		return nil, invalidf("hosts must name at least one host id")
	}
	out := make([]int32, 0, len(raw))
	for _, r := range raw {
		id, ok := parseID(r)
		if !ok {
			return nil, invalidf("hosts must be a comma-separated list of integer host ids; %q is not one", r)
		}
		out = append(out, id)
	}
	return out, nil
}

func (h *readHandler) events(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	var hostID int32
	if raw := q.Get("host"); raw != "" {
		id, ok := parseID(raw)
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "host must be an integer"})
			return
		}
		hostID = id
	}

	since, err := parseTime(q.Get("since"), "since")
	if err != nil {
		writeReadError(w, r, err)
		return
	}
	until, err := parseTime(q.Get("until"), "until")
	if err != nil {
		writeReadError(w, r, err)
		return
	}

	var limit int
	if raw := q.Get("limit"); raw != "" {
		n, convErr := strconv.Atoi(raw)
		if convErr != nil || n < 1 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "limit must be a positive integer"})
			return
		}
		limit = n
	}

	events, err := h.svc.Events(r.Context(), read.EventQuery{
		HostID: hostID,
		Since:  since,
		Until:  until,
		Type:   q.Get("type"),
		Limit:  limit,
	}, h.now())
	if err != nil {
		writeReadError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, events)
}

// parseTime accepts RFC 3339 or unix milliseconds, and reports the zero time
// for an absent value so the service can apply its own default.
//
// Milliseconds are accepted because that is what the response's points carry:
// a client charting a series and then asking for a narrower window around a
// spike should be able to pass a timestamp straight back rather than
// reformatting it, and a round trip that changes representation is a round
// trip that can lose a millisecond.
func parseTime(raw, field string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, nil
	}
	if ms, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return time.UnixMilli(ms).UTC(), nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, invalidf("%s must be RFC 3339 or unix milliseconds", field)
	}
	return t.UTC(), nil
}

// parseStep accepts a Go duration. Which tier that resolves to is the
// service's decision, not this function's -- see selectTier.
func parseStep(raw string) (time.Duration, bool, error) {
	if raw == "" {
		return 0, false, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, false, invalidf("step must be a duration such as 60s, 5m or 1h")
	}
	if d <= 0 {
		return 0, false, invalidf("step must be positive")
	}
	return d, true, nil
}

// splitColumns accepts both ?columns=a,b and ?columns=a&columns=b, because a
// caller building the query from a list will reach for one or the other and
// neither is wrong.
func splitColumns(values []string) []string {
	var out []string
	for _, v := range values {
		for _, part := range strings.Split(v, ",") {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}

// invalidf builds a read.ErrInvalid whose message reaches the client, so it
// must name what is wrong without quoting SQL.
func invalidf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", read.ErrInvalid, fmt.Sprintf(format, args...))
}

// writeReadError maps a read error to a status, on the same rule
// writeAdminError follows: only the sentinel errors reach the client as a
// message, and anything else is logged and answered with a bare 500 so an
// internal detail never leaves the process.
func writeReadError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, read.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	case errors.Is(err, read.ErrInvalid):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	default:
		slog.Error("read request failed", "path", r.URL.Path, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
	}
}
