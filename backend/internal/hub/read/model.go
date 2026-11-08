package read

import (
	"encoding/json"
	"time"
)

// Event is one row of /api/v1/events.
//
// Table: -
type Event struct {
	// ID is a string because the log unions three tables with three different
	// keys: events has an integer identity, package_events is keyed
	// (host_id, ts, name) and systemd_unit_events (host_id, unit_id, ts).
	// Synthesising integers across those keyspaces invites a collision that
	// would silently drop a row from a React list; a prefixed string cannot.
	// Nothing reads it apart from the row key, so it costs nothing.
	ID       string    `json:"id"`
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
	// Severity is "info", "warning" or "critical", and every branch of the
	// union answers it.
	//
	// It is a column on `events` and a derivation on the other two, which is
	// deliberate rather than an inconsistency. A unit's severity is a pure
	// function of its state -- systemdstate.NotableSQL, one definition --
	// so storing it would duplicate a derived value AND let it drift, since
	// ApplySystemdSnapshot rewrites unit state and a stored severity would not
	// follow. A package upgrade is a fact, not an emergency, and is always
	// info.
	//
	// The only channel. It was duplicated into the detail JSON while the UI
	// read it from there; both event views take it from here now, and the
	// producers have stopped writing the key.
	Severity string `json:"severity"`
}

// Condition is one open condition, as the fleet page reads it.
//
// The row is the truth and the events are the history -- 0016 is explicit about
// which way round that is, because the log is pruned at 90 days and a condition
// open longer than that still has to be able to say when it began. So `since`
// comes from here and is never looked up from the event that opened it.
//
// Table: host_conditions
type Condition struct {
	ID       int64  `json:"id"`
	HostID   int32  `json:"host_id"`
	Hostname string `json:"hostname"`
	Kind     string `json:"kind"`
	// Subject is a mount label for disk, a device for drive, and empty for a
	// condition about the host as a whole. Finer than the page displays: the
	// collapse to one row per host is the renderer's job.
	Subject  string `json:"subject"`
	Severity string `json:"severity"`
	// OpenedTS is the onset, walked once when the condition opened.
	OpenedTS time.Time `json:"opened_ts"`
	// OpenedAtLeast marks an onset that is a FLOOR rather than a moment,
	// because the walk hit the end of what is retained. The page says "over 7 d"
	// rather than naming a bucket where nothing happened.
	OpenedAtLeast bool `json:"opened_at_least"`
	// Detail is the condition's own numbers, shaped by the kind, for the
	// sentence the page writes.
	Detail json.RawMessage `json:"detail"`

	// MeasuredTS is when this condition's SUBJECT was last actually measured,
	// for the kinds that have a subject to measure. Null for the host-wide ones,
	// whose subject is the host and whose measurement is last_seen.
	MeasuredTS *time.Time `json:"measured_ts"`
	// Stale says the subject is present and unmeasurable: still listed, and not
	// re-read for longer than its kind allows.
	//
	// It exists because the hub deliberately never resolves such a condition.
	// A mount the agent cannot stat produces no sample at all and a hung NFS
	// export freezes its reading indefinitely, which from here is byte for byte
	// what an unmounted volume looks like -- so declaring one gone would
	// eventually declare a still-full wedged disk recovered.
	//
	// The browser used to make that go away by dropping the mount after three
	// minutes, which is the same lie told quietly. So the row is served, marked,
	// and the page says the reading is old instead of pretending the problem
	// ended.
	Stale bool `json:"stale"`
}

