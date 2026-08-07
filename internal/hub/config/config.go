// Package config turns NETRA_* environment variables into a hub Config.
package config

import (
	"fmt"
	"os"
)

// Config holds every hub setting. There is no config file: env only, so a
// container is configured entirely by its compose file.
type Config struct {
	ListenAddr  string
	DatabaseDSN string
	AdminToken  string
	LogLevel    string
}

// Load reads the environment and applies defaults. It fails rather than
// starting with no database or an unauthenticated admin API.
func Load() (Config, error) {
	cfg := Config{
		ListenAddr:  envOr("NETRA_LISTEN_ADDR", ":8080"),
		DatabaseDSN: os.Getenv("NETRA_DB_DSN"),
		AdminToken:  os.Getenv("NETRA_ADMIN_TOKEN"),
		LogLevel:    envOr("NETRA_LOG_LEVEL", "info"),
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
