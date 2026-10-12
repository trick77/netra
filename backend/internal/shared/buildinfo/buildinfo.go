// Package buildinfo exposes build-time identity. Values are stamped with
// -ldflags -X at release; the defaults are what a local `go build` produces.
package buildinfo

import "runtime"

var (
	version = "dev"
	commit  = "unknown"
)

// Version returns the semantic version, or "dev" for an unstamped build.
func Version() string { return version }

// Commit returns the short git SHA, or "unknown" for an unstamped build.
func Commit() string { return commit }

// GoVersion returns the Go toolchain version the binary was built with.
func GoVersion() string { return runtime.Version() }
