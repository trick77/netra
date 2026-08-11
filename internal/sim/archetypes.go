package sim

import "fmt"

// Fleet returns the simulated machines in a stable order.
//
// The four are chosen for what they DO NOT have as much as for what they do:
// a Pi with no SMART and no swap, a VPS with steal time and no sensors, a
// baremetal box that reports everything, and a 1 vCPU box with no Docker at
// all. A fleet of four identical hosts would leave every NULL path and every
// "capability absent" branch untested.
func Fleet() []*Profile {
	return []*Profile{rpi5(), nvmeVPS(), smartBaremetal(), minimalVPS()}
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
		Processes: []ProcessSpec{
			{Name: "zigbee2mqtt", CPUBase: 3.2, MemBase: 170 * mib, Count: 1},
			{Name: "dockerd", CPUBase: 1.4, MemBase: 92 * mib, Count: 1},
			{Name: "netra-agent", CPUBase: 0.9, MemBase: 32 * mib, Count: 1},
			{Name: "mosquitto", CPUBase: 0.5, MemBase: 18 * mib, Count: 1},
			{Name: "systemd", CPUBase: 0.2, MemBase: 12 * mib, Count: 2},
			{Name: "sshd", CPUBase: 0.1, MemBase: 9 * mib, Count: 3},
		},
		Collectors: []string{
			"cpu", "percpu", "memory", "load", "kernelstat", "procs", "netstat",
			"users", "diskio", "sensors", "network", "addresses", "containers",
			"filesystems", "systemd", "packages", "processes",
		},
		Capabilities: map[string]string{
			"smart":     "no-device-access",
			"processes": "namespaced",
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
		Processes: []ProcessSpec{
			{Name: "postgres", CPUBase: 9.8, MemBase: 1900 * mib, Count: 14},
			{Name: "netra", CPUBase: 3.9, MemBase: 200 * mib, Count: 1},
			{Name: "dockerd", CPUBase: 1.7, MemBase: 118 * mib, Count: 1},
			{Name: "redis-server", CPUBase: 2.0, MemBase: 128 * mib, Count: 1},
			{Name: "traefik", CPUBase: 1.2, MemBase: 60 * mib, Count: 1},
			{Name: "caddy", CPUBase: 0.8, MemBase: 42 * mib, Count: 1},
			{Name: "netra-agent", CPUBase: 0.7, MemBase: 30 * mib, Count: 1},
			{Name: "sshd", CPUBase: 0.1, MemBase: 11 * mib, Count: 4},
		},
		Collectors: []string{
			"cpu", "percpu", "memory", "load", "kernelstat", "procs", "netstat",
			"users", "diskio", "network", "addresses", "containers",
			"filesystems", "systemd", "packages", "processes",
		},
		Capabilities: map[string]string{
			"sensors": "absent",
			"smart":   "no-device-access",
		},
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

		Drives: []DriveSpec{
			{Device: "sda", Model: "ST16000NM000J-2TW103", Serial: "ZR5A1M0K", PowerOnHours: 21400},
			{Device: "sdb", Model: "ST16000NM000J-2TW103", Serial: "ZR5A1N7C", PowerOnHours: 21398},
			// The one that goes bad. Everything else on this host is healthy,
			// so a "which drive is dying" query has exactly one answer.
			{Device: "sdc", Model: "ST16000NM000J-2TW103", Serial: "ZR5A1PQ2", PowerOnHours: 21402, Failing: true},
			{Device: "sdd", Model: "ST16000NM000J-2TW103", Serial: "ZR5A1RB8", PowerOnHours: 21391},
			{Device: "nvme0n1", Model: "SAMSUNG MZQL21T9HCJR-00A07", Serial: "S64HNE0T512345", SSD: true, PowerOnHours: 9120},
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
		Processes: []ProcessSpec{
			{Name: "z_wr_iss", CPUBase: 14.2, MemBase: 64 * mib, Count: 24},
			{Name: "postgres", CPUBase: 12.6, MemBase: 8 * gib, Count: 22},
			{Name: "nfsd", CPUBase: 8.1, MemBase: 32 * mib, Count: 8},
			{Name: "dockerd", CPUBase: 3.2, MemBase: 240 * mib, Count: 1},
			{Name: "arc_prune", CPUBase: 2.4, MemBase: 16 * mib, Count: 4},
			{Name: "smartd", CPUBase: 0.4, MemBase: 14 * mib, Count: 1},
			{Name: "node_exporter", CPUBase: 1.1, MemBase: 48 * mib, Count: 1},
			{Name: "netra-agent", CPUBase: 1.6, MemBase: 46 * mib, Count: 1},
			{Name: "rsyslogd", CPUBase: 0.7, MemBase: 22 * mib, Count: 1},
			{Name: "sshd", CPUBase: 0.2, MemBase: 13 * mib, Count: 6},
		},
		Collectors: []string{
			"cpu", "percpu", "memory", "load", "kernelstat", "procs", "netstat",
			"users", "diskio", "sensors", "mdraid", "network", "addresses",
			"containers", "filesystems", "systemd", "packages", "smart",
			"processes",
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
		SensorSpec{Chip: "nvme", Label: "Composite", Base: 38, Swing: 11},
		SensorSpec{Chip: "acpitz", Label: "temp1", Base: 32, Swing: 6},
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
		Processes: []ProcessSpec{
			{Name: "nginx", CPUBase: 2.4, MemBase: 28 * mib, Count: 2},
			{Name: "netra-agent", CPUBase: 1.2, MemBase: 26 * mib, Count: 1},
			{Name: "busybox", CPUBase: 0.3, MemBase: 4 * mib, Count: 5},
			{Name: "sshd", CPUBase: 0.1, MemBase: 7 * mib, Count: 2},
		},
		Collectors: []string{
			"cpu", "percpu", "memory", "load", "kernelstat", "procs", "netstat",
			"users", "diskio", "network", "addresses", "filesystems", "packages", "processes",
		},
		Capabilities: map[string]string{
			"containers": "no-docker-socket",
			"sensors":    "absent",
			"smart":      "no-device-access",
			"systemd":    "unavailable",
		},
		Packages: packages("x86_64", 48),
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
