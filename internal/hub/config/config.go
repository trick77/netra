// Package config turns BACKEND_* environment variables into a hub Config.
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

	// HubURL is the address agents post to, used to render a
	// ready-to-paste setup-agent.sh command in the UI and, when OIDC is
	// configured, to derive the redirect URI. Deriving that rather than
	// configuring it means the value Authelia is told to redirect to cannot
	// drift from the name Traefik actually routes on: both come from
	// BACKEND_HOSTNAME.
	//
	// It is optional to the binary: the hub is reached on loopback by the
	// browser and cannot infer its own public name from that request, so rather
	// than guess, an unset value makes the UI render no setup command at all
	// until an operator types one. A command correct except for its hostname
	// would copy, run, succeed, and post that host's metrics to whoever owns the
	// name.
	//
	// Optional here, required in compose.yaml, which derives it from
	// BACKEND_HOSTNAME and marks that `:?`. Running the binary directly with it
	// unset is supported and degrades as described above -- browser sign-in is
	// then unconfigurable too, since there is no redirect URI to derive.
	HubURL string

	// OIDC is the optional browser login. Zero value means the hub behaves
	// exactly as it did before: the admin token is the only credential.
	OIDC OIDCConfig
}

// OIDCConfig holds the OpenID Connect settings for browser sign-in.
//
// Deliberately no group or role field. Netra has one role -- everyone who gets
// in is an admin -- so a groups claim would be read and then have nothing to
// decide. Who may reach netra at all is the identity provider's business, and
// is expressed there as an authorization policy on this client.
type OIDCConfig struct {
	Issuer       string
	ClientID     string
	ClientSecret string
}

// Enabled reports whether browser sign-in is configured. The issuer alone is
// the switch: setting it and forgetting a credential is a misconfiguration that
// Load rejects, not a silent fallback to token-only login.
func (c OIDCConfig) Enabled() bool { return c.Issuer != "" }

// Load reads the environment and applies defaults. It fails rather than
// starting with no database or an unauthenticated admin API.
func Load() (Config, error) {
	if err := rejectOldPrefix(); err != nil {
		return Config{}, err
	}

	cfg := Config{
		ListenAddr:  envOr("BACKEND_LISTEN_ADDR", ":8080"),
		DatabaseDSN: os.Getenv("BACKEND_DB_DSN"),
		AdminToken:  os.Getenv("BACKEND_ADMIN_TOKEN"),
		LogLevel:    envOr("BACKEND_LOG_LEVEL", "info"),
		HubURL:      strings.TrimRight(os.Getenv("BACKEND_HUB_URL"), "/"),
		OIDC: OIDCConfig{
			Issuer:       strings.TrimRight(os.Getenv("BACKEND_OIDC_ISSUER"), "/"),
			ClientID:     os.Getenv("BACKEND_OIDC_CLIENT_ID"),
			ClientSecret: os.Getenv("BACKEND_OIDC_CLIENT_SECRET"),
		},
	}

	if cfg.DatabaseDSN == "" {
		return Config{}, fmt.Errorf("BACKEND_DB_DSN is required")
	}

	// The admin token stays required even with OIDC configured. netra-sim and
	// any curl against the admin API present it, and it is the way back in when
	// the identity provider is the thing that is down -- which, for a
	// monitoring hub, is exactly when you need to look at it.
	if cfg.AdminToken == "" {
		return Config{}, fmt.Errorf("BACKEND_ADMIN_TOKEN is required")
	}

	if cfg.OIDC.Enabled() {
		if cfg.OIDC.ClientID == "" {
			return Config{}, fmt.Errorf("BACKEND_OIDC_CLIENT_ID is required when BACKEND_OIDC_ISSUER is set")
		}
		if cfg.OIDC.ClientSecret == "" {
			return Config{}, fmt.Errorf("BACKEND_OIDC_CLIENT_SECRET is required when BACKEND_OIDC_ISSUER is set")
		}
		// The redirect URI is derived from HubURL, so an unset BACKEND_HUB_URL
		// would send the provider to "/auth/callback" with no host. Fail here
		// rather than at the first login attempt.
		if cfg.HubURL == "" {
			return Config{}, fmt.Errorf("BACKEND_HUB_URL is required when BACKEND_OIDC_ISSUER is set (the OIDC redirect URI is derived from it)")
		}
		if !strings.HasPrefix(cfg.HubURL, "https://") {
			return Config{}, fmt.Errorf("BACKEND_HUB_URL must be https:// when BACKEND_OIDC_ISSUER is set (the session cookie is Secure and will not be sent over http)")
		}
	}

	return cfg, nil
}

// RedirectURL is the OIDC redirect URI, derived from HubURL. It must match the
// redirect_uris entry registered for this client at the provider exactly.
func (c Config) RedirectURL() string { return c.HubURL + "/auth/callback" }

// rejectOldPrefix refuses to start while a NETRA_-prefixed variable is still
// set. Every hub variable was renamed to BACKEND_ in one go, and the swap is
// mechanical, so the message can name the replacement.
//
// Loud rather than ignored: a stale .env carried across the rename sets
// NETRA_ADMIN_TOKEN, and silently reading nothing would start a hub that
// refuses every login with no clue as to why -- or, worse for a variable with
// a default like NETRA_LISTEN_ADDR, one that quietly listens somewhere else.
func rejectOldPrefix() error {
	for _, kv := range os.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		if old, found := strings.CutPrefix(key, "NETRA_"); found {
			return fmt.Errorf("%s is no longer read; rename it to BACKEND_%s", key, old)
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
