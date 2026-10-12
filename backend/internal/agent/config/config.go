// Package config turns AGENT_* environment variables into an agent Config.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"
)

// MaxBufferWindow is the hub's continuous-aggregate start_offset for the 5m
// tier (internal/hub/store/migrations/0001_init.sql). Data buffered
// longer than this and then replayed lands in a chunk TimescaleDB no longer
// re-materialises, so it would be silently excluded from rollups forever.
const MaxBufferWindow = 6 * time.Hour

// ScrapeInterval is how often the agent collects and ships a sample. It is
// deliberately NOT configurable.
//
// The hub has no per-host cadence column: it could only ever hand every agent
// the same hardcoded constant back, which silently overrode whatever an
// operator had set locally on the first successful flush. A knob the hub can
// override behind your back is worse than no knob at all, so there is no knob.
// A per-host cadence override was considered and dropped: nothing should
// reintroduce a hosts.interval_s column or a wire field to carry one.
const ScrapeInterval = 60 * time.Second

// Config holds every agent setting. Only HubURL and Token are required.
type Config struct {
	HubURL       string
	Token        string
	BufferWindow time.Duration
	ProcRoot     string
	SysRoot      string
	Location     string
	Provider     string
	Facility     string
	HostType     string
	LogLevel     string

	// UtmpPath is the file the logged-in session count is read from. It is
	// configurable so a host that keeps utmp somewhere other than
	// /var/run/utmp, or a test using a fixture, can point at it.
	UtmpPath string

	// FsMounts maps a marker label to the host mountpoint it stands for --
	// {"root": "/", "ark": "/mnt/ark"}. Rendered by setup-agent.sh from the
	// same FS_MOUNTS list that produced the bind mounts, so the two cannot
	// disagree.
	//
	// Without it the filesystem collector can only report the label, and the
	// container-side path it is attached to (/netra/fs/ark) names nothing on
	// the host at all.
	FsMounts map[string]string

	// PidHost records whether the container was started with pid: host.
	//
	// The process collector can only guess otherwise, and every guess has a
	// case it gets wrong. setup-agent.sh already knows what it rendered, so it
	// writes the answer here and the guessing is skipped entirely.
	PidHost bool

	// CgroupRoot is the mounted cgroup v2 hierarchy the container collector
	// reads. cgroup v1 is a permanent non-goal (spec §1).
	//
	// The default is the HOST's hierarchy bind-mounted to /host/sys/fs/cgroup,
	// not the container's own /sys/fs/cgroup, and that is deliberate twice
	// over. Docker's default cgroup namespace is private, so a container's
	// own /sys/fs/cgroup is rooted at its own cgroup and contains no sibling
	// container scopes at all -- the collector walked it, found nothing, and
	// reported no error, because an empty walk is not a failure. Pointing the
	// default at a path that only EXISTS when the mount was granted turns that
	// silence into a logged collector error instead.
	CgroupRoot string

	// OsRelease is the host's os-release file, read once at startup for the
	// distro name the UI puts a mark beside.
	//
	// The default is the HOST's file bind-mounted to /host/etc/os-release,
	// and it is NOT /etc/os-release for the same reason CgroupRoot is not
	// /sys/fs/cgroup, only worse: this image is Alpine and HAS an
	// /etc/os-release of its own. A missing mount would not read nothing, it
	// would read "Alpine Linux" and report it for every host in the fleet --
	// a wrong answer with nothing to distinguish it from a right one. A path
	// that exists only when the mount was granted degrades to the GOOS
	// fallback instead, which is what the UI's generic Tux already means.
	OsRelease string

	// DpkgStatus and ApkInstalled are the package databases. Whichever exists
	// decides the format; a host with neither reports an unsupported-format
	// capability rather than failing.
	DpkgStatus   string
	ApkInstalled string

	// SensorsTimeout bounds a single hwmon read. A wedged driver blocks
	// read(2) indefinitely, and without a deadline that stalls the whole
	// scrape loop -- every other collector included.
	SensorsTimeout time.Duration

	// SmartInterval is how often smartctl runs. Long on purpose: SMART values
	// change slowly and reading them spins up sleeping drives.
	SmartInterval time.Duration
}

