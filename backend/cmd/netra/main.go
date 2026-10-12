// Command netra is the monitoring hub: it accepts agent metric batches and
// stores them in TimescaleDB.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	kitauth "github.com/trick77/ship/kit/auth"
	"github.com/trick77/ship/kit/httpx"
	"github.com/trick77/ship/kit/logging"
	kitrun "github.com/trick77/ship/kit/run"
	"github.com/trick77/ship/kit/version"

	"github.com/trick77/netra/internal/hub/auth"
	"github.com/trick77/netra/internal/hub/conditions"
	"github.com/trick77/netra/internal/hub/config"
	"github.com/trick77/netra/internal/hub/httpapi"
	"github.com/trick77/netra/internal/hub/store"
)

func main() {
	// The container healthcheck: the distroless image has no shell and no
	// wget, so the binary probes itself. Before config.Load, which a probe has
	// no business failing on.
	healthcheck := flag.Bool("healthcheck", false, "probe this hub's /healthz and exit 0 when it answers")
	flag.Parse()
	if *healthcheck {
		// Trimmed as config.Load trims it, so the probe dials what the server bound.
		addr := strings.TrimSpace(os.Getenv("BACKEND_LISTEN_ADDR"))
		if addr == "" {
			addr = ":8080"
		}
		os.Exit(httpx.Healthcheck(httpx.LocalURL(addr, "/healthz")))
	}

	if err := run(); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("fatal", logging.Err(err))
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// Before the first log line, or the level would not apply to it.
	if _, err := logging.Setup(logging.Options{Level: cfg.LogLevel, LevelVar: "BACKEND_LOG_LEVEL"}); err != nil {
		return err
	}

	slog.Info("starting netra hub",
		"version", version.String(),
		"listen", cfg.ListenAddr)

	// SIGINT/SIGTERM end the whole run, startup included: a stop while
	// Migrate waits on another replica's lock, or OIDC discovery hangs, unwinds
	// through the defers instead of killing the process mid-way.
	ctx, stop := kitrun.Context(context.Background())
	defer stop()

	s, err := store.Open(ctx, cfg.DatabaseDSN)
	if err != nil {
		return err
	}
	defer s.Close()

	if err := s.Migrate(ctx); err != nil {
		return err
	}

	// Browser sign-in, when configured. Discovery is a network call, so it
	// happens once here rather than on the first login attempt: a provider that
	// cannot be reached should stop the hub starting, not surface as a broken
	// button to whoever tries first. The admin token still works either way,
	// which is what makes failing here safe -- there is a way back in.
	var oidcSvc *kitauth.OIDC
	if cfg.OIDC.Enabled() {
		// Bounded, because "cannot be reached" includes an issuer that drops
		// packets rather than refusing them. On the process-lifetime context
		// that discovery would hang here forever: the hub never binds, never
		// logs a reason, and only a SIGTERM ends it.
		discovery, cancel := context.WithTimeout(ctx, 15*time.Second)
		oidcSvc, err = kitauth.NewOIDC(discovery, kitauth.OIDCConfig{
			Issuer:       cfg.OIDC.Issuer,
			ClientID:     cfg.OIDC.ClientID,
			ClientSecret: cfg.OIDC.ClientSecret,
			RedirectURL:  cfg.RedirectURL(),
			// No "groups": netra has one role, so the claim would be requested
			// and then ignored.
			Scopes:       []string{"openid", "profile", "email"},
			CookiePrefix: "netra_",
			Secure:       true,
		})
		cancel()
		if err != nil {
			return fmt.Errorf("oidc init: %w", err)
		}
		slog.Info("browser sign-in enabled", "issuer", cfg.OIDC.Issuer, "redirect", cfg.RedirectURL())
	}

	// The hub's first background loop, and the reason it has to be one: every
	// other condition is a reading in the data, but `silent` is the ABSENCE of
	// data. No ingest happens for a host that has stopped talking, so nothing
	// driven by ingest would ever notice.
	//
	// Started before the listener rather than after, so the warm-up clock
	// begins at the earliest honest moment. That clock is what stops a hub
	// restart recording one outage of its own as an outage of every host it
	// monitors -- see warmUp in the conditions package.
	//
	// One instance is assumed. A second hub against the same database would
	// evaluate in parallel; the writes are idempotent (ON CONFLICT DO NOTHING
	// against the partial unique index, and a resolve that guards on
	// resolved_ts IS NULL), so the outcome is correct rather than corrupt, but
	// nothing here elects a leader.
	// Its own context, cancelled before the pool closes.
	//
	// The process context's cancel is deferred above the store's Close, and
	// defers run last-in-first-out, so on the error path the pool would shut
	// while the evaluator was still querying it. Harmless -- the process is
	// leaving either way -- but it logs failures that describe the shutdown
	// rather than anything wrong.
	evalCtx, stopEval := context.WithCancel(ctx)
	defer stopEval()
	go conditions.New(s, time.Now()).Run(evalCtx)

	srv := httpx.NewServer(cfg.ListenAddr,
		httpapi.NewRouter(auth.NewAuthenticator(s.Pool()), s, cfg, oidcSvc))
	return httpx.Serve(ctx, srv, 15*time.Second)
}
