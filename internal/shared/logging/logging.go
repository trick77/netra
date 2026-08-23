// Package logging turns the log-level config value into the default slog
// handler.
//
// Both binaries parsed their log level into their Config and then never used
// it: neither main called slog.SetDefault, so the variable documented in the
// spec and in .env.example did nothing at all. The visible cost was that every
// slog.Debug in the tree was unreachable — including the hub's "ingesting
// backfilled batch" line, which is the only observability the backfill flag
// has.
//
// One package rather than a helper per main, so the hub and the agent cannot
// drift into accepting different spellings of the same level.
package logging

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// Setup installs a default slog handler at the named level. envVar is the
// variable the level came from -- BACKEND_LOG_LEVEL for the hub,
// AGENT_LOG_LEVEL for the agent -- so the error names the thing to go and fix.
// The two binaries no longer share a variable name, only this parser.
//
// An unrecognised level is an ERROR, not a silent fallback to info. The whole
// failure this replaces was a logging knob that quietly did nothing, and
// "BACKEND_LOG_LEVEL=DEBUG_" behaving exactly like a correct value would be the
// same bug wearing a different hat.
func Setup(envVar, level string) error {
	lvl, err := parseLevel(envVar, level)
	if err != nil {
		return err
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: lvl,
	})))
	return nil
}

func parseLevel(envVar, level string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug, nil
	case "", "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("%s %q is not one of debug, info, warn, error", envVar, level)
	}
}
