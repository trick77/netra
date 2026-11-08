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
