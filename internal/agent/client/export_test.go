package client

import (
	"time"

	"github.com/trick77/netra/internal/agent/collector"
	"github.com/trick77/netra/internal/agent/config"
)

// NewWithInterval builds a Client scraping at an arbitrary cadence.
//
// The production cadence is a fixed 60s (config.ScrapeInterval) and is
// deliberately not configurable, but Run's ticker behaviour — backoff,
// retry_after, cancellation, scraping while a flush is held off — can only be
// tested at a cadence measured in milliseconds. This file is compiled only
// into this package's test binary, so the escape hatch cannot leak into a
// production call site.
func NewWithInterval(cfg config.Config, collectors []collector.Collector, interval time.Duration) *Client {
	return newClient(cfg, collectors, interval)
}
