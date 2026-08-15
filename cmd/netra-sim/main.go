// Command netra-sim populates a netra hub with a fake fleet.
//
// It is a DEVELOPMENT TOOL. It is not built into either container image and
// has no place on a production hub: it registers hosts, mints tokens for
// them and writes three months of invented history.
//
//	netra-sim --admin-token "$NETRA_ADMIN_TOKEN" \
//	          --dsn "postgres://netra:...@127.0.0.1:5432/netra" \
//	          --backfill 2160h --live
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/trick77/netra/internal/devtools/sim"
)

func main() {
	if err := run(); err != nil {
		slog.Error("netra-sim failed", "err", err)
		os.Exit(1)
	}
}

func run() error {
	hubURL := flag.String("hub", envOr("NETRA_HUB_URL", "http://127.0.0.1:8080"), "hub base URL")
	adminToken := flag.String("admin-token", os.Getenv("NETRA_ADMIN_TOKEN"), "hub admin token")
	dsn := flag.String("dsn", os.Getenv("NETRA_SIM_DSN"),
		"hub database DSN, used only to materialise continuous aggregates over the backfill; "+
			"without it only the last few hours roll up")
	hosts := flag.String("hosts", "", "comma-separated profiles to simulate (default: all)")
	backfill := flag.Duration("backfill", 2160*time.Hour, "how much history to generate (90 days)")
	live := flag.Bool("live", false, "keep posting one scrape per host per minute after the backfill")
	seed := flag.Uint64("seed", 1, "random seed; the same seed regenerates identical history")
	fresh := flag.Bool("fresh", false, "delete the simulated hosts and their history first")
	logLevel := flag.String("log-level", "info", "debug, info, warn or error")
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: parseLevel(*logLevel)})))

	if *adminToken == "" {
		return fmt.Errorf("--admin-token is required (or set NETRA_ADMIN_TOKEN)")
	}

	profiles := sim.Fleet()
	if strings.TrimSpace(*hosts) != "" {
		var err error
		profiles, err = sim.ByName(splitList(*hosts))
		if err != nil {
			return err
		}
	}

	// Ctrl-C stops the run rather than killing it: live mode is meant to be
	// left running, and a backfill in progress should stop between batches
	// instead of mid-POST.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg := sim.Config{
		Seed:     *seed,
		Backfill: *backfill,
		Live:     *live,
		Fresh:    *fresh,
		Log:      slog.Default(),
	}

	if *dsn != "" {
		refresher, err := sim.NewRefresher(ctx, *dsn)
		if err != nil {
			return fmt.Errorf("connect to the hub database: %w", err)
		}
		defer refresher.Close()
		cfg.Refresher = refresher
	} else if *backfill > 12*time.Hour {
		slog.Warn("no --dsn: the 5m and 1h rollups will only cover the last few hours, " +
			"because the hub's own refresh policies do not reach further back than that")
	}

	if err := sim.Run(ctx, sim.NewHub(*hubURL, *adminToken), profiles, cfg); err != nil {
		if ctx.Err() != nil {
			slog.Info("interrupted")
			return nil
		}
		return err
	}
	return nil
}

func splitList(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
