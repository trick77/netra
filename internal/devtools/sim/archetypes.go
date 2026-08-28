package sim

import "fmt"

// Fleet returns the simulated machines in a stable order.
//
// They are chosen for what they DO NOT have as much as for what they do: a Pi
// with no SMART and no swap, a VPS with steal time and no sensors, a baremetal
// box that reports everything, and a 1 vCPU box with no Docker at all. A fleet
// of identical hosts would leave every NULL path and every "capability absent"
// branch untested.
//
// The NAS is there for its DATA rather than its hardware: it is the only host
// whose traffic is heavy-tailed, and a chart that averages a burst away looks
// perfectly healthy on the other four. Appended rather than inserted -- the
// tests index this slice.
func Fleet() []*Profile {
	return []*Profile{
		rpi5(),
		nvmeVPS(),
		smartBaremetal(),
		minimalVPS(),
		homeNAS(),
	}
}

// ByName selects profiles by their Name, preserving Fleet's order and
// rejecting an unknown name rather than silently simulating nothing.
func ByName(names []string) ([]*Profile, error) {
	wanted := map[string]bool{}
	for _, n := range names {
		wanted[n] = true
	}

	var out []*Profile
	for _, p := range Fleet() {
		if wanted[p.Name] {
			out = append(out, p)
			delete(wanted, p.Name)
		}
	}
	for n := range wanted {
		return nil, fmt.Errorf("unknown profile %q", n)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no profiles selected")
	}
	return out, nil
}

const (
	gib = 1 << 30
	mib = 1 << 20
)

// rpi5 is a Raspberry Pi 5 on a home connection: arm64, no swap, one thermal
// sensor, an SD card rather than a disk, and no drive that answers SMART.
func rpi5() *Profile {
	return &Profile{
		Name:        "rpi5",
		Hostname:    "sim-rpi5",
		Arch:        "arm64",
		OSName:      "Debian GNU/Linux 12 (bookworm)",
		Kernel:      "6.6.51+rpt-rpi-2712",
		CPUModel:    "Cortex-A76",
		Cores:       4,
		Threads:     4,
		MemoryTotal: 8 * gib,
		HostType:    "sbc",
		Provider:    "self-hosted",
		Facility:    "home",
		Location:    "Zurich, CH",
		PkgFormat:   "dpkg",
		CPUBase:     14,
		MemUsedFrac: 0.42,

		// No swap: a Pi booting from an SD card is normally configured
		// without one, and swap_total must reach the database as NULL.
		SwapTotal: 0,

		Sensors: []SensorSpec{
			{Chip: "cpu_thermal", Label: "temp1", Base: 46, Swing: 14},
			{Chip: "rp1_adc", Label: "temp1", Base: 41, Swing: 6},
		},
		Disks: []DiskSpec{
			{Device: "mmcblk0", ReadBase: 90 * 1024, WriteBase: 240 * 1024, AwaitBase: 6.5},
		},
		Nets: []NetSpec{
			{Iface: "eth0", RxBase: 180 * 1024, TxBase: 95 * 1024},
			{Iface: "wlan0", RxBase: 9 * 1024, TxBase: 4 * 1024},
		},
		Filesystems: []FSSpec{
			{Label: "root", Mountpoint: "/", DeviceID: 64769, Total: 58 * gib, InodesTotal: 3800000, UsedStart: 0.41, UsedEnd: 0.63},
			{Label: "boot-firmware", Mountpoint: "/boot/firmware", DeviceID: 64768, Total: 512 * mib, InodesTotal: 0, UsedStart: 0.15, UsedEnd: 0.16},
		},
		Containers: []ContainerSpec{
			{Key: "netra/agent", Name: "netra-agent", Image: "ghcr.io/trick77/netra-agent:latest", IsAgent: true, MemLimit: 128 * mib, CPUBase: 1.1, MemBase: 38 * mib},
			{Key: "home/mosquitto", Name: "home-mosquitto-1", Image: "eclipse-mosquitto:2", MemLimit: 256 * mib, CPUBase: 0.6, MemBase: 21 * mib},
			{Key: "home/zigbee2mqtt", Name: "home-zigbee2mqtt-1", Image: "koenkk/zigbee2mqtt:latest", MemLimit: 512 * mib, CPUBase: 3.4, MemBase: 174 * mib},
		},
		Units: []string{
			"ssh.service", "systemd-timesyncd.service", "docker.service",
			"cron.service", "dphys-swapfile.service",
		},
		Addresses: []AddressSpec{
			{Iface: "lo", IfIndex: 1, Address: "127.0.0.1", Family: 4, Description: "loopback"},
			{Iface: "eth0", IfIndex: 2, Address: "192.168.1.42", Family: 4, Description: "lan"},
			{Iface: "eth0", IfIndex: 2, Address: "2a02:168:4a00:1::42", Family: 6, Description: "lan"},
			{Iface: "wlan0", IfIndex: 3, Address: "192.168.1.43", Family: 4, Description: "wifi"},
		},
		Collectors: []string{
			"cpu", "percpu", "memory", "load", "kernelstat", "vmstat", "limits", "procs", "netstat",
			"users", "diskio", "sensors", "network", "addresses", "containers",
			"filesystems", "systemd", "packages",
		},
		Capabilities: map[string]string{
			"smart":     "no-device-access",
			"processes": "namespaced",
			// The agent runs containerised here without the host's network
			// namespace, so per-container rx/tx cannot be measured at all.
			// The container page reads this and says so instead of drawing
			// an empty traffic chart, which would claim these containers
			// moved no bytes.
			"container_network": "no-host-netns",
			"file_descriptors":  "unavailable",
		},
		Packages: packages("arm64", 64),
	}
}

