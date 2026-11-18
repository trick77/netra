package admin

import "time"

// Host is one row of the admin host list.
//
// Table: hosts
type Host struct {
	ID       int32
	Hostname string
	// LastSeen is nil when the host has never posted a sample.
	LastSeen *time.Time
}

// Mute is one notification mute.
//
// Table: alert_mutes
type Mute struct {
	ID       int32
	HostID   int32
	Hostname string
	// Kind is empty when the whole host is muted.
	Kind string
	// Until is nil for a mute that lasts until lifted.
	Until   *time.Time
	Note    string
	Created time.Time
}
