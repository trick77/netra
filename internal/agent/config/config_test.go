package config

import (
	"testing"
	"time"
)

func TestLoadRequiresHubURLAndToken(t *testing.T) {
	t.Setenv("NETRA_HUB_URL", "")
	t.Setenv("NETRA_TOKEN", "nta_x")
	if _, err := Load(); err == nil {
		t.Fatal("Load() succeeded with no NETRA_HUB_URL, want error")
	}

	t.Setenv("NETRA_HUB_URL", "http://hub:8080")
	t.Setenv("NETRA_TOKEN", "")
	if _, err := Load(); err == nil {
		t.Fatal("Load() succeeded with no NETRA_TOKEN, want error")
	}
}

func TestLoadDefaults(t *testing.T) {
	t.Setenv("NETRA_HUB_URL", "http://hub:8080")
	t.Setenv("NETRA_TOKEN", "nta_x")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Interval != time.Minute {
		t.Fatalf("Interval = %v, want 1m", cfg.Interval)
	}
	if cfg.BufferWindow != time.Hour {
		t.Fatalf("BufferWindow = %v, want 1h", cfg.BufferWindow)
	}
	if cfg.ProcRoot != "/proc" {
		t.Fatalf("ProcRoot = %q, want %q", cfg.ProcRoot, "/proc")
	}
}

// The interval is a duration string, not a millisecond integer: beszel's
// uint16 field caps its interval at ~65s, and netra must not inherit that.
func TestLoadParsesDurationInterval(t *testing.T) {
	t.Setenv("NETRA_HUB_URL", "http://hub:8080")
	t.Setenv("NETRA_TOKEN", "nta_x")
	t.Setenv("NETRA_INTERVAL", "5m")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Interval != 5*time.Minute {
		t.Fatalf("Interval = %v, want 5m", cfg.Interval)
	}
}

func TestLoadRejectsBadDuration(t *testing.T) {
	t.Setenv("NETRA_HUB_URL", "http://hub:8080")
	t.Setenv("NETRA_TOKEN", "nta_x")
	t.Setenv("NETRA_INTERVAL", "sixty")

	if _, err := Load(); err == nil {
		t.Fatal("Load() succeeded with an unparseable NETRA_INTERVAL, want error")
	}
}

// A buffer window past the hub's continuous-aggregate start_offset (6h) must
// be rejected: data replayed from a buffer that deep would land in a chunk
// TimescaleDB no longer re-materialises, silently excluding it from rollups
// forever (internal/hub/store/migrations/0002_host_samples.sql).
func TestLoadRejectsBufferWindowPastHubStartOffset(t *testing.T) {
	t.Setenv("NETRA_HUB_URL", "http://hub:8080")
	t.Setenv("NETRA_TOKEN", "nta_x")
	t.Setenv("NETRA_BUFFER_WINDOW", "7h")

	if _, err := Load(); err == nil {
		t.Fatal("Load() succeeded with NETRA_BUFFER_WINDOW=7h (past the 6h hub start_offset), want error")
	}
}

// The bound is inclusive: exactly the hub's start_offset must be accepted.
func TestLoadAcceptsBufferWindowAtHubStartOffset(t *testing.T) {
	t.Setenv("NETRA_HUB_URL", "http://hub:8080")
	t.Setenv("NETRA_TOKEN", "nta_x")
	t.Setenv("NETRA_BUFFER_WINDOW", "6h")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.BufferWindow != MaxBufferWindow {
		t.Fatalf("BufferWindow = %v, want %v", cfg.BufferWindow, MaxBufferWindow)
	}
}
