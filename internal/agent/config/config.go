// Package config turns NETRA_* environment variables into an agent Config.
package config

import (
	"fmt"
	"os"
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
// A genuine per-host override belongs to the admin API in phase 2.
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

	// PidHost records whether the container was started with pid: host.
	//
	// The process collector can only guess otherwise, and every guess has a
	// case it gets wrong. setup-agent.sh already knows what it rendered, so it
	// writes the answer here and the guessing is skipped entirely.
	PidHost bool
}

// Load reads the environment and applies defaults.
func Load() (Config, error) {
	cfg := Config{
		HubURL:   os.Getenv("NETRA_HUB_URL"),
		Token:    os.Getenv("NETRA_TOKEN"),
		ProcRoot: envOr("NETRA_PROC_ROOT", "/proc"),
		SysRoot:  envOr("NETRA_SYSFS_ROOT", "/sys"),
		Location: os.Getenv("NETRA_LOCATION"),
		Provider: os.Getenv("NETRA_PROVIDER"),
		Facility: os.Getenv("NETRA_FACILITY"),
		HostType: os.Getenv("NETRA_HOST_TYPE"),
		LogLevel: envOr("NETRA_LOG_LEVEL", "info"),
		UtmpPath: envOr("NETRA_UTMP_PATH", "/var/run/utmp"),
		PidHost:  boolEnv("NETRA_PID_HOST"),
	}

	if cfg.HubURL == "" {
		return Config{}, fmt.Errorf("NETRA_HUB_URL is required")
	}
	if cfg.Token == "" {
		return Config{}, fmt.Errorf("NETRA_TOKEN is required")
	}

	var err error
	// The buffer window is coupled to the hub's continuous-aggregate
	// start_offset (MaxBufferWindow, 6h). Raising it past that would silently
	// exclude replayed data from rollups forever, so it is rejected here.
	if cfg.BufferWindow, err = durationOr("NETRA_BUFFER_WINDOW", time.Hour); err != nil {
		return Config{}, err
	}
	if cfg.BufferWindow > MaxBufferWindow {
		return Config{}, fmt.Errorf(
			"NETRA_BUFFER_WINDOW must not exceed %s (the hub's continuous-aggregate "+
				"start_offset); data buffered longer than that and then replayed would be "+
				"silently excluded from rollups forever, got %s",
			MaxBufferWindow, cfg.BufferWindow)
	}

	return cfg, nil
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
