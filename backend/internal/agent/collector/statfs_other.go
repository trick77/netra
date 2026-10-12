//go:build !linux

package collector

import "errors"

// SystemStatfs is unavailable off Linux.
//
// The agent ships on Linux only, but the package must still build on the
// macOS machines the test suite runs on. Returning an error rather than
// omitting the symbol keeps every caller compiling; the filesystem collector
// then skips each mount, which is the same behaviour as an unstatable one.
func SystemStatfs(string) (FsStat, error) {
	return FsStat{}, errors.New("statfs is only implemented on linux")
}