// nvmeVPS is a virtual machine on a shared hypervisor: it reports steal time,
// which bare metal never does, and no sensors at all.
func nvmeVPS() *Profile {
	return &Profile{
		Name:        "nvme-vps",
		Hostname:    "sim-vps-nvme",
		Arch:        "amd64",
		OSName:      "Ubuntu 24.04.1 LTS",
		Kernel:      "6.8.0-45-generic",
		CPUModel:    "AMD EPYC 9354P 32-Core Processor",
		Cores:       4,
		Threads:     4,
		MemoryTotal: 8 * gib,
		HostType:    "vps",
		Provider:    "Hetzner",
		Facility:    "FSN1-DC14",
		Location:    "Falkenstein, DE",
		PkgFormat:   "dpkg",
		CPUBase:     31,
		MemUsedFrac: 0.61,

		SwapTotal: 2 * gib,
		// Non-zero on purpose: steal is the one CPU field that separates a
		// tenant from a landlord, and a fleet without it never shows it.
		StealPct: 1.8,

		Disks: []DiskSpec{
			{Device: "nvme0n1", ReadBase: 1.8 * 1024 * 1024, WriteBase: 3.1 * 1024 * 1024, AwaitBase: 0.35, SolidState: true},
		},
		Nets: []NetSpec{
			{Iface: "eth0", RxBase: 2.4 * 1024 * 1024, TxBase: 5.6 * 1024 * 1024},
		},
		Filesystems: []FSSpec{
			{Label: "root", Mountpoint: "/", DeviceID: 66305, Total: 160 * gib, InodesTotal: 10485760, UsedStart: 0.52, UsedEnd: 0.79},
		},
		Containers: []ContainerSpec{
			{Key: "netra/agent", Name: "netra-agent", Image: "ghcr.io/trick77/netra-agent:latest", IsAgent: true, MemLimit: 128 * mib, CPUBase: 0.8, MemBase: 34 * mib},
			{Key: "netra/hub", Name: "netra-hub-1", Image: "ghcr.io/trick77/netra:latest", MemLimit: 512 * mib, CPUBase: 4.2, MemBase: 210 * mib},
			{Key: "netra/timescaledb", Name: "netra-timescaledb-1", Image: "timescale/timescaledb:latest-pg17", MemLimit: 4 * gib, CPUBase: 11.5, MemBase: 2100 * mib},
			{Key: "netra/traefik", Name: "netra-traefik-1", Image: "traefik:v3.1", MemLimit: 256 * mib, CPUBase: 1.3, MemBase: 62 * mib},
			{Key: "web/caddy", Name: "web-caddy-1", Image: "caddy:2-alpine", MemLimit: 256 * mib, CPUBase: 0.9, MemBase: 44 * mib},
			{Key: "web/redis", Name: "web-redis-1", Image: "redis:7-alpine", MemLimit: 512 * mib, CPUBase: 2.1, MemBase: 130 * mib},
		},
		Units: []string{
			"ssh.service", "docker.service", "systemd-resolved.service",
			"unattended-upgrades.service", "fail2ban.service", "cron.service",
			"nginx.service",
		},
		Addresses: []AddressSpec{
			{Iface: "lo", IfIndex: 1, Address: "127.0.0.1", Family: 4, Description: "loopback"},
			// A routable address, so store.AddressScope classifies at least
			// one row in the fleet as public.
			{Iface: "eth0", IfIndex: 2, Address: "5.75.183.24", Family: 4, Description: "public"},
			{Iface: "eth0", IfIndex: 2, Address: "2a01:4f8:c17:b3::1", Family: 6, Description: "public"},
			{Iface: "docker0", IfIndex: 3, Address: "172.17.0.1", Family: 4, Description: "docker bridge"},
		},
		Collectors: []string{
			"cpu", "percpu", "memory", "load", "kernelstat", "vmstat", "limits", "procs", "netstat",
			"users", "diskio", "network", "addresses", "containers",
			"filesystems", "systemd", "packages",
		},
		Capabilities: map[string]string{
			"sensors": "absent",
			"smart":   "no-device-access",
		},
		FileMax:  524288,
		Packages: packages("amd64", 96),
	}
}

