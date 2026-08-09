//go:build linux

package collector

import "syscall"

// SystemStatfs is the production StatfsFunc: a real statfs(2) call, plus a
// stat(2) for the device id.
//
// Build-tagged because the agent ships on Linux only, while the test suite and
// developer machines are frequently macOS -- where Statfs_t carries different
// fields. The collector itself is portable; only this syscall pair is not.
//
// Uses syscall rather than golang.org/x/sys/unix deliberately: these two calls
// are all netra needs from it, and the standard library already has them.
func SystemStatfs(mountpoint string) (FsStat, error) {
	var fs syscall.Statfs_t
	if err := syscall.Statfs(mountpoint, &fs); err != nil {
		return FsStat{}, err
	}

	// st_dev comes from stat(2), not from statfs's Fsid. Fsid's layout is
	// architecture-dependent and is not the same value /proc/self/mountinfo
	// or a device number would give, whereas Dev is exactly the st_dev that
	// identifies a filesystem uniquely -- which is what the dedup depends on.
	var st syscall.Stat_t
	if err := syscall.Stat(mountpoint, &st); err != nil {
		return FsStat{}, err
	}

	// Bsize is the block size the counts below are expressed in.
	bs := uint64(fs.Bsize)

	// Bavail rather than Bfree: Bfree includes the blocks reserved for root,
	// which an unprivileged process cannot use. Reporting Bfree would show
	// free space the host cannot actually give out, so a "disk full" alert
	// would fire late.
	return FsStat{
		Total:       fs.Blocks * bs,
		Free:        fs.Bavail * bs,
		InodesTotal: fs.Files,
		InodesFree:  fs.Ffree,
		DeviceID:    uint64(st.Dev),
	}, nil
}
