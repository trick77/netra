package notify

import "time"

// Condition is one condition with something to say about it.
//
// Table: host_conditions
type Condition struct {
	ID       int32
	HostID   int32
	Hostname string
	Kind     string
	Subject  string
	Severity string
	OpenedTS time.Time
	// OpenedAtLeast marks OpenedTS as a floor rather than a moment.
	OpenedAtLeast bool
	// ResolvedTS and ResolvedReason are set for Cleared only.
	ResolvedTS     *time.Time
	ResolvedReason string
	Detail         map[string]any
	What           What
	// Muted is true while an alert_mutes row covers the host and kind.
	Muted bool
}

// Event is one critical event inside the freshness window.
//
// Table: events
type Event struct {
	ID       int32
	HostID   int32
	Hostname string
	TS       time.Time
	Type     string
	Subject  string
	Severity string
	Detail   map[string]any
	Muted    bool
}