// smartBaremetal is the host that reports everything: 32 threads, five drives
// answering SMART, a full sensor tree, ZFS, and a software RAID array that
// degrades and rebuilds.
func smartBaremetal() *Profile {
	p := &Profile{
		Name:        "smart-baremetal",
		Hostname:    "sim-bare-01",
		Arch:        "amd64",
		OSName:      "Debian GNU/Linux 12 (bookworm)",
		Kernel:      "6.1.0-25-amd64",
		CPUModel:    "Intel(R) Xeon(R) Silver 4314 CPU @ 2.40GHz",
		Cores:       16,
		Threads:     32,
		MemoryTotal: 128 * gib,
		HostType:    "baremetal",
		Provider:    "Init7",
		Facility:    "ZRH2",
		Location:    "Winterthur, CH",
		PkgFormat:   "dpkg",
		CPUBase:     46,
		MemUsedFrac: 0.68,

		SwapTotal: 8 * gib,
		ZFSArc:    24 * gib,
		Mdraid:    "md0",

		// A file-max someone actually set, rather than the int64 max the
		// rest of the fleet is left at. Without one host carrying a real
		// ceiling the descriptor meter is "no limit" everywhere and the
		// bar the Limits card exists to draw is never drawn.
		FileMax: 1048576,

		Drives: []DriveSpec{
			{Device: "sda", Model: "ST16000NM000J-2TW103", Serial: "ZR5A1M0K", PowerOnHours: 21400},
			{Device: "sdb", Model: "ST16000NM000J-2TW103", Serial: "ZR5A1N7C", PowerOnHours: 21398},
			// The one that goes bad. Everything else on this host is healthy,
			// so a "which drive is dying" query has exactly one answer.
			{Device: "sdc", Model: "ST16000NM000J-2TW103", Serial: "ZR5A1PQ2", PowerOnHours: 21402, Failing: true},
			{Device: "sdd", Model: "ST16000NM000J-2TW103", Serial: "ZR5A1RB8", PowerOnHours: 21391},
			// NVMe, so the fleet exercises both attribute id spaces. ATA
			// numbers 1-255 out of the drive's own table; NVMe has no ids at
			// all, so the collector maps its health log onto synthetic ones
			// from 1000 up, and the two are read by different code. This drive
			// was already named nvme0n1 while reporting ATA attributes, which
			// no real NVMe device does.
			//
			// On this host rather than the VPS because that profile declares
			// smart: no-device-access and runs no smart collector -- a
			// hypervisor does not pass SMART through, which is the realistic
			// state and worth keeping.
			{Device: "nvme0n1", Model: "SAMSUNG MZQL21T9HCJR-00A07", Serial: "S64HNE0T512345", SSD: true, NVMe: true, PowerOnHours: 9120},
		},
		Disks: []DiskSpec{
			{Device: "sda", ReadBase: 4.2 * 1024 * 1024, WriteBase: 6.8 * 1024 * 1024, AwaitBase: 9.4},
			{Device: "sdb", ReadBase: 4.1 * 1024 * 1024, WriteBase: 6.9 * 1024 * 1024, AwaitBase: 9.6},
			{Device: "sdc", ReadBase: 4.3 * 1024 * 1024, WriteBase: 6.7 * 1024 * 1024, AwaitBase: 11.2},
			{Device: "sdd", ReadBase: 4.0 * 1024 * 1024, WriteBase: 6.6 * 1024 * 1024, AwaitBase: 9.1},
			{Device: "nvme0n1", ReadBase: 22 * 1024 * 1024, WriteBase: 31 * 1024 * 1024, AwaitBase: 0.22, SolidState: true},
		},
		Nets: []NetSpec{
			{Iface: "bond0", RxBase: 41 * 1024 * 1024, TxBase: 28 * 1024 * 1024},
			{Iface: "enp1s0f0", RxBase: 21 * 1024 * 1024, TxBase: 14 * 1024 * 1024},
			{Iface: "enp1s0f1", RxBase: 20 * 1024 * 1024, TxBase: 14 * 1024 * 1024},
		},
		Filesystems: []FSSpec{
			{Label: "root", Mountpoint: "/", DeviceID: 66306, Total: 440 * gib, InodesTotal: 29360128, UsedStart: 0.31, UsedEnd: 0.44},
			{Label: "boot", Mountpoint: "/boot", DeviceID: 66305, Total: 1 * gib, InodesTotal: 65536, UsedStart: 0.28, UsedEnd: 0.31},
			{Label: "tank", Mountpoint: "/tank", DeviceID: 43, Total: 44000 * gib, InodesTotal: 0, UsedStart: 0.58, UsedEnd: 0.71},
			{Label: "tank-backups", Mountpoint: "/tank/backups", DeviceID: 44, Total: 44000 * gib, InodesTotal: 0, UsedStart: 0.60, UsedEnd: 0.74},
			// The one that fills up: 62% to 91% over the window.
			{Label: "var-log", Mountpoint: "/var/log", DeviceID: 66307, Total: 32 * gib, InodesTotal: 2097152, UsedStart: 0.62, UsedEnd: 0.91},
			// The one that is high but not short: 90% of 6.8 TB still leaves
			// 680 GB, which is nothing to do. Here so the fleet page can be
			// seen staying quiet about it while /var/log above, at the same
			// percentage and 2.9 GB left, is still called out.
			{Label: "ark", Mountpoint: "/mnt/ark", DeviceID: 45, Total: 6800 * gib, InodesTotal: 0, UsedStart: 0.88, UsedEnd: 0.90},
			{Label: "nvme-scratch", Mountpoint: "/scratch", DeviceID: 66560, Total: 1800 * gib, InodesTotal: 117440512, UsedStart: 0.12, UsedEnd: 0.37},
		},
		Units: []string{
			"ssh.service", "docker.service", "zfs-zed.service", "smartd.service",
			"mdmonitor.service", "systemd-journald.service", "chrony.service",
			"nfs-server.service", "prometheus-node-exporter.service",
			"unattended-upgrades.service", "cron.service", "rsyslog.service",
		},
		Addresses: []AddressSpec{
			{Iface: "lo", IfIndex: 1, Address: "127.0.0.1", Family: 4, Description: "loopback"},
			{Iface: "lo", IfIndex: 1, Address: "::1", Family: 6, Description: "loopback"},
			{Iface: "bond0", IfIndex: 4, Address: "77.109.139.14", Family: 4, Description: "uplink"},
			{Iface: "bond0", IfIndex: 4, Address: "2001:1620:2ff:1::14", Family: 6, Description: "uplink"},
			{Iface: "bond0", IfIndex: 4, Address: "10.20.0.1", Family: 4, Description: "storage vlan"},
			{Iface: "docker0", IfIndex: 7, Address: "172.17.0.1", Family: 4, Description: "docker bridge"},
		},
		Collectors: []string{
			"cpu", "percpu", "memory", "load", "kernelstat", "vmstat", "limits", "procs", "netstat",
			"users", "diskio", "sensors", "mdraid", "network", "addresses",
			"containers", "filesystems", "systemd", "packages", "smart",
		},
		Capabilities: map[string]string{},
		Packages:     packages("amd64", 140),
	}

	// One package-manager container per service, plus a per-core sensor tree.
	for i := range 12 {
		p.Containers = append(p.Containers, ContainerSpec{
			Key:      fmt.Sprintf("lab/worker%02d", i+1),
			Name:     fmt.Sprintf("lab-worker%02d-1", i+1),
			Image:    "ghcr.io/trick77/lab-worker:2.4",
			MemLimit: 2 * gib,
			CPUBase:  3.5 + float64(i)*0.4,
			MemBase:  uint64(340+i*37) * mib,
		})
	}
	p.Containers = append([]ContainerSpec{{
		Key: "netra/agent", Name: "netra-agent", Image: "ghcr.io/trick77/netra-agent:latest",
		IsAgent: true, MemLimit: 128 * mib, CPUBase: 1.5, MemBase: 44 * mib,
	}}, p.Containers...)

	p.Sensors = append(p.Sensors,
		SensorSpec{Chip: "coretemp", Label: "Package id 0", Base: 44, Swing: 22},
		SensorSpec{Chip: "nvme", Label: "Composite", Instance: "nvme0n1", Base: 38, Swing: 11},
		SensorSpec{Chip: "acpitz", Label: "temp1", Base: 32, Swing: 6},

		// The four SATA disks, as the drivetemp module reports them: one
		// chip per drive, every one of them named "drivetemp", none of them
		// carrying a temp1_label. This is the shape that has no unique
		// identity without the instance -- four chips indistinguishable by
		// chip and label alone -- and no archetype had it, so the collapse
		// real multi-disk hosts were suffering could not be seen locally.
		//
		// sdc runs warm, matching the drive the SMART profile above fails:
		// a disk that is reallocating sectors and running eight degrees
		// hotter than its three neighbours is one story told twice, which
		// is what makes the temperature worth charting beside the table.
		SensorSpec{Chip: "drivetemp", Label: "temp1", Instance: "sda", Base: 34, Swing: 7},
		SensorSpec{Chip: "drivetemp", Label: "temp1", Instance: "sdb", Base: 33, Swing: 7},
		SensorSpec{Chip: "drivetemp", Label: "temp1", Instance: "sdc", Base: 42, Swing: 8},
		SensorSpec{Chip: "drivetemp", Label: "temp1", Instance: "sdd", Base: 35, Swing: 6},

		// Bare metal is the only place these exist, and they are the two
		// hardware failures a temperature cannot show: a stopped fan and a
		// sagging rail. Base and Swing are in each kind's own unit.
		//
		// fan2 stalls. It is the case the whole value_min path is for --
		// averaged over a five-minute bucket a brief stall still reads as a
		// healthy fan, so a simulator that never stalls one cannot show
		// whether the UI would catch a real one.
		SensorSpec{Chip: "nct6775", Label: "fan1", Kind: "fan", Base: 1180, Swing: 900},
		SensorSpec{Chip: "nct6775", Label: "fan2", Kind: "fan", Base: 1240, Swing: 860, Stalls: true},
		SensorSpec{Chip: "nct6775", Label: "fan3", Kind: "fan", Base: 980, Swing: 640},
		SensorSpec{Chip: "nct6775", Label: "+12V", Kind: "voltage", Base: 12.06, Swing: 0.22},
		SensorSpec{Chip: "nct6775", Label: "+5V", Kind: "voltage", Base: 5.02, Swing: 0.08},
		SensorSpec{Chip: "nct6775", Label: "Vcore", Kind: "voltage", Base: 1.19, Swing: 0.06},
		SensorSpec{Chip: "power_meter", Label: "input", Kind: "power", Base: 78, Swing: 145},
		SensorSpec{Chip: "power_meter", Label: "rail", Kind: "current", Base: 6.4, Swing: 11.5},
	)
	for i := range 16 {
		p.Sensors = append(p.Sensors, SensorSpec{
			Chip:  "coretemp",
			Label: fmt.Sprintf("Core %d", i),
			Base:  42 + float64(i%4),
			Swing: 20,
		})
	}
	return p
}

