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
	"github.com/trick77/netra/internal/shared/buildinfo"
	"github.com/trick77/netra/internal/shared/logging"
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

	// No cadence is threaded through here. The scrape loop runs every
	// collector on every tick at config.ScrapeInterval, and the two that must
	// run less often gate themselves: Smart against cfg.SmartInterval below,
	// Packages against its database's mtime with a daily floor. Handing each
	// constructor an interval that nothing read described a schedule that did
	// not exist.
	collectors := []collector.Collector{
		collector.NewCPU(cfg.ProcRoot),
		collector.NewPerCoreCPU(cfg.ProcRoot),
		collector.NewMemory(cfg.ProcRoot),
		collector.NewLoad(cfg.ProcRoot),
		collector.NewKernelStat(cfg.ProcRoot),
		collector.NewVMStat(cfg.ProcRoot),
		collector.NewLimits(cfg.ProcRoot),
		collector.NewProcs(cfg.ProcRoot, cfg.PidHost),
		collector.NewNetstat(cfg.ProcRoot),
		collector.NewUsers(cfg.UtmpPath),

		// Group 1: no privileges, no dependencies.
		collector.NewDiskIO(cfg.ProcRoot),
		collector.NewSensors(cfg.SysRoot, cfg.SensorsTimeout),
		collector.NewMdraid(cfg.SysRoot),

		// Group 2: needs network_mode: host to see the host's interfaces
		// rather than the container's.
		collector.NewNetwork(cfg.ProcRoot),
		collector.NewAddresses(collector.SystemIfaces),

		// Group 3: needs a mount.
		collector.NewContainers(cfg.CgroupRoot, cfg.ProcRoot, collector.SystemDockerContainers),
		collector.NewFilesystems(cfg.ProcRoot, collector.SystemStatfs),
		collector.NewSystemd(collector.SystemUnits),
		collector.NewPackages(cfg.DpkgStatus, cfg.ApkInstalled),

		// Group 4: privileged, opt-in. SMART gates itself to cfg.SmartInterval
		// internally -- the scrape loop runs every collector on every tick, and
		// waking sleeping drives once a minute would shorten their life.
		collector.NewSmart(cfg.SmartInterval, collector.SystemSmartctl),
		collector.NewProcesses(cfg.ProcRoot, cfg.PidHost),
	}

	c := client.New(cfg, collectors)

	// Prime the delta-based collectors (CPU, kernelstat, netstat): each needs
	// a baseline before it can report a rate, and doing it here means the
	// first scheduled scrape has one. Prime does not buffer or send anything,
	// unlike ScrapeOnce.
	c.Prime(ctx)

	return c.Run(ctx)
}
