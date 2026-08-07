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

	"github.com/trick77/netra/internal/buildinfo"
	"github.com/trick77/netra/internal/hub/auth"
	"github.com/trick77/netra/internal/hub/config"
	"github.com/trick77/netra/internal/hub/httpapi"
	"github.com/trick77/netra/internal/hub/store"
)

// defaultInterval is the scrape interval handed to agents. 60s matches the
// spec's default; per-host overrides arrive with the admin API.
const defaultInterval = 60 * time.Second

// versionFlag reports whether the process was asked only to print its build
// identity. Kept deliberately simple: neither binary takes any other flag.
func versionFlag() bool {
	return len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "-version")
}

func main() {
	if versionFlag() {
		fmt.Printf("%s %s (commit %s, %s)\n",
			"netra", buildinfo.Version(), buildinfo.Commit(), buildinfo.GoVersion())
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

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           httpapi.NewRouter(auth.NewAuthenticator(s.Pool()), s, defaultInterval),
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