// minimalVPS is the small box: one vCPU, a gigabyte of RAM, Alpine, and no
// Docker at all. It exists so the fleet contains a host whose per-entity
// tables are nearly empty, which is where "no rows" and "no host" get
// confused.
func minimalVPS() *Profile {
	return &Profile{
		Name:        "minimal-vps",
		Hostname:    "sim-vps-tiny",
		Arch:        "amd64",
		OSName:      "Alpine Linux v3.20",
		Kernel:      "6.6.49-0-lts",
		CPUModel:    "Intel Xeon Processor (Skylake, IBRS)",
		Cores:       1,
		Threads:     1,
		MemoryTotal: 1 * gib,
		HostType:    "vps",
		Provider:    "Vultr",
		Facility:    "AMS",
		Location:    "Amsterdam, NL",
		PkgFormat:   "apk",
		CPUBase:     9,
		MemUsedFrac: 0.55,

		SwapTotal: 512 * mib,
		StealPct:  4.3,

		Disks: []DiskSpec{
			{Device: "vda", ReadBase: 140 * 1024, WriteBase: 260 * 1024, AwaitBase: 1.4, SolidState: true},
		},
		Nets: []NetSpec{
			{Iface: "eth0", RxBase: 320 * 1024, TxBase: 180 * 1024},
		},
		Filesystems: []FSSpec{
			{Label: "root", Mountpoint: "/", DeviceID: 65024, Total: 25 * gib, InodesTotal: 1638400, UsedStart: 0.34, UsedEnd: 0.48},
		},
		// No units at all: Alpine runs OpenRC, which is exactly why this host
		// reports systemd as unavailable. Listing units here and then
		// declaring the collector unavailable would have the host emitting
		// unit events it cannot possibly observe.
		Units: nil,
		Addresses: []AddressSpec{
			{Iface: "lo", IfIndex: 1, Address: "127.0.0.1", Family: 4, Description: "loopback"},
			{Iface: "eth0", IfIndex: 2, Address: "95.179.212.88", Family: 4, Description: "public"},
		},
		Collectors: []string{
			"cpu", "percpu", "memory", "load", "kernelstat", "vmstat", "limits", "procs", "netstat",
			"users", "diskio", "network", "addresses", "filesystems", "packages",
		},
		Capabilities: map[string]string{
			"containers": "no-docker-socket",
			"sensors":    "absent",
			"smart":      "no-device-access",
			"systemd":    "unavailable",
			// A locked-down container host: /proc/net/nf_conntrack is not
			// readable and the conntrack module is not loaded, so the
			// conntrack meter has no reading to show. The capability is the
			// answer next to it -- without one the empty meter is
			// indistinguishable from a broken collector.
			"conntrack": "unavailable",
		},
		Packages: packages("x86_64", 48),
	}
}

