package read

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/trick77/netra/internal/hub/systemdstate"
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
	// ServicesTotal and ServicesFailed are the host's systemd service counts
	// as of its last scrape.
	//
	// Gauges here rather than a count of the /units response, which is what
	// the host page used to do. That endpoint now returns only the units that
	// need attention, so counting it would report "0 units" for a healthy host
	// running several hundred. These are the numbers the agent actually
	// reported. Null on a host with no systemd, which is a different fact from
	// zero -- see Capabilities.
	ServicesTotal  *int32 `json:"services_total"`
	ServicesFailed *int32 `json:"services_failed"`
	// FailedUnits names up to failedUnitNames of the failed units behind
	// ServicesFailed, alphabetically.
	//
	// A NAME, never a count. ServicesFailed is what the agent summarised and
	// stays the number anything counts; these are read from systemd_units,
	// which the hub fills from the unit snapshot, and the two can legitimately
	// disagree -- a host heard from once has a summary and no unit rows yet.
	// So this annotates the count and never replaces it: an empty list is "the
	// hub cannot name them", which is not "none failed".
	//
	// It exists because the fleet band's row was a dead end. "1 failed unit"
	// is the fleet answering whether a host is worth opening, and it made the
	// reader open the host to learn the one word that would have told them
	// whether it mattered.
	//
	// Capped rather than complete: a host mid-cascade fails forty units, and
	// the band is one line per condition. The names are the first three by
	// name and the count says how many there are in total.
	FailedUnits []string `json:"failed_units"`
	// FailedSince is when the OLDEST of those units entered its current state:
	// systemd's own timestamp, not when the hub heard about it.
	//
	// One timestamp for the whole set, and the oldest rather than the newest,
	// because the fleet list states this as one condition per host. Five units
	// failing at five different times is one thing that has been wrong since
	// the first of them went, and a reader scanning the column for "how long
	// has this been broken" is asking about that first one.
	//
	// Null is real and common: state_ts is nullable (a unit whose state the
	// agent reported without a timestamp), and a host with a failed-unit count
	// but no unit rows yet has nothing to take a minimum over. The list simply
	// leaves the column empty rather than substituting now().
	//
	// Taken over EVERY notable unit, not just the three FailedUnits names: the
	// cap is about how much fits on a line, and the onset is about when the
	// condition started.
	FailedSince *time.Time `json:"failed_since"`
	// Threads is inventory rather than a gauge, and it is here because the
	// fleet list's CPU sparkline is a per-core stack: the page has to know
	// how many logical CPUs a host has BEFORE deciding to ask for one series
	// per core. Without it every host would be asked blind, including the
	// 128-thread ones the read API has no way to reduce server-side.
	Threads *int32 `json:"threads"`
	// Where the host is, verbatim as its own agent reports it
	// (AGENT_LOCATION, AGENT_PROVIDER, AGENT_FACILITY -- see
	// internal/agent/config/config.go and the 0009 migration).
	//
	// On the SUMMARY rather than the detail, and that placement is the whole
	// reason the fleet needs no extra request: the list is what draws a
	// location under every hostname, and resolving it any other way meant a
	// second whole-table read joined client-side by id. HostDetail embeds this
	// struct, so the host page gets the same three fields for free -- and they
	// must not be redeclared there, for the reason the Threads comment above
	// gives.
	//
	// Free text, not identifiers. The agent sends whatever the operator wrote,
	// so Location is "Roubaix, France" and nothing parses it into a city and a
	// country -- there is no ISO code here to look up and nothing to guess at.
	// NULL is "not reported", which is every host whose operator set none of
	// the variables, and is distinct from an empty string (SaveMetadata's
	// NULLIF keeps those out).
	//
	// Unrelated to SiteID above and to the sites/providers tables it points
	// at. That pair is filled in by a human through the admin UI; this is the
	// machine's own account of itself, and nothing here creates a site.
	Location *string `json:"location"`
	Provider *string `json:"provider"`
	Facility *string `json:"facility"`

	// Capabilities is what each collector reported about its own
	// availability, verbatim from hosts.capabilities.
	//
	// This is not decoration. It is the ONLY way to tell "this host has no
	// hwmon" from "the sensors collector never ran" -- without it every NULL
	// in every other response is ambiguous in exactly the way the agent went
	// to trouble to avoid, and the read API would quietly undo a decision the
	// collectors already paid for. A host whose agent reported nothing carries
	// {} rather than null, matching the column's default.
	//
	// The list coalesces it in SQL rather than repairing a nil map in Go,
	// matching coalesce(h.hostname, '') one line above it: the column is NOT
	// NULL DEFAULT '{}', so the Go branch was unreachable and could only ever
	// be a line no test could cover.
	//
	// It sits on the SUMMARY, not on the detail, and that placement is the
	// point: it used to be detail-only, on the reasoning that a per-host
	// answer needs a per-host request. But the absences it explains are
	// fleet-wide. A host whose cgroup hierarchy is not mounted reports
	// `containers: no-cgroup-scopes` and then contributes no containers at
	// all, so the fleet's container list is silently short by one host --
	// and the list endpoint is the only thing that page asks. Explaining that
	// from the detail endpoint would mean a second fan-out across the whole
	// fleet to render one sentence. One JSONB column per row is cheaper than
	// one request per host.
	//
	// The list and the detail still differ in everything else: the detail
	// carries the host's full inventory, its site and provider join, and its
	// coordinates, none of which belongs on a row of a list.
	Capabilities map[string]string `json:"capabilities"`
}

