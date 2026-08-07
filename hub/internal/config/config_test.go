package config

import "testing"

func TestLoadAppliesDefaults(t *testing.T) {
	t.Setenv("NETRA_DB_DSN", "postgres://localhost/netra")
	t.Setenv("NETRA_ADMIN_TOKEN", "secret")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ListenAddr != ":8080" {
		t.Fatalf("ListenAddr = %q, want %q", cfg.ListenAddr, ":8080")
	}
	if cfg.LogLevel != "info" {
		t.Fatalf("LogLevel = %q, want %q", cfg.LogLevel, "info")
	}
}

func TestLoadRequiresDSN(t *testing.T) {
	t.Setenv("NETRA_DB_DSN", "")
	t.Setenv("NETRA_ADMIN_TOKEN", "secret")

	if _, err := Load(); err == nil {
		t.Fatal("Load() succeeded with no NETRA_DB_DSN, want error")
	}
}

func TestLoadRequiresAdminToken(t *testing.T) {
	t.Setenv("NETRA_DB_DSN", "postgres://localhost/netra")
	t.Setenv("NETRA_ADMIN_TOKEN", "")

	if _, err := Load(); err == nil {
		t.Fatal("Load() succeeded with no NETRA_ADMIN_TOKEN, want error")
	}
}
