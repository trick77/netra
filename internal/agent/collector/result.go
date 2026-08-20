package collector

import netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"

// Result is one collector's contribution to a scrape.
//
// A collector fills only what it owns and leaves everything else nil. The
// agent merges the results of the collectors that succeeded and discards the
// rest whole, so a collector that fails part-way through contributes nothing
// rather than leaving half its fields behind for the hub to store as though
// they had been measured.
//
// Returning a value rather than writing into a shared sample also keeps
// collectors independent of one another: two of them cannot interleave writes
// into the same object, and a test needs no shared fixture.
type Result struct {
	// Host is this collector's share of the wide per-host row. Fields it does
	// not own stay unset, and the agent merges only the fields that are set.
	Host *netrav1.HostSample

	// One slice per per-entity family. A collector fills only the families it
	// owns; the agent concatenates them across collectors in registration
	// order.
	Cores       []*netrav1.CpuCoreSample
	Disks       []*netrav1.DiskIoSample
	Sensors     []*netrav1.SensorSample
	Nets        []*netrav1.NetSample
	Containers  []*netrav1.ContainerSample
	Filesystems []*netrav1.FilesystemSample
	Smart       []*netrav1.SmartAttribute
	Events      []*netrav1.Event

	// SystemdEvents and PackageEvents are separate from Events because both
	// carry structured columns that would otherwise be buried in the generic
	// event's detail JSON (spec 5.2).
	SystemdEvents []*netrav1.SystemdUnitEvent
	PackageEvents []*netrav1.PackageEvent

	// SystemdSnapshot is the level-triggered counterpart to SystemdEvents: the
	// full unit set, sent periodically so a divergence between the agent's
	// in-memory prev map and the hub's stored state cannot become permanent.
	//
	// A pointer rather than a slice, unlike every family above, because it is
	// one document describing a whole scrape rather than a set of independent
	// rows -- the `complete` flag it carries is meaningless split apart, and
	// nil must stay distinguishable from empty (see SystemdSnapshot.complete).
	SystemdSnapshot *netrav1.SystemdSnapshot

	// Inventory: what the host HAS, rather than what it measured. Reported on
	// change rather than every scrape.
	Addresses []*netrav1.HostAddress
	Packages  []*netrav1.HostPackage
}
