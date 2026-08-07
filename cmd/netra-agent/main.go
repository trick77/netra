// Command netra-agent collects host metrics and pushes them to a netra hub.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/trick77/netra/internal/agent/client"
	"github.com/trick77/netra/internal/agent/collector"
	"github.com/trick77/netra/internal/agent/config"
	"github.com/trick77/netra/internal/buildinfo"
)

// versionFlag reports whether the process was asked only to print its build
// identity. Kept deliberately simple: neither binary takes any other flag.
func versionFlag() bool {
	return len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "-version")
}

func main() {
	if versionFlag() {
		fmt.Printf("%s %s (commit %s, %s)\n",
			"netra-agent", buildinfo.Version(), buildinfo.Commit(), buildinfo.GoVersion())
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

	slog.Info("starting netra agent",
		"version", buildinfo.Version(),
		"commit", buildinfo.Commit(),
		"hub", cfg.HubURL,
		"interval", cfg.Interval)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	collectors := []collector.Collector{
		collector.NewCPU(cfg.ProcRoot, cfg.Interval),
		collector.NewMemory(cfg.ProcRoot, cfg.Interval),
		collector.NewLoad(cfg.ProcRoot, cfg.Interval),
	}

	c := client.New(cfg, collectors)

	// Prime the CPU collector: it needs a baseline before it can report a
	// delta, and doing it here means the first scheduled scrape has one.
	// Prime does not buffer or send anything, unlike ScrapeOnce.
	c.Prime(ctx)

	return c.Run(ctx)
}