// homeNAS is the fleet's HEAVY-TAILED host: a box that does nothing all day
// and then moves a backup.
//
// Every other archetype's traffic is a gentle daily curve that occasionally
// triples, which draws as a smooth hump at every range and cannot show the
// difference between a chart that keeps a burst and one that averages it
// away. Both look correct. This host is the one that tells them apart: 26
// kB/s of idle chatter against 90 MB/s bursts, the ~3500:1 range measured on
// a real box when the traffic axis was first argued about.
//
// The rest of it is deliberately unremarkable -- one filesystem that matters,
// no sensors, no SMART -- because the interesting thing here is the traffic
// and a second baremetal box's worth of hardware detail would only slow the
// simulator down.
func homeNAS() *Profile {
	return &Profile{
		Name:        "home-nas",
		Hostname:    "sim-nas",
		Arch:        "amd64",
		OSName:      "Debian GNU/Linux 12 (bookworm)",
		Kernel:      "6.1.0-23-amd64",
		CPUModel:    "Intel Celeron J4125",
		Cores:       4,
		Threads:     4,
		MemoryTotal: 8 * gib,
		HostType:    "physical",
		Provider:    "self-hosted",
		Facility:    "basement",
		Location:    "Zurich, CH",
		PkgFormat:   "dpkg",
		CPUBase:     6,
		MemUsedFrac: 0.41,

		SwapTotal: 2 * gib,

		Disks: []DiskSpec{
			{Device: "sda", ReadBase: 90 * 1024, WriteBase: 1400 * 1024, AwaitBase: 8.2},
		},
		Nets: []NetSpec{
			// 26 kB/s idle, 90 MB/s when a backup runs. The magnitude is what
			// makes this host worth simulating; the chance is roughly one
			// burst per hour at a 60 s scrape.
			{
				Iface:          "enp2s0",
				RxBase:         26 * 1024,
				TxBase:         31 * 1024,
				BurstChance:    0.017,
				BurstMagnitude: 3500,
			},
		},
		Filesystems: []FSSpec{
			{Label: "root", Mountpoint: "/", DeviceID: 2049, Total: 60 * gib, InodesTotal: 3932160, UsedStart: 0.29, UsedEnd: 0.31},
			{Label: "tank", Mountpoint: "/srv/tank", DeviceID: 2050, Total: 8000 * gib, InodesTotal: 488378368, UsedStart: 0.61, UsedEnd: 0.68},
		},
		Addresses: []AddressSpec{
			{Iface: "lo", IfIndex: 1, Address: "127.0.0.1", Family: 4, Description: "loopback"},
			{Iface: "enp2s0", IfIndex: 2, Address: "192.168.1.10", Family: 4, Description: "lan"},
		},
		Collectors: []string{
			"cpu", "percpu", "memory", "load", "kernelstat", "vmstat", "limits", "procs", "netstat",
			"users", "diskio", "network", "addresses", "filesystems", "packages", "units",
		},
		Capabilities: map[string]string{
			"containers": "no-docker-socket",
			"sensors":    "absent",
			"smart":      "no-device-access",
		},
		Packages: packages("amd64", 61),
	}
}