// HostSummary is one row of the host list. Every metric on it comes from
// host_current, which carries exactly one row per host: the list must stay
// cheap however much history sits behind it, so it touches no hypertable
// (spec 8).
//
// The gauges are pointers because a registered host that has never posted has
// no reading at all, and 0% CPU on a machine that has never been heard from
// is a different and much more misleading claim than "nothing yet".
//
// Table: hosts
type HostSummary struct {
	ID       int32      `json:"id"`
	Hostname string     `json:"hostname"`
	LastSeen *time.Time `json:"last_seen"`
	// ReceivedAt is when the hub last took a batch from this host, on the
	// hub's clock; last_seen is the agent's. Online/offline is judged on it.
	ReceivedAt *time.Time `json:"received_at"`
	CPUTotal   *float64   `json:"cpu_total"`
	MemUsed    *int64     `json:"mem_used"`
	MemTotal   *int64     `json:"mem_total"`
	UptimeS    *int64     `json:"uptime_s"`
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
	Location *string `json:"location"`
	Provider *string `json:"provider"`
	Facility *string `json:"facility"`

	// The distribution, as the agent's os-release reported it -- "Debian 13",
	// "Ubuntu 24.04". On the SUMMARY as well as the detail because the fleet
	// list draws a distribution mark beside every hostname and prints the
	// release on the line under it; without it the list would have to fan out
	// one detail request per host to label a row. NULL is a host that has not
	// posted metadata yet, which draws no mark and no words rather than a
	// placeholder.
	OSName *string `json:"os_name"`

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
	// matching coalesce(h.hostname, '') in ListHosts' SELECT: the column is NOT
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
	// Filesystems is every mount on this host that has ever reported bytes,
	// each with its last reading and the timestamp of that reading.
	//
	// On the SUMMARY for the same reason Capabilities is: the absence it
	// repairs is fleet-wide. The fleet's Disk cell used to take its figure
	// from the last slot of the answered window, so a host that had been
	// switched off longer than that window lost the cell entirely -- and disk
	// fullness is the one saturation reading that survives an outage intact.
	// Answering that from a metrics fetch would mean one request per host to
	// fill one column.
	//
	// Bytes only, no series: the filesystem metric family still owns the
	// history, and the sparkline beside the figure is still drawn from it,
	// gaps and all. Fullness is used / (used + free) -- see the comment on
	// filesystem_samples in 0001_init.sql, and 0013_filesystem_current.sql on
	// why the gauge is stored rather than derived.
	//
	// Never null: a host with no reporting mounts gets an empty array, which
	// is "asked, and there are none" rather than "not asked".
	Filesystems []Filesystem `json:"filesystems"`
}

