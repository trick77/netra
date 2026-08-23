// Command netra is the monitoring hub: it accepts agent metric batches and
// stores them in TimescaleDB.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/trick77/netra/internal/hub/auth"
	"github.com/trick77/netra/internal/hub/config"
	"github.com/trick77/netra/internal/hub/httpapi"
	"github.com/trick77/netra/internal/hub/oidc"
	"github.com/trick77/netra/internal/hub/store"
	"github.com/trick77/netra/internal/shared/buildinfo"
	"github.com/trick77/netra/internal/shared/logging"
)

func main() {
	if buildinfo.HandleVersionFlag(os.Args, os.Stdout, "netra") {
		return
	}

	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// Before the first log line, or the level would not apply to it.
	if err := logging.Setup(cfg.LogLevel); err != nil {
		return err
	}

	slog.Info("starting netra hub",
		"version", buildinfo.Version(),
		"commit", buildinfo.Commit(),
		"listen", cfg.ListenAddr)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
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
	var oidcSvc *oidc.Service
	if cfg.OIDC.Enabled() {
		// Bounded, because "cannot be reached" includes an issuer that drops
		// packets rather than refusing them. On the process-lifetime context
		// that discovery would hang here forever: the hub never binds, never
		// logs a reason, and only a SIGTERM ends it.
		discovery, cancel := context.WithTimeout(ctx, 15*time.Second)
		oidcSvc, err = oidc.New(discovery, cfg.OIDC.Issuer, cfg.OIDC.ClientID, cfg.OIDC.ClientSecret, cfg.RedirectURL())
		cancel()
		if err != nil {
			return fmt.Errorf("oidc init: %w", err)
		}
		slog.Info("browser sign-in enabled", "issuer", cfg.OIDC.Issuer, "redirect", cfg.RedirectURL())
	}

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           httpapi.NewRouter(auth.NewAuthenticator(s.Pool()), s, cfg, oidcSvc),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		slog.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}
