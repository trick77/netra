package config

import (
	"strings"
	"testing"
)

func TestLoadAppliesDefaults(t *testing.T) {
	t.Setenv("BACKEND_DB_DSN", "postgres://localhost/netra")
	t.Setenv("BACKEND_ADMIN_TOKEN", "secret")

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
	t.Setenv("BACKEND_DB_DSN", "")
	t.Setenv("BACKEND_ADMIN_TOKEN", "secret")

	if _, err := Load(); err == nil {
		t.Fatal("Load() succeeded with no BACKEND_DB_DSN, want error")
	}
}

func TestLoadRequiresAdminToken(t *testing.T) {
	t.Setenv("BACKEND_DB_DSN", "postgres://localhost/netra")
	t.Setenv("BACKEND_ADMIN_TOKEN", "")

	if _, err := Load(); err == nil {
		t.Fatal("Load() succeeded with no BACKEND_ADMIN_TOKEN, want error")
	}
}

func TestLoadOIDCDisabledByDefault(t *testing.T) {
	t.Setenv("BACKEND_DB_DSN", "postgres://x")
	t.Setenv("BACKEND_ADMIN_TOKEN", "secret")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.OIDC.Enabled() {
		t.Error("OIDC should be off when BACKEND_OIDC_ISSUER is unset")
	}
}

func TestLoadOIDCRequiresItsCompanions(t *testing.T) {
	// Setting the issuer and forgetting a credential is a misconfiguration, not
	// a request for token-only login. It must stop the hub rather than start it
	// with a sign-in button that cannot work.
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"no client id", map[string]string{
			"BACKEND_OIDC_ISSUER": "https://auth.example.com",
			"BACKEND_HUB_URL":     "https://netra.example.com",
		}, "BACKEND_OIDC_CLIENT_ID"},
		{"no client secret", map[string]string{
			"BACKEND_OIDC_ISSUER":    "https://auth.example.com",
			"BACKEND_OIDC_CLIENT_ID": "netra",
			"BACKEND_HUB_URL":        "https://netra.example.com",
		}, "BACKEND_OIDC_CLIENT_SECRET"},
		{"no hub url to derive the redirect from", map[string]string{
			"BACKEND_OIDC_ISSUER":        "https://auth.example.com",
			"BACKEND_OIDC_CLIENT_ID":     "netra",
			"BACKEND_OIDC_CLIENT_SECRET": "shh",
		}, "BACKEND_HUB_URL"},
		{"hub url is not https", map[string]string{
			"BACKEND_OIDC_ISSUER":        "https://auth.example.com",
			"BACKEND_OIDC_CLIENT_ID":     "netra",
			"BACKEND_OIDC_CLIENT_SECRET": "shh",
			"BACKEND_HUB_URL":            "http://netra.example.com",
		}, "https://"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("BACKEND_DB_DSN", "postgres://x")
			t.Setenv("BACKEND_ADMIN_TOKEN", "secret")
			t.Setenv("BACKEND_HUB_URL", "")
			for k, v := range tc.env {
				t.Setenv(k, v)
			}

			_, err := Load()
			if err == nil {
				t.Fatal("expected Load to reject the configuration")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestRedirectURLIsDerivedFromHubURL(t *testing.T) {
	// Derived, not configured: the value the provider redirects to cannot drift
	// from the name Traefik routes on, because both come from BACKEND_HOSTNAME.
	t.Setenv("BACKEND_DB_DSN", "postgres://x")
	t.Setenv("BACKEND_ADMIN_TOKEN", "secret")
	t.Setenv("BACKEND_HUB_URL", "https://netra.example.com/")
	t.Setenv("BACKEND_OIDC_ISSUER", "https://auth.example.com")
	t.Setenv("BACKEND_OIDC_CLIENT_ID", "netra")
	t.Setenv("BACKEND_OIDC_CLIENT_SECRET", "shh")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := cfg.RedirectURL(), "https://netra.example.com/auth/callback"; got != want {
		t.Errorf("RedirectURL() = %q, want %q", got, want)
	}
}

// A stale .env carried across the rename is refused by name, not ignored. The
// variable picked here is one with a default: BACKEND_LISTEN_ADDR being unset
// is not an error, so nothing else in Load would ever notice it was missing.
func TestLoadRejectsOldPrefix(t *testing.T) {
	t.Setenv("BACKEND_DB_DSN", "postgres://localhost/netra")
	t.Setenv("BACKEND_ADMIN_TOKEN", "secret")
	t.Setenv("NETRA_LISTEN_ADDR", ":9000")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() succeeded with NETRA_LISTEN_ADDR set, want error")
	}
	if !strings.Contains(err.Error(), "BACKEND_LISTEN_ADDR") {
		t.Fatalf("error %q does not name the replacement BACKEND_LISTEN_ADDR", err)
	}
}
