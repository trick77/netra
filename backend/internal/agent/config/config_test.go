package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadRequiresHubURLAndToken(t *testing.T) {
	t.Setenv("AGENT_HUB_URL", "")
	t.Setenv("AGENT_TOKEN", "nta_x")
	if _, err := Load(); err == nil {
		t.Fatal("Load() succeeded with no AGENT_HUB_URL, want error")
	}

	t.Setenv("AGENT_HUB_URL", "http://hub:8080")
	t.Setenv("AGENT_TOKEN", "")
	if _, err := Load(); err == nil {
		t.Fatal("Load() succeeded with no AGENT_TOKEN, want error")
	}
}

func TestLoadDefaults(t *testing.T) {
	t.Setenv("AGENT_HUB_URL", "http://hub:8080")
	t.Setenv("AGENT_TOKEN", "nta_x")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.BufferWindow != time.Hour {
		t.Fatalf("BufferWindow = %v, want 1h", cfg.BufferWindow)
	}
	if cfg.ProcRoot != "/proc" {
		t.Fatalf("ProcRoot = %q, want %q", cfg.ProcRoot, "/proc")
	}
}

// Durations are duration strings, not millisecond integers: beszel's uint16
// field caps its interval at ~65s, and netra must not inherit that shape.
// An unparseable one must be rejected loudly rather than falling back to the
// default, which would leave the operator believing a setting took effect.
func TestLoadRejectsBadDuration(t *testing.T) {
	t.Setenv("AGENT_HUB_URL", "http://hub:8080")
	t.Setenv("AGENT_TOKEN", "nta_x")
	t.Setenv("AGENT_BUFFER_WINDOW", "sixty")

	if _, err := Load(); err == nil {
		t.Fatal("Load() succeeded with an unparseable AGENT_BUFFER_WINDOW, want error")
	}
}

// A buffer window past the hub's continuous-aggregate start_offset (6h) must
// be rejected: data replayed from a buffer that deep would land in a chunk
// TimescaleDB no longer re-materialises, silently excluding it from rollups
// forever (internal/hub/store/migrations/0001_init.sql).
func TestLoadRejectsBufferWindowPastHubStartOffset(t *testing.T) {
	t.Setenv("AGENT_HUB_URL", "http://hub:8080")
	t.Setenv("AGENT_TOKEN", "nta_x")
	t.Setenv("AGENT_BUFFER_WINDOW", "7h")

	if _, err := Load(); err == nil {
		t.Fatal("Load() succeeded with AGENT_BUFFER_WINDOW=7h (past the 6h hub start_offset), want error")
	}
}

// The bound is inclusive: exactly the hub's start_offset must be accepted.
func TestLoadAcceptsBufferWindowAtHubStartOffset(t *testing.T) {
	t.Setenv("AGENT_HUB_URL", "http://hub:8080")
	t.Setenv("AGENT_TOKEN", "nta_x")
	t.Setenv("AGENT_BUFFER_WINDOW", "6h")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.BufferWindow != MaxBufferWindow {
		t.Fatalf("BufferWindow = %v, want %v", cfg.BufferWindow, MaxBufferWindow)
	}
}

// The agent refuses the old prefix for the same reason the hub does, with more
// at stake: an agent .env lives on a host nobody logs into, and a silently
// unread AGENT_PID_HOST costs no crash, only a process count that quietly goes
// back to being guessed.
func TestLoadRejectsOldPrefix(t *testing.T) {
	t.Setenv("AGENT_HUB_URL", "http://hub:8080")
	t.Setenv("AGENT_TOKEN", "nta_x")
	t.Setenv("NETRA_PID_HOST", "1")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() succeeded with NETRA_PID_HOST set, want error")
	}
	if !strings.Contains(err.Error(), "AGENT_PID_HOST") {
		t.Fatalf("error %q does not name the replacement AGENT_PID_HOST", err)
	}
}
