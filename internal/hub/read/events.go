package read

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Event is one row of /api/v1/events.
type Event struct {
	ID       int32     `json:"id"`
	HostID   int32     `json:"host_id"`
	Hostname string    `json:"hostname"`
	TS       time.Time `json:"ts"`
	Type     string    `json:"type"`
	// Subject is the thing the event is about -- an array name, a device, an
	// interface -- and is null for events about the host as a whole, such as
	// an agent upgrade.
	Subject *string `json:"subject"`
	// Detail is the event's payload, passed through as stored. Its shape is
	// the emitting collector's, not this API's.
	Detail json.RawMessage `json:"detail"`
}

// EventQuery is a parsed /api/v1/events request. A zero HostID means every
// host; a zero Since means the default window.
type EventQuery struct {
	HostID int32
	Since  time.Time
	Until  time.Time
	Type   string
	Limit  int
}

// Event query bounds. The default window is a day because the endpoint
// answers "what happened recently"; the limit exists because a fleet-wide
// query with no host filter reads a table that spans every host, and a
// crash-looping agent can put a lot in it.
const (
	defaultEventWindow = 24 * time.Hour
	defaultEventLimit  = 500
	maxEventLimit      = 5000
)

// Events returns the discrete-state log, newest first.
//
// The host filter is optional on purpose: mdraid degradation and agent version
// changes are as often read across the fleet as per host. With a host given
// the query rides events_host_id_ts_idx; without one it is an ordered scan
// bounded by since and the limit.
func (s *Service) Events(ctx context.Context, q EventQuery, now time.Time) ([]Event, error) {
	if q.HostID != 0 {
		if err := s.hostExists(ctx, q.HostID); err != nil {
			return nil, err
		}
	}

	since, until := q.Since, q.Until
	if until.IsZero() {
		until = now
	}
	if since.IsZero() {
		since = until.Add(-defaultEventWindow)
	}
	if !since.Before(until) {
		return nil, fmt.Errorf("%w: since must be before until", ErrInvalid)
	}

	limit := q.Limit
	switch {
	case limit <= 0:
		limit = defaultEventLimit
	case limit > maxEventLimit:
		// Clamped rather than rejected: a caller asking for more than the
		// cap wants as much as they can have, and newest-first ordering
		// means the rows they get are the ones they care about.
		limit = maxEventLimit
	}

	// A zero host id and an empty type mean "no filter". Encoding that in SQL
	// with a NULL parameter rather than assembling the WHERE clause in Go
	// keeps this one prepared statement whatever the request looks like.
	var hostFilter *int32
	if q.HostID != 0 {
		hostFilter = &q.HostID
	}
	var typeFilter *string
	if q.Type != "" {
		typeFilter = &q.Type
	}

	rows, err := s.pool.Query(ctx, `
		SELECT e.id, e.host_id, coalesce(h.hostname, ''), e.ts, e.type, e.subject, e.detail
		  FROM events e
		  JOIN hosts h ON h.id = e.host_id
		 WHERE ($1::integer IS NULL OR e.host_id = $1)
		   AND ($2::text IS NULL OR e.type = $2)
		   AND e.ts >= $3 AND e.ts <= $4
		 ORDER BY e.ts DESC, e.id DESC
		 LIMIT $5`, hostFilter, typeFilter, since, until, limit)
	if err != nil {
		return nil, fmt.Errorf("query events: %w", err)
	}
	defer rows.Close()

	out := []Event{}
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.HostID, &e.Hostname, &e.TS, &e.Type, &e.Subject, &e.Detail); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		out = append(out, e)
	}
	return out, rowsErr(rows.Err(), "events")
}
