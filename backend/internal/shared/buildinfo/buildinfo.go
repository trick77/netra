// Package buildinfo exposes build-time identity. The version is stamped with
// -ldflags -X at release; the default is what a local `go build` produces.
package buildinfo

import "runtime"

var version = "dev"

// Version returns the semantic version, or "dev" for an unstamped build.
func Version() string { return version }

// GoVersion returns the Go toolchain version the binary was built with.
func GoVersion() string { return runtime.Version() }
