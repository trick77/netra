package read

import (
	"fmt"
	"sort"
	"strings"
)

// keySpec is one component of a series key: the name it carries in the
// response, and the SQL expression that produces it.
//
// The expression is a constant in this file and never comes from a request.
// s is the sample relation, d the dimension table joined to it.
type keySpec struct {
	name string
	expr string
}

// family is one metric family: the relations that hold it, what distinguishes
// one series from another within it, and which tiers exist.
type family struct {
	name string
	// table is the RAW relation. A tier's relation is table + tierSpec.suffix,
	// which is the naming convention 0001_init.sql follows without exception.
	table string
	// keys is what splits the family into series. Empty for the three
	// host-level families (host, host_snmp, agent), which have exactly one
	// series each.
	keys []keySpec
	// join is the dimension join, or empty when the key columns live on the
	// sample relation itself.
	//
	// Which of the two a family gets is not a style choice: spec 5.3 gives
	// disk_io_samples.device, net_samples.iface and
	// collector_samples.collector as bare names, while sensors, filesystems,
	// containers and devices are renameable identities behind a surrogate id
	// (5.1 rule 2) and must be joined so a rename does not fork history.
	join string
	// dimensionColumns are the sample-relation columns that carry the
	// dimension -- the id used by the join, or the bare name used as a key.
	// They are excluded from the value columns discovered per tier: they
	// identify the series rather than measure anything.
	dimensionColumns []string
	tiers            []tierSpec
}

// relation names the table or continuous aggregate holding one tier.
func (f *family) relation(t tierSpec) string { return f.table + t.suffix }

// families is the registry. The names are the API's, and the ones a client
// passes as ?family=.
var families = map[string]*family{
	"host": {
		name:  "host",
		table: "host_samples",
		tiers: rolledUpTiers,
	},
	"agent": {
		name:  "agent",
		table: "agent_samples",
		tiers: rolledUpTiers,
	},
	// The IP and ICMP MIBs. A second host-level family rather than more
	// columns on "host", because a TimescaleDB continuous aggregate cannot
	// gain a column -- adding one to host_samples means recreating its
	// rollups and losing every host metric's rolled-up history past raw
	// retention. See 0003_host_snmp_samples.sql.
	"host_snmp": {
		name:  "host_snmp",
		table: "host_snmp_samples",
		tiers: rolledUpTiers,
	},
	"cpu_core": {
		name:             "cpu_core",
		table:            "cpu_core_samples",
		keys:             []keySpec{{name: "core", expr: "s.core::text"}},
		dimensionColumns: []string{"core"},
		tiers:            rolledUpTiers,
	},
	"disk_io": {
		name:             "disk_io",
		table:            "disk_io_samples",
		keys:             []keySpec{{name: "device", expr: "s.device"}},
		dimensionColumns: []string{"device"},
		tiers:            rolledUpTiers,
	},
	"net": {
		name:             "net",
		table:            "net_samples",
		keys:             []keySpec{{name: "iface", expr: "s.iface"}},
		dimensionColumns: []string{"iface"},
		tiers:            rolledUpTiers,
	},
	"collector": {
		name:             "collector",
		table:            "collector_samples",
		keys:             []keySpec{{name: "collector", expr: "s.collector"}},
		dimensionColumns: []string{"collector"},
		tiers:            rolledUpTiers,
	},
	"sensor": {
		name:  "sensor",
		table: "sensor_samples",
		keys: []keySpec{
			{name: "chip", expr: "d.chip"},
			{name: "label", expr: "d.label"},
			// Part of the series identity, not a value: a client charting
			// these has to know a 1200 RPM fan does not belong on the same
			// axis as a 45 degree package.
			{name: "kind", expr: "d.kind"},
		},
		join:             "JOIN sensors d ON d.id = s.sensor_id AND d.host_id = s.host_id",
		dimensionColumns: []string{"sensor_id"},
		tiers:            rolledUpTiers,
	},
	"container": {
		name:             "container",
		table:            "container_samples",
		keys:             []keySpec{{name: "container", expr: "d.container_key"}},
		join:             "JOIN containers d ON d.id = s.container_id AND d.host_id = s.host_id",
		dimensionColumns: []string{"container_id"},
		tiers:            rolledUpTiers,
	},
	// The value columns here are total, used and free (and their bucketed
	// forms), and the read API computes NO fullness percentage from them --
	// see the comment on filesystem_samples in 0001_init.sql. used and free
	// do not sum to total; the gap is the root reserve, which holds no data
	// and is not allocatable either. A consumer's fullness is
	// used / (used + free), as df's Use%. At the 5m and 1h tiers a percentage
	// would be worse than absent: used_max / (used_max + free_min) composes
	// two different instants and is not the maximum of the true ratio.
	"filesystem": {
		name:  "filesystem",
		table: "filesystem_samples",
		// Both names, because they are for different readers. The label is the
		// identity -- stable, unique per host, what the inventory joins on --
		// while the mountpoint is what an operator recognises, and a fleet
		// page saying "/mnt/ark is 94 % full" beats one saying "ark". It is
		// nullable, so a consumer has to fall back to the label.
		keys: []keySpec{
			{name: "filesystem", expr: "d.label"},
			{name: "mountpoint", expr: "d.mountpoint"},
		},
		join:             "JOIN filesystems d ON d.id = s.fs_id AND d.host_id = s.host_id",
		dimensionColumns: []string{"fs_id"},
		tiers:            rolledUpTiers,
	},
	"smart": {
		name:  "smart",
		table: "smart_attributes",
		keys: []keySpec{
			{name: "device", expr: "d.device"},
			{name: "attr_id", expr: "s.attr_id::text"},
		},
		join:             "JOIN devices d ON d.id = s.device_id AND d.host_id = s.host_id",
		dimensionColumns: []string{"device_id", "attr_id"},
		tiers:            smartTiers,
	},
	"process": {
		name:             "process",
		table:            "process_samples",
		keys:             []keySpec{{name: "name", expr: "s.name"}},
		dimensionColumns: []string{"name"},
		tiers:            processTiers,
	},
}

// familyNames lists the registry in a stable order, for the error message an
// unknown family gets. A caller who mistyped one wants the list, not a bare
// "invalid".
func familyNames() []string {
	names := make([]string, 0, len(families))
	for n := range families {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// lookupFamily resolves a ?family= value.
func lookupFamily(name string) (*family, error) {
	f, ok := families[name]
	if !ok {
		return nil, fmt.Errorf("%w: unknown family %q; valid families are %s",
			ErrInvalid, name, strings.Join(familyNames(), ", "))
	}
	return f, nil
}
