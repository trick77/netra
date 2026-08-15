package read

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// HostSummary is one row of the host list. Every metric on it comes from
// host_current, which carries exactly one row per host: the list must stay
// cheap however much history sits behind it, so it touches no hypertable
// (spec 8).
//
// The gauges are pointers because a registered host that has never posted has
// no reading at all, and 0% CPU on a machine that has never been heard from
// is a different and much more misleading claim than "nothing yet".
type HostSummary struct {
	ID       int32      `json:"id"`
	Hostname string     `json:"hostname"`
	SiteID   *int32     `json:"site_id"`
	LastSeen *time.Time `json:"last_seen"`
	CPUTotal *float64   `json:"cpu_total"`
	MemUsed  *int64     `json:"mem_used"`
	MemTotal *int64     `json:"mem_total"`
	UptimeS  *int64     `json:"uptime_s"`
	// NetRxBytes and NetTxBytes are the host's traffic summed over its
	// interfaces at its last scrape, in bytes per second.
	//
	// A gauge rather than a series lookup on purpose. This is the number the
	// fleet prints as "ingress + egress", and taking it off the end of a
	// fetched series made it depend on the range those series were drawn
	// over: the range picks the step, the step picks the tier, and the 5m
	// tier answers a five-minute average that ended a quarter of an hour ago
	// where the raw tier answers the rate now. A current rate must not change
	// because somebody widened a chart.
	//
	// The names are rx/tx, matching net_samples and /proc/net/dev, which is
	// where the agent reads them. Only what a person reads says ingress and
	// egress.
	NetRxBytes *float64 `json:"net_rx_bytes"`
	NetTxBytes *float64 `json:"net_tx_bytes"`
	// Threads is inventory rather than a gauge, and it is here because the
	// fleet list's CPU sparkline is a per-core stack: the page has to know
	// how many logical CPUs a host has BEFORE deciding to ask for one series
	// per core. Without it every host would be asked blind, including the
	// 128-thread ones the read API has no way to reduce server-side.
	Threads *int32 `json:"threads"`
}

// HostDetail is everything the hub knows about one host that is not a time
// series.
type HostDetail struct {
	HostSummary

	SiteName     *string `json:"site_name"`
	ProviderName *string `json:"provider_name"`

	Fingerprint  *string `json:"fingerprint"`
	HostType     *string `json:"host_type"`
	AgentVersion *string `json:"agent_version"`
	GoVersion    *string `json:"go_version"`
	BuildCommit  *string `json:"build_commit"`
	Kernel       *string `json:"kernel"`
	OSName       *string `json:"os_name"`
	Arch         *string `json:"arch"`
	CPUModel     *string `json:"cpu_model"`
	Cores        *int32  `json:"cores"`
	// Threads is not repeated here: it moved up to HostSummary, which this
	// embeds, when the fleet list started needing it to size its per-core
	// CPU stack. Declaring it in both would shadow the embedded field, and
	// the detail query would fill one while the list filled the other.
	MemoryTotal *int64 `json:"memory_total"`

	Latitude  *float64  `json:"latitude"`
	Longitude *float64  `json:"longitude"`
	CreatedAt time.Time `json:"created_at"`

	// Capabilities is what each collector reported about its own
	// availability, verbatim from hosts.capabilities.
	//
	// This is not decoration, and it is the reason this endpoint exists
	// separately from the list. It is the ONLY way to tell "this host has no
	// hwmon" from "the sensors collector never ran" -- without it every NULL
	// in every other response is ambiguous in exactly the way the agent went
	// to trouble to avoid, and the read API would quietly undo a decision the
	// collectors already paid for. A host whose agent reported nothing
	// carries {} rather than null, matching the column's default.
	Capabilities map[string]string `json:"capabilities"`
}

// ListHosts returns every host with its current gauges, ordered by hostname.
func (s *Service) ListHosts(ctx context.Context) ([]HostSummary, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT h.id, coalesce(h.hostname, ''), h.site_id,
		       c.last_seen, c.cpu_total, c.mem_used, c.mem_total, c.uptime_s,
		       c.net_rx_bytes, c.net_tx_bytes,
		       h.threads
		  FROM hosts h
		  LEFT JOIN host_current c ON c.host_id = h.id
		 ORDER BY h.hostname, h.id`)
	if err != nil {
		return nil, fmt.Errorf("query hosts: %w", err)
	}
	defer rows.Close()

	hosts := []HostSummary{}
	for rows.Next() {
		var h HostSummary
		if err := rows.Scan(&h.ID, &h.Hostname, &h.SiteID,
			&h.LastSeen, &h.CPUTotal, &h.MemUsed, &h.MemTotal, &h.UptimeS,
			&h.NetRxBytes, &h.NetTxBytes,
			&h.Threads); err != nil {
			return nil, fmt.Errorf("scan host: %w", err)
		}
		hosts = append(hosts, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate hosts: %w", err)
	}
	return hosts, nil
}

// Host returns one host's full metadata, including its collector
// capabilities.
func (s *Service) Host(ctx context.Context, hostID int32) (HostDetail, error) {
	var h HostDetail

	err := s.pool.QueryRow(ctx, `
		SELECT h.id, coalesce(h.hostname, ''), h.site_id,
		       c.last_seen, c.cpu_total, c.mem_used, c.mem_total, c.uptime_s,
		       c.net_rx_bytes, c.net_tx_bytes,
		       si.name, p.name,
		       h.fingerprint, h.host_type, h.agent_version, h.go_version, h.build_commit,
		       h.kernel, h.os_name, h.arch, h.cpu_model, h.cores, h.threads, h.memory_total,
		       h.latitude, h.longitude, h.created_at, h.capabilities
		  FROM hosts h
		  LEFT JOIN host_current c ON c.host_id = h.id
		  LEFT JOIN sites si ON si.id = h.site_id
		  LEFT JOIN providers p ON p.id = si.provider_id
		 WHERE h.id = $1`, hostID).Scan(
		&h.ID, &h.Hostname, &h.SiteID,
		&h.LastSeen, &h.CPUTotal, &h.MemUsed, &h.MemTotal, &h.UptimeS,
		&h.NetRxBytes, &h.NetTxBytes,
		&h.SiteName, &h.ProviderName,
		&h.Fingerprint, &h.HostType, &h.AgentVersion, &h.GoVersion, &h.BuildCommit,
		&h.Kernel, &h.OSName, &h.Arch, &h.CPUModel, &h.Cores, &h.Threads, &h.MemoryTotal,
		&h.Latitude, &h.Longitude, &h.CreatedAt, &h.Capabilities)
	if errors.Is(err, pgx.ErrNoRows) {
		return HostDetail{}, ErrNotFound
	}
	if err != nil {
		return HostDetail{}, fmt.Errorf("query host: %w", err)
	}

	// The column is NOT NULL DEFAULT '{}', so this is belt and braces -- but
	// a nil map renders as null, and null would mean "we do not know what
	// this agent can collect", which is precisely the ambiguity capabilities
	// exist to remove.
	if h.Capabilities == nil {
		h.Capabilities = map[string]string{}
	}
	return h, nil
}
