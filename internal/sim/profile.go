// Package sim generates a fake fleet of netra agents.
//
// It exists because the only way to get data into a netra database is a real
// agent on a real Linux host, which makes developing anything that READS the
// data -- the read API, the UI, dashboard queries, retention behaviour -- a
// chicken-and-egg problem, and makes the interesting cases (a Pi with no
// SMART, a VPS with steal time, a baremetal box with a dying drive)
// impossible to reproduce on a laptop.
//
// It is a development tool. It speaks the real ingest protocol over HTTP
// rather than writing SQL, so a run exercises the natural-key resolution, the
// poison-row quarantine and the hub-side address classification that a direct
// seeder would skip entirely -- the parts most likely to be wrong.
package sim

import netrav1 "github.com/trick77/netra/internal/gen/netra/v1"

// HostnamePrefix is the contract every simulated hostname keeps, and the only
// thing standing between --fresh and someone's real fleet.
//
// --hub defaults to localhost but is a flag, and a developer will eventually
// point it at a real hub. DeleteHost cascades everything a host ever
// reported, and EnsureHost rotates the token of any host whose name matches --
// which would take a real agent offline. Both are therefore refused for any
// hostname that does not start with this, so the worst a misdirected run can
// do to a production hub is add four obviously-named hosts.
const HostnamePrefix = "sim-"

// Profile is one simulated machine: its static facts, and which families it
// reports. A field left empty means the subsystem is absent, and absent is
// the point -- a fleet where every host reports everything hides every bug
// that only shows up when a column is NULL.
type Profile struct {
	// Name is the --hosts selector, and Hostname is what the hub registers.
	Name     string
	Hostname string

	Arch        string
	OSName      string
	Kernel      string
	CPUModel    string
	Cores       int
	Threads     int
	MemoryTotal uint64
	HostType    string

	Provider string
	Facility string
	Location string

	// SwapTotal of 0 means the host has no swap, and swap_total/swap_used
	// then reach the database as NULL rather than as a fabricated zero.
	SwapTotal uint64
	// ZFSArc is set only on a host running ZFS; elsewhere mem_zfs_arc is NULL.
	ZFSArc uint64
	// StealPct is the baseline CPU steal. Zero on bare metal, where the
	// column is left unset rather than reported as 0.
	StealPct float64

	// CPUBase is the host's mean CPU utilisation in percent and MemUsedFrac
	// the mean fraction of RAM in use. Everything else that should move with
	// load -- per-core busy, load average, sensor temperature, container CPU
	// -- is derived from these, so a busy host is busy consistently rather
	// than reporting a hot CPU next to an idle load average.
	CPUBase     float64
	MemUsedFrac float64

	// PkgFormat is "dpkg" or "apk".
	PkgFormat string

	Sensors     []SensorSpec
	Disks       []DiskSpec
	Drives      []DriveSpec
	Nets        []NetSpec
	Filesystems []FSSpec
	Containers  []ContainerSpec
	Units       []string
	Packages    []PackageSpec
	Addresses   []AddressSpec
	Processes   []ProcessSpec

	// Collectors names the collectors whose health this host reports. See the
	// note on collector_samples in build.go: filling this is a deliberate
	// divergence from the real agent.
	Collectors []string

	// Capabilities is what each collector reported about its own
	// availability. It is the only way to tell "no SMART data because no
	// drive supports it" from "no SMART data because nothing ran".
	Capabilities map[string]string

	// Mdraid names the array this host reports degrade/rebuild events for.
	// Empty on a host with no software RAID.
	Mdraid string
}

// SensorSpec is one hwmon reading. Base is the idle temperature in degrees
// Celsius and Swing how far it moves over a day under load.
type SensorSpec struct {
	Chip  string
	Label string
	Base  float64
	Swing float64
}

// DiskSpec is one block device's I/O baseline, in bytes per second.
type DiskSpec struct {
	Device     string
	ReadBase   float64
	WriteBase  float64
	AwaitBase  float64
	SolidState bool
}

// DriveSpec is one drive that answers SMART. SSD selects the attribute set: a
// spinning disk reports reallocated sectors and power-on hours, an SSD
// reports wear levelling instead.
type DriveSpec struct {
	Device string
	Model  string
	Serial string
	SSD    bool
	// Failing marks the drive whose reallocated-sector count steps up
	// partway through the window, so "find me a drive that is dying" has
	// something to find.
	Failing bool
	// PowerOnHours at the start of the simulated window.
	PowerOnHours int64
}

// NetSpec is one interface's traffic baseline, in bytes per second.
type NetSpec struct {
	Iface  string
	RxBase float64
	TxBase float64
}

// FSSpec is one filesystem. UsedStart and UsedEnd are the fraction full at
// the beginning and end of the simulated window, so a filesystem can be shown
// filling up over months rather than sitting at a constant.
type FSSpec struct {
	Label       string
	Mountpoint  string
	DeviceID    uint64
	Total       uint64
	InodesTotal uint64
	UsedStart   float64
	UsedEnd     float64
}

// ContainerSpec is one container. Key is the compose project+service, never
// the Docker id -- an id changes on every recreate, which would restart the
// history of a service that merely got a new image.
type ContainerSpec struct {
	Key      string
	Name     string
	Image    string
	IsAgent  bool
	MemLimit uint64
	CPUBase  float64
	MemBase  uint64
}

// PackageSpec is one installed package.
type PackageSpec struct {
	Name    string
	Version string
	Arch    string
	Size    uint64
}

// AddressSpec is one address on one interface. Both private and public
// addresses appear across the fleet on purpose: host_addresses.scope is
// derived hub-side by store.AddressScope, and a fleet of RFC1918 addresses
// would never exercise the other branch.
type AddressSpec struct {
	Iface       string
	IfIndex     uint32
	Address     string
	Family      uint32
	Description string
}

// ProcessSpec is one process name, aggregated across every process sharing
// it. Names are comm-style and truncated to 15 bytes like the kernel's,
// because that is what the real collector can see -- argv is never collected
// anywhere in netra and must not appear here either.
type ProcessSpec struct {
	Name    string
	CPUBase float64
	MemBase uint64
	Count   uint32
}

// Metadata renders the profile's static facts as the block the hub stores on
// the hosts row. The fingerprint is synthetic but stable per profile, so a
// re-run does not look like the token was copied to a different machine.
func (p *Profile) Metadata(agentVersion, goVersion, commit string) *netrav1.Metadata {
	return &netrav1.Metadata{
		AgentVersion: agentVersion,
		GoVersion:    goVersion,
		BuildCommit:  commit,
		Hostname:     p.Hostname,
		Kernel:       p.Kernel,
		OsName:       p.OSName,
		Arch:         p.Arch,
		CpuModel:     p.CPUModel,
		Cores:        uint32(p.Cores),
		Threads:      uint32(p.Threads),
		MemoryTotal:  p.MemoryTotal,
		Location:     p.Location,
		Provider:     p.Provider,
		Facility:     p.Facility,
		HostType:     p.HostType,
		Fingerprint:  fingerprint(p.Hostname),
		Capabilities: p.Capabilities,
	}
}