// HostDetail is everything the hub knows about one host that is not a time
// series.
type HostDetail struct {
	HostSummary

	// The site join is gone. It served site_name, provider_name and the site's
	// own facility and country, and every one of those answered "where is this
	// machine" from a table a human has to fill in by hand -- while the agent
	// had been reporting the answer on every metadata post and the hub had
	// been discarding it. Location/Provider/Facility on the embedded
	// HostSummary are that answer. The sites tables still exist and SiteID
	// still points at them; nothing reads them for a location any more.

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
	// Capabilities is not repeated here either, for the same reason as
	// Threads: it moved up to HostSummary when the fleet's container list
	// needed it to explain a host that reports nothing. Declaring it in both
	// would shadow the embedded field, and the JSON would look right while
	// HostSummary.Capabilities stayed empty for every caller that read it
	// through the embed.
}

// failedUnitNames is how many failed units the list names per host. See
// HostSummary.FailedUnits for why it is capped at all.
const failedUnitNames = 3

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
		SELECT h.id, coalesce(h.hostname, ''), h.site_id,
		       c.last_seen, c.cpu_total, c.mem_used, c.mem_total, c.uptime_s,
		       c.net_rx_bytes, c.net_tx_bytes, c.services_total, c.services_failed,
		       h.threads, coalesce(h.capabilities, '{}'::jsonb),
		       h.location, h.provider, h.facility,
		       coalesce(fu.names, '{}'::text[]), fu.since
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
		  ) fu ON TRUE
		 ORDER BY h.hostname, h.id`, failedUnitNames)
	if err != nil {
		return nil, fmt.Errorf("query hosts: %w", err)
	}
	defer rows.Close()

	hosts := []HostSummary{}
	for rows.Next() {
		var h HostSummary
		if err := rows.Scan(&h.ID, &h.Hostname, &h.SiteID,
			&h.LastSeen, &h.CPUTotal, &h.MemUsed, &h.MemTotal, &h.UptimeS,
			&h.NetRxBytes, &h.NetTxBytes, &h.ServicesTotal, &h.ServicesFailed,
			&h.Threads, &h.Capabilities,
			&h.Location, &h.Provider, &h.Facility,
			&h.FailedUnits, &h.FailedSince); err != nil {
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
		SELECT h.id, coalesce(h.hostname, ''), h.site_id,
		       c.last_seen, c.cpu_total, c.mem_used, c.mem_total, c.uptime_s,
		       c.net_rx_bytes, c.net_tx_bytes, c.services_total, c.services_failed,
		       h.location, h.provider, h.facility,
		       h.fingerprint, h.host_type, h.agent_version, h.go_version, h.build_commit,
		       h.kernel, h.os_name, h.arch, h.cpu_model, h.cores, h.threads, h.memory_total,
		       h.latitude, h.longitude, h.created_at, h.capabilities,
		       coalesce(fu.names, '{}'::text[]), fu.since
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
		  ) fu ON TRUE
		 WHERE h.id = $1`, hostID, failedUnitNames).Scan(
		&h.ID, &h.Hostname, &h.SiteID,
		&h.LastSeen, &h.CPUTotal, &h.MemUsed, &h.MemTotal, &h.UptimeS,
		&h.NetRxBytes, &h.NetTxBytes, &h.ServicesTotal, &h.ServicesFailed,
		&h.Location, &h.Provider, &h.Facility,
		&h.Fingerprint, &h.HostType, &h.AgentVersion, &h.GoVersion, &h.BuildCommit,
		&h.Kernel, &h.OSName, &h.Arch, &h.CPUModel, &h.Cores, &h.Threads, &h.MemoryTotal,
		&h.Latitude, &h.Longitude, &h.CreatedAt, &h.Capabilities, &h.FailedUnits,
		&h.FailedSince)
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
