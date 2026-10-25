// Package config turns BACKEND_* environment variables into a hub Config.
package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/trick77/ship/kit/env"
)

// Config holds every hub setting. There is no config file: env only, so a
// container is configured entirely by its compose file.
type Config struct {
	ListenAddr  string
	DatabaseDSN string
	AdminToken  string
	LogLevel    string

	// HubURL is the address agents post to, https://<PUBLIC_FQDN>, used to render a
	// ready-to-paste setup-agent.sh command in the UI and, when OIDC is
	// configured, to derive the redirect URI. Deriving both from PUBLIC_FQDN means
	// the value Authelia is told to redirect to cannot drift from the name
	// Traefik actually routes on.
	//
	// It is optional to the binary: the hub is reached on loopback by the
	// browser and cannot infer its own public name from that request, so rather
	// than guess, an unset PUBLIC_FQDN makes the UI render no setup command at all
	// until an operator types one. A command correct except for its hostname
	// would copy, run, succeed, and post that host's metrics to whoever owns the
	// name.
	//
	// Optional here, required in compose.yaml, which marks PUBLIC_FQDN `:?` in the
	// Traefik rule. Running the binary directly with it unset is supported and
	// degrades as described above -- browser sign-in is then unconfigurable
	// too, since there is no redirect URI to derive.
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

// MaxAdminTokenBytes bounds BACKEND_ADMIN_TOKEN. The login form caps its body
// at four times this, so a token this long still fits with every byte
// url-encoded as %XX.
// `openssl rand -hex 32`, the documented way to make one, is 64.
const MaxAdminTokenBytes = 2 << 10

// Load reads the environment and applies defaults. It fails rather than
// starting with no database or an unauthenticated admin API, and reports every
// fault at once rather than one per restart.
func Load() (Config, error) {
	if err := rejectOldPrefix(); err != nil {
		return Config{}, err
	}

	r := env.New(os.Getenv)
	cfg := Config{
		ListenAddr:  r.String("BACKEND_LISTEN_ADDR", ":8080"),
		DatabaseDSN: r.Required("BACKEND_DB_DSN"),
		// The admin token stays required even with OIDC configured. netra-sim
		// and any curl against the admin API present it, and it is the way
		// back in when the identity provider is the thing that is down --
		// which, for a monitoring hub, is exactly when you need to look at it.
		AdminToken: r.Required("BACKEND_ADMIN_TOKEN"),
		LogLevel:   r.String("BACKEND_LOG_LEVEL", "info"),
		OIDC: OIDCConfig{
			Issuer:       r.String("BACKEND_OIDC_ISSUER", ""),
			ClientID:     r.String("BACKEND_OIDC_CLIENT_ID", ""),
			ClientSecret: r.String("BACKEND_OIDC_CLIENT_SECRET", ""),
		},
	}

	// A bare hostname only: a scheme or path here would render
	// "https://https://…" or a path-prefixed ingest URL into every setup
	// command, which the hub is never served under.
	if fqdn := strings.TrimSpace(r.String("PUBLIC_FQDN", "")); fqdn != "" {
		if strings.Contains(fqdn, "://") || strings.Contains(fqdn, "/") {
			r.Fail("PUBLIC_FQDN must be a bare hostname such as netra.example.com, got %q", fqdn)
		} else {
			cfg.HubURL = "https://" + fqdn
		}
	}

	// Longer would start a hub whose login form answers every attempt 413.
	if len(cfg.AdminToken) > MaxAdminTokenBytes {
		r.Fail("BACKEND_ADMIN_TOKEN is longer than %d bytes", MaxAdminTokenBytes)
	}

	if cfg.OIDC.Enabled() {
		if cfg.OIDC.ClientID == "" {
			r.Fail("BACKEND_OIDC_CLIENT_ID is required when BACKEND_OIDC_ISSUER is set")
		}
		if cfg.OIDC.ClientSecret == "" {
			r.Fail("BACKEND_OIDC_CLIENT_SECRET is required when BACKEND_OIDC_ISSUER is set")
		}
		// The redirect URI is derived from HubURL, so an unset PUBLIC_FQDN would send
		// the provider to "/auth/callback" with no host. Fail here rather than
		// at the first login attempt. HubURL is always https, which the Secure
		// session cookie needs.
		if cfg.HubURL == "" {
			r.Fail("PUBLIC_FQDN is required when BACKEND_OIDC_ISSUER is set (the OIDC redirect URI is derived from it)")
		}
	}

	if err := r.Err(); err != nil {
		return Config{}, err
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