// HostDetail is everything the hub knows about one host that is not a time
// series.
//
// Table: hosts
type HostDetail struct {
	HostSummary

	Fingerprint  *string `json:"fingerprint"`
	HostType     *string `json:"host_type"`
	AgentVersion *string `json:"agent_version"`
	GoVersion    *string `json:"go_version"`
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

// Container is one row of /hosts/{id}/containers.
//
// Table: containers
type Container struct {
	ID    int32   `json:"id"`
	Key   string  `json:"container_key"`
	Name  *string `json:"name"`
	Image *string `json:"image"`
	// IsAgent marks netra's own container. A fleet view that does not
	// separate it reports the monitoring as part of the workload.
	IsAgent bool `json:"is_agent"`
	// When this container's newest sample was TAKEN, from
	// containers.last_seen.
	//
	// The agent enumerates running containers, so a container that was
	// removed -- or merely stopped -- simply stops appearing, and this is the
	// only thing that says so: nothing on the wire announces a removal, and
	// the row itself is kept until someone purges it or the nightly prune
	// reaches it at 120 days.
	//
	// Stamped from the sample's ts, never from the hub's clock, for the same
	// reason as Drive.LastSeen: the agent replays buffered scrapes after an
	// outage, and now() would report overnight samples as taken on arrival.
	//
	// containers.first_seen is deliberately NOT here -- a hub timestamp, the
	// prune's floor, and shipping it beside an agent-clocked last_seen would
	// put a pair on the wire that can read first_seen > last_seen.
	LastSeen time.Time `json:"last_seen"`

	// What Docker itself says, as opposed to what the cgroup measures. Every
	// one is a pointer, and null means "not reported" rather than a default --
	// the same rule Unit.State follows below, and the reason the container page
	// carried a card naming these as uncollected for as long as they were.
	//
	// DockerState is running, paused or restarting. It can never be "exited":
	// sample rows are driven by the agent's cgroup walk, so a stopped container
	// emits nothing at all and its absence is what LastSeen reports.
	DockerState *string `json:"docker_state"`
	// Health is healthy, unhealthy, starting or none. "none" is a reading --
	// the image defines no HEALTHCHECK -- and is not the same as null.
	Health *string `json:"health"`
	// StateSince is when DockerState was entered, advanced only when the state
	// actually changes. Same rule as Unit.Since.
	StateSince *time.Time `json:"state_since"`
	// RestartCount is Docker's own counter for this container. It resets when a
	// container is recreated, so against a compose-keyed history a DECREASE is
	// a redeploy and an increase is a crash-restart.
	RestartCount *int64 `json:"restart_count"`
	// StartedAt is when Docker says this container's CURRENT incarnation came
	// up, and it is the only thing here that answers "how long has it been up".
	//
	// Not StateSince, which is when the HUB first observed a state: for a
	// container netra met yesterday that reads as yesterday however long it had
	// actually been running, and nothing else on this struct contradicts it.
	//
	// Uptime is now - StartedAt only while LastSeen is current. For a row that
	// has gone quiet the honest statement is "up for at least LastSeen -
	// StartedAt", because nothing here says the container is still running --
	// which is why this is an instant and not a duration: a duration would rot
	// between the write and the read.
	//
	// Null is "no agent could inspect it" -- no socket, or a socket that
	// refuses inspect -- the same fact RestartCount reports as null, and for
	// the same reason: they ride one inspect response. It never means "just
	// started".
	StartedAt *time.Time `json:"started_at"`
	// Labels is every label the daemon reported. An empty object is a container
	// with no labels; null is a container nobody could ask about.
	Labels map[string]string `json:"labels"`

	// RestartsWindow is how many times this container restarted inside
	// RestartWindow, SUMMED from the container_restart event log rather than
	// differenced from a counter -- see migration 0019.
	//
	// Summed, never counted: one event can carry a delta of three, because a
	// crash-looping container advances Docker's counter by more than one
	// between two observations. Counting rows would report that loop as a
	// single restart.
	//
	// This is the reading that catches a container which is broken without
	// ever LOOKING broken -- one that dies and comes back every few minutes is
	// docker_state "running" at almost every scrape, and no snapshot reveals
	// it. It is the same job Unit.Restarts1h does for systemd.
	//
	// Zero is a real answer: the log was read and there were none. It is not
	// the same as RestartCount being null, which is nobody having looked.
	RestartsWindow int64 `json:"restarts_window"`
	// RecreatesWindow is redeploys in the same window -- operator actions, not
	// faults. Kept apart from the restarts above because mixing them would
	// make every deploy look like an incident.
	RecreatesWindow int64 `json:"recreates_window"`
	// LastRestart is when the newest restart in the window was recorded, or
	// null for none. On an event whose detail says ts_source "observed" this
	// is an upper bound rather than a moment -- see 0019.
	LastRestart *time.Time `json:"last_restart"`
	// RestartsWindowSeconds is the window the two counts were taken over,
	// echoed so a client never has to assume the server's default.
	RestartsWindowSeconds int64 `json:"restarts_window_seconds"`
}

// Filesystem is one row of /hosts/{id}/filesystems, and the same shape the
// host list embeds per host.
//
// The capacity figures are the GAUGE, from filesystem_current -- the last
// reading netra has for this mount, with no window and no rollup lag. They are
// not the series: the filesystem metric family still answers "what has this
// disk been doing", and a consumer drawing a line wants that. This answers
// "how full is it", which for a machine that is switched off is still a
// question with a true answer. 0013_filesystem_current.sql has the argument.
//
// The comment on filesystem_samples in 0001_init.sql governs any fullness a
// consumer computes from these: used / (used + free), never used / total.
//
// Table: filesystems
type Filesystem struct {
	ID         int32   `json:"id"`
	Label      string  `json:"label"`
	Mountpoint *string `json:"mountpoint"`
	// DeviceID is the st_dev the collector dedups bind mounts by, not a
	// reference to the devices table.
	DeviceID *int64 `json:"device_id"`
	// TS dates the three figures below. Null when this mount has never
	// reported one.
	//
	// Load-bearing, not decoration: `filesystems` is never pruned, so a mount
	// that stopped being reported keeps its row and its last bytes forever.
	// Compared against the host's own last_seen it separates a host that is
	// off -- every mount's reading legitimately its last -- from a host that
	// is talking while this one mount is not, which is a retired mount and
	// must not be shown as a fact about now.
	TS    *time.Time `json:"ts"`
	Total *int64     `json:"total"`
	Used  *int64     `json:"used"`
	Free  *int64     `json:"free"`
}

// Address is one row of /hosts/{id}/addresses.
//
// Table: host_addresses
type Address struct {
	Iface   string  `json:"iface"`
	IfIndex *int32  `json:"if_index"`
	Address string  `json:"address"`
	Family  int16   `json:"family"`
	Scope   *string `json:"scope"`
	VRF     *string `json:"vrf"`

	Description *string   `json:"description"`
	FirstSeen   time.Time `json:"first_seen"`
	LastSeen    time.Time `json:"last_seen"`
}

// Interface is one row of /hosts/{id}/interfaces: one link, with no addresses
// on it.
//
// Separate from Address because an interface with no address is the case worth
// showing -- a failed bond, an unplugged spare NIC -- and an address-keyed list
// cannot hold one.
//
// Table: host_interfaces
type Interface struct {
	Iface   string `json:"iface"`
	IfIndex *int32 `json:"if_index"`
	// The kernel's operstate verbatim: up, down, unknown, lowerlayerdown,
	// dormant, testing, notpresent. Not a bool -- see HostInterface in the
	// proto for why the agent does not classify.
	OperState *string `json:"oper_state"`
	// NULL, not 0, wherever the kernel has no answer: a virtual device has no
	// link speed and a down one refuses to report its.
	SpeedMbps   *int64  `json:"speed_mbps"`
	Duplex      *string `json:"duplex"`
	MTU         *int32  `json:"mtu"`
	MAC         *string `json:"mac"`
	Description *string `json:"description"`
	// Whether the interface has a /sys/class/net/<iface>/device link, which
	// is a NIC on a bus rather than a bond, bridge or VLAN. NULL from an agent
	// older than the field or a host without sysfs.
	Physical *bool `json:"physical"`

	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
}

// Package is one row of /hosts/{id}/packages.
//
// Table: host_packages
type Package struct {
	Name      string    `json:"name"`
	Version   string    `json:"version"`
	Arch      string    `json:"arch"`
	Format    string    `json:"format"`
	SizeBytes *int64    `json:"size_bytes"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
	// When this package's version last moved, as opposed to when the row was
	// first written or last re-emitted. See migration 0008.
	VersionChangedAt time.Time `json:"version_changed_at"`
}

// Unit is one row of /hosts/{id}/units: a unit that needs attention, and the
// state it is in.
//
// The state is read straight off systemd_units. It used to be a LAST-VALUE
// lookup into systemd_unit_events, on the reasoning that the collector emits
// events rather than samples (spec 5.3) so "what state is this unit in" is by
// construction "what did the newest event say". That reasoning held only while
// every transition arrived; the agent now also sends a periodic snapshot so
// the hub can be told what IS, and the answer lives on the row. See Units
// in inventory.go and systemd_units in 0001_init.sql.
//
// Since is when the unit ENTERED this state, not when the hub last heard about
// it -- the write paths advance it only when the state actually changes. A
// unit with no state recorded yet reports null rather than "active", which
// would be a guess.
//
// Table: systemd_units
type Unit struct {
	ID       int32      `json:"id"`
	Name     string     `json:"unit_name"`
	State    *string    `json:"state"`
	Substate *string    `json:"substate"`
	Since    *time.Time `json:"since"`
	// Restarts1h is how many state changes this unit has recorded in the last
	// hour, and it is the ONLY thing that reveals a unit which is broken
	// without ever looking broken -- a service that runs for a few minutes,
	// dies, and comes back is healthy at nearly every scrape. See
	// systemdstate.FlapThreshold.
	Restarts1h int32 `json:"restarts_1h"`
}

// driveRow is one driveSelect row before it is folded: a drive, and at most
// one of its attributes.
//
// Table: -
type driveRow struct {
	device     string
	model      *string
	serial     *string
	sizeBytes  *int64
	lastSeen   time.Time
	attrID     *int16
	raw        *int64
	normalized *int16
}
