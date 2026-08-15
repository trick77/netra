// Package config turns NETRA_* environment variables into a hub Config.
package config

import (
	"fmt"
	"os"
	"strings"
)

// Config holds every hub setting. There is no config file: env only, so a
// container is configured entirely by its compose file.
type Config struct {
	ListenAddr  string
	DatabaseDSN string
	AdminToken  string
	LogLevel    string

	// HubURL is the address agents post to, used only to render a
	// ready-to-paste setup-agent.sh command in the UI. It is optional to the
	// binary: the hub is reached on loopback by the browser and cannot infer
	// its own public name from that request, so rather than guess, an unset
	// value makes the UI render no setup command at all until an operator
	// types one. A command correct except for its hostname would copy, run,
	// succeed, and post that host's metrics to whoever owns the name.
	//
	// Optional here, required in compose.yaml, which derives it from
	// NETRA_HOSTNAME and marks that `:?`. Running the binary directly with it
	// unset is supported and degrades as described above.
	HubURL string
}

// Load reads the environment and applies defaults. It fails rather than
// starting with no database or an unauthenticated admin API.
func Load() (Config, error) {
	cfg := Config{
		ListenAddr:  envOr("NETRA_LISTEN_ADDR", ":8080"),
		DatabaseDSN: os.Getenv("NETRA_DB_DSN"),
		AdminToken:  os.Getenv("NETRA_ADMIN_TOKEN"),
		LogLevel:    envOr("NETRA_LOG_LEVEL", "info"),
		HubURL:      strings.TrimRight(os.Getenv("NETRA_HUB_URL"), "/"),
	}

	if cfg.DatabaseDSN == "" {
		return Config{}, fmt.Errorf("NETRA_DB_DSN is required")
	}
	if cfg.AdminToken == "" {
		return Config{}, fmt.Errorf("NETRA_ADMIN_TOKEN is required")
	}

	return cfg, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
