package read

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/trick77/netra/internal/hub/systemdstate"
)

// failedUnitNames is how many failed units the list names per host. See
// HostSummary.FailedUnits for why it is capped at all.
const failedUnitNames = 3

// filesystemsLateral is the per-host filesystem gauge, as one JSONB column.
//
// Shared between ListHosts and Host rather than repeated the way the
// failed-unit LATERAL above is, and the difference is deliberate: that one
// carries a different placeholder number on each side and so cannot be one
// string. This one takes no parameter, and HostDetail EMBEDS HostSummary --
// so selecting it on the list and forgetting it on the detail would publish a
// confident empty array to every host page, the exact trap the failed-unit
// comment describes. One string cannot be added to one side only.
//
// The join to filesystem_current is INNER: a mount that has never reported
// bytes has nothing to say here, and letting it through would inflate the
// "+N others" count the fleet cell prints beside the mount it names. The
// /hosts/{id}/filesystems endpoint joins the other way round, because there
// the question is what the host HAS.
//
// No hypertable, so the promise on HostSummary that this list touches none
// still holds -- filesystem_current is a plain table keyed on (host_id,
// fs_id), and this is one host's own rows.
const filesystemsLateral = `
		  LEFT JOIN LATERAL (
		       SELECT jsonb_agg(jsonb_build_object(
		                  'id', f.id, 'label', f.label,
		                  'mountpoint', f.mountpoint, 'device_id', f.device_id,
		                  'ts', c.ts, 'total', c.total,
		                  'used', c.used, 'free', c.free
		              ) ORDER BY f.label) AS rows
		         FROM filesystems f
		         JOIN filesystem_current c
		           ON c.fs_id = f.id AND c.host_id = f.host_id
		        WHERE f.host_id = h.id
		  ) fs ON TRUE`

// ListHosts returns every host with its current gauges, ordered by hostname.
func (s *Service) ListHosts(ctx context.Context) ([]HostSummary, error) {
	// The LATERAL reads systemd_units, which is a plain table with a unique
	// index on (host_id, unit_name) -- so the promise on HostSummary that this
	// list touches no hypertable still holds. The index leads on host_id, so
	// this is one host's own unit range per row (a few hundred at most, and
	// the state filter is applied within it) rather than anything that grows
	// with the history behind the list.
	//
	// systemdstate.NotableSQL rather than a literal state = 'failed': the
	// names here have to be the same units the /units endpoint lists, and that
	// endpoint applies the same predicate from the same place.
	rows, err := s.pool.Query(ctx, `
		SELECT h.id, coalesce(h.hostname, ''),
		       c.last_seen, c.received_at, c.cpu_total, c.mem_used, c.mem_total, c.uptime_s,
		       c.net_rx_bytes, c.net_tx_bytes, c.services_total, c.services_failed,
		       h.threads, coalesce(h.capabilities, '{}'::jsonb),
		       h.location, h.provider, h.facility, h.os_name,
		       coalesce(fu.names, '{}'::text[]), fu.since,
		       coalesce(fs.rows, '[]'::jsonb)
		  FROM hosts h
		  LEFT JOIN host_current c ON c.host_id = h.id
		  LEFT JOIN LATERAL (
		       -- One pass over the host's notable units, sliced two ways: the
		       -- first $1 names for the sentence, and the earliest state_ts
		       -- across ALL of them for the onset. A second subquery would
		       -- walk the same index range again to answer half of one
		       -- question.
		       SELECT (array_agg(unit_name ORDER BY unit_name))[1:$1] AS names,
		              min(state_ts) AS since
		         FROM systemd_units
		        WHERE host_id = h.id AND `+systemdstate.NotableSQL("")+`
		  ) fu ON TRUE`+filesystemsLateral+`
		 ORDER BY h.hostname, h.id`, failedUnitNames)
	if err != nil {
		return nil, fmt.Errorf("query hosts: %w", err)
	}
	defer rows.Close()

	hosts := []HostSummary{}
	for rows.Next() {
		var h HostSummary
		if err := rows.Scan(&h.ID, &h.Hostname,
			&h.LastSeen, &h.ReceivedAt, &h.CPUTotal, &h.MemUsed, &h.MemTotal, &h.UptimeS,
			&h.NetRxBytes, &h.NetTxBytes, &h.ServicesTotal, &h.ServicesFailed,
			&h.Threads, &h.Capabilities,
			&h.Location, &h.Provider, &h.Facility, &h.OSName,
			&h.FailedUnits, &h.FailedSince, &h.Filesystems); err != nil {
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

	// The failed-unit LATERAL is repeated here rather than shared with
	// ListHosts, and it has to be: HostDetail EMBEDS HostSummary, so a column
	// the list selects and this does not comes back as a confident null for
	// every host that asks for its own page -- the trap the Threads and
	// Capabilities comments both warn about, and the one the detail test for
	// services_failed exists to catch.
	err := s.pool.QueryRow(ctx, `
		SELECT h.id, coalesce(h.hostname, ''),
		       c.last_seen, c.received_at, c.cpu_total, c.mem_used, c.mem_total, c.uptime_s,
		       c.net_rx_bytes, c.net_tx_bytes, c.services_total, c.services_failed,
		       h.location, h.provider, h.facility,
		       h.fingerprint, h.host_type, h.agent_version, h.go_version,
		       h.kernel, h.os_name, h.arch, h.cpu_model, h.cores, h.threads, h.memory_total,
		       h.latitude, h.longitude, h.created_at, h.capabilities,
		       coalesce(fu.names, '{}'::text[]), fu.since,
		       coalesce(fs.rows, '[]'::jsonb)
		  FROM hosts h
		  LEFT JOIN host_current c ON c.host_id = h.id
		  LEFT JOIN LATERAL (
		       -- The same one-pass shape as ListHosts above; the detail
		       -- embeds the summary, so selecting on one side only would
		       -- publish a confident null on the other.
		       SELECT (array_agg(unit_name ORDER BY unit_name))[1:$2] AS names,
		              min(state_ts) AS since
		         FROM systemd_units
		        WHERE host_id = h.id AND `+systemdstate.NotableSQL("")+`
		  ) fu ON TRUE`+filesystemsLateral+`
		 WHERE h.id = $1`, hostID, failedUnitNames).Scan(
		&h.ID, &h.Hostname,
		&h.LastSeen, &h.ReceivedAt, &h.CPUTotal, &h.MemUsed, &h.MemTotal, &h.UptimeS,
		&h.NetRxBytes, &h.NetTxBytes, &h.ServicesTotal, &h.ServicesFailed,
		&h.Location, &h.Provider, &h.Facility,
		&h.Fingerprint, &h.HostType, &h.AgentVersion, &h.GoVersion,
		&h.Kernel, &h.OSName, &h.Arch, &h.CPUModel, &h.Cores, &h.Threads, &h.MemoryTotal,
		&h.Latitude, &h.Longitude, &h.CreatedAt, &h.Capabilities, &h.FailedUnits,
		&h.FailedSince, &h.Filesystems)
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
