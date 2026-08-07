// Package config turns NETRA_* environment variables into an agent Config.
package config

import (
	"fmt"
	"os"
	"time"
)

// Config holds every agent setting. Only HubURL and Token are required.
type Config struct {
	HubURL       string
	Token        string
	Interval     time.Duration
	BufferWindow time.Duration
	ProcRoot     string
	SysRoot      string
	Location     string
	Provider     string
	Facility     string
	HostType     string
	LogLevel     string
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
	}

	if cfg.HubURL == "" {
		return Config{}, fmt.Errorf("NETRA_HUB_URL is required")
	}
	if cfg.Token == "" {
		return Config{}, fmt.Errorf("NETRA_TOKEN is required")
	}

	var err error
	if cfg.Interval, err = durationOr("NETRA_INTERVAL", time.Minute); err != nil {
		return Config{}, err
	}
	// The buffer window is coupled to the hub's continuous-aggregate
	// start_offset (6h). Raising it past that silently corrupts rollups for
	// replayed data, so it is documented rather than validated here.
	if cfg.BufferWindow, err = durationOr("NETRA_BUFFER_WINDOW", time.Hour); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
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