// Load reads the environment and applies defaults.
func Load() (Config, error) {
	if err := rejectOldPrefix(); err != nil {
		return Config{}, err
	}

	cfg := Config{
		HubURL:   os.Getenv("AGENT_HUB_URL"),
		Token:    os.Getenv("AGENT_TOKEN"),
		ProcRoot: envOr("AGENT_PROC_ROOT", "/proc"),
		SysRoot:  envOr("AGENT_SYSFS_ROOT", "/sys"),
		Location: os.Getenv("AGENT_LOCATION"),
		Provider: os.Getenv("AGENT_PROVIDER"),
		Facility: os.Getenv("AGENT_FACILITY"),
		HostType: os.Getenv("AGENT_HOST_TYPE"),
		LogLevel: envOr("AGENT_LOG_LEVEL", "info"),
		UtmpPath: envOr("AGENT_UTMP_PATH", "/var/run/utmp"),
		PidHost:  boolEnv("AGENT_PID_HOST"),
		FsMounts: fsMounts(os.Getenv("AGENT_FS_MOUNTS")),

		CgroupRoot:   envOr("AGENT_CGROUP_ROOT", "/host/sys/fs/cgroup"),
		OsRelease:    envOr("AGENT_OS_RELEASE", "/host/etc/os-release"),
		DpkgStatus:   envOr("AGENT_DPKG_STATUS", "/var/lib/dpkg/status"),
		ApkInstalled: envOr("AGENT_APK_INSTALLED", "/lib/apk/db/installed"),
	}

	if cfg.HubURL == "" {
		return Config{}, fmt.Errorf("AGENT_HUB_URL is required")
	}
	if cfg.Token == "" {
		return Config{}, fmt.Errorf("AGENT_TOKEN is required")
	}

	var err error

	// Two seconds is generous for a sysfs read that normally takes
	// microseconds, and short enough that a wedged driver costs one scrape
	// rather than the whole loop.
	if cfg.SensorsTimeout, err = durationOr("AGENT_SENSORS_TIMEOUT", 2*time.Second); err != nil {
		return Config{}, err
	}

	// One hour: SMART values move slowly, and reading them wakes sleeping
	// drives.
	if cfg.SmartInterval, err = durationOr("AGENT_SMART_INTERVAL", time.Hour); err != nil {
		return Config{}, err
	}

	// The buffer window is coupled to the hub's continuous-aggregate
	// start_offset (MaxBufferWindow, 6h). Raising it past that would silently
	// exclude replayed data from rollups forever, so it is rejected here.
	if cfg.BufferWindow, err = durationOr("AGENT_BUFFER_WINDOW", time.Hour); err != nil {
		return Config{}, err
	}
	if cfg.BufferWindow > MaxBufferWindow {
		return Config{}, fmt.Errorf(
			"AGENT_BUFFER_WINDOW must not exceed %s (the hub's continuous-aggregate "+
				"start_offset); data buffered longer than that and then replayed would be "+
				"silently excluded from rollups forever, got %s",
			MaxBufferWindow, cfg.BufferWindow)
	}

	return cfg, nil
}

// rejectOldPrefix refuses to start while a NETRA_-prefixed variable is still
// set. Every agent variable was renamed to AGENT_ in one go, and the swap is
// mechanical, so the message can name the replacement.
//
// Loud rather than ignored, and it matters more here than on the hub: an agent
// .env lives on a host nobody logs into, and an upgrade that started reading
// nothing would leave AGENT_PID_HOST unset -- not a crash, just a fleet whose
// process counts quietly went back to being guessed.
func rejectOldPrefix() error {
	for _, kv := range os.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		if old, found := strings.CutPrefix(key, "NETRA_"); found {
			return fmt.Errorf("%s is no longer read; rename it to AGENT_%s", key, old)
		}
	}
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// boolEnv reads a flag that is written by setup-agent.sh as 1 or 0. Anything
// unrecognised is false: this drives whether a heuristic is skipped, and
// defaulting an unparseable value to "trust it" would be the wrong direction.
func boolEnv(key string) bool {
	switch os.Getenv(key) {
	case "1", "true", "TRUE", "yes", "on":
		return true
	default:
		return false
	}
}

// fsMounts parses `label=mountpoint,label=mountpoint` as rendered by
// setup-agent.sh.
//
// A malformed entry is skipped, never fatal. The worst case it causes is a
// filesystem reported under its label instead of its mountpoint; an agent that
// refuses to start over it would cost the operator every other metric on the
// host as well.
//
// A mountpoint may legitimately contain the separators, so setup-agent.sh
// percent-encodes , and = (and % itself) on the way out; this undoes that.
func fsMounts(v string) map[string]string {
	if v == "" {
		return nil
	}
	out := make(map[string]string)
	for _, pair := range strings.Split(v, ",") {
		label, mountpoint, ok := strings.Cut(pair, "=")
		if !ok || label == "" || mountpoint == "" {
			slog.Warn("AGENT_FS_MOUNTS entry is not label=mountpoint, ignoring it",
				"entry", pair)
			continue
		}
		out[label] = pctDecode(mountpoint)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// pctDecode undoes the %2C / %3D / %25 encoding setup-agent.sh applies to a
// mountpoint. An incomplete or non-hex escape is left as written rather than
// dropped: it came from a real path, and mangling it further helps nobody.
func pctDecode(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			if hi, ok := hexVal(s[i+1]); ok {
				if lo, ok2 := hexVal(s[i+2]); ok2 {
					b.WriteByte(hi<<4 | lo)
					i += 2
					continue
				}
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func hexVal(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

func durationOr(key string, fallback time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s must be positive, got %s", key, v)
	}
	return d, nil
}