// packageNames is the pool the inventory is drawn from. Real enough that a
// package list looks like one, short enough to stay readable.
var packageNames = []string{
	"bash", "coreutils", "curl", "openssh-server", "ca-certificates", "tzdata",
	"libc6", "libssl3", "zlib1g", "systemd", "udev", "dbus", "iproute2",
	"iptables", "nftables", "grep", "sed", "gawk", "tar", "gzip", "xz-utils",
	"less", "vim-tiny", "nano", "htop", "rsync", "cron", "logrotate",
	"ca-certificates-java", "python3", "python3-minimal", "perl-base",
	"git", "jq", "wget", "dnsutils", "netcat-openbsd", "lsof", "strace",
	"smartmontools", "mdadm", "lvm2", "e2fsprogs", "xfsprogs", "zfsutils-linux",
	"docker-ce", "containerd.io", "docker-compose-plugin", "chrony", "fail2ban",
	"unattended-upgrades", "apt", "dpkg", "gnupg", "sudo", "passwd", "login",
	"libpam-modules", "libselinux1", "libcap2", "libgcc-s1", "libstdc++6",
	"ncurses-base", "ncurses-bin", "readline-common", "libtinfo6", "findutils",
	"diffutils", "hostname", "init-system-helpers", "mount", "util-linux",
	"procps", "psmisc", "traceroute", "ethtool", "pciutils", "usbutils",
	"lm-sensors", "nvme-cli", "hdparm", "sysstat", "iotop", "iftop", "tcpdump",
	"nginx", "nginx-common", "redis-tools", "postgresql-client-16", "socat",
	"ssl-cert", "man-db", "bash-completion", "file", "bzip2", "zstd", "cpio",
	"debianutils", "base-files", "libzstd1", "liblz4-1", "libudev1", "libblkid1",
	"libmount1", "libuuid1", "libsystemd0", "libseccomp2", "libffi8", "libidn2-0",
	"libunistring2", "libnettle8", "libhogweed6", "libgmp10", "libtasn1-6",
	"libp11-kit0", "libgnutls30", "libkrb5-3", "libk5crypto3", "libcom-err2",
	"libkeyutils1", "libsasl2-2", "libldap-2.5-0", "libnghttp2-14", "librtmp1",
	"libssh2-1", "libpsl5", "libbrotli1", "libcurl4", "libexpat1", "libpython3.11",
	"mailcap", "mime-support", "netbase", "openssl", "publicsuffix", "xdg-user-dirs",
	"whiptail", "libnewt0.52", "libslang2", "libpopt0", "lsb-release", "distro-info-data",
}

// packages renders the first n names as an inventory. Versions are derived
// from the index so a re-run produces the identical list -- the hub upserts
// on (host_id, name, arch), and a version that changed every run would look
// like an upgrade on every scrape.
func packages(arch string, n int) []PackageSpec {
	if n > len(packageNames) {
		n = len(packageNames)
	}
	out := make([]PackageSpec, 0, n)
	for i, name := range packageNames[:n] {
		out = append(out, PackageSpec{
			Name:    name,
			Version: fmt.Sprintf("%d.%d.%d-%d", 1+i%4, i%13, i%7, 1+i%3),
			Arch:    arch,
			Size:    uint64(48*1024 + i*7919),
		})
	}
	return out
}
