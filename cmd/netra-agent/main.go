// Command netra-agent collects host metrics and pushes them to a netra hub.
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/trick77/netra/internal/agent/client"
	"github.com/trick77/netra/internal/agent/collector"
	"github.com/trick77/netra/internal/agent/config"
	"github.com/trick77/netra/internal/buildinfo"
	"github.com/trick77/netra/internal/logging"
)

func main() {
	if buildinfo.HandleVersionFlag(os.Args, os.Stdout, "netra-agent") {
		return
	}

	if err := run(); err != nil && !errors.Is(err, context.Canceled) {
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

	slog.Info("starting netra agent",
		"version", buildinfo.Version(),
		"commit", buildinfo.Commit(),
		"hub", cfg.HubURL,
		"interval", config.ScrapeInterval)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	collectors := []collector.Collector{
		collector.NewCPU(cfg.ProcRoot, config.ScrapeInterval),
		collector.NewMemory(cfg.ProcRoot, config.ScrapeInterval),
		collector.NewLoad(cfg.ProcRoot, config.ScrapeInterval),
	}

	c := client.New(cfg, collectors)

	// Prime the CPU collector: it needs a baseline before it can report a
	// delta, and doing it here means the first scheduled scrape has one.
	// Prime does not buffer or send anything, unlike ScrapeOnce.
	c.Prime(ctx)

	return c.Run(ctx)
}
