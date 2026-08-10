package collector

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

// FsStat is one filesystem's statfs result, in the terms the schema uses.
//
// Used and Free are reported INDEPENDENTLY, and on a filesystem with a root
// reserve they do not add up to Total. That is deliberate, and it is what df
// does:
//
//	Total = Blocks           every block on the filesystem
//	Free  = Bavail           what an unprivileged process may still allocate
//	Used  = Blocks - Bfree   what actually holds data
//
// The gap between them is the root reserve -- ext4's default 5% -- which is
// neither used nor available. Deriving one from the other would misreport
// whichever end it was derived from: Total - Free counts the reserve as used
// and overstates consumption by 5% on every ext4 filesystem, while
// Total - Used counts it as free and fires a "disk full" alert late, after
// unprivileged writes have already started failing with ENOSPC.
//
// So a UI must compute its percentage as Used / (Used + Free), the way df
// computes Use%, rather than Used / Total.
type FsStat struct {
	Total       uint64
	Free        uint64
	Used        uint64
	InodesTotal uint64
	InodesFree  uint64
	// DeviceID is st_dev. Bind mounts of one filesystem share it, which is
	// how duplicates are found.
	DeviceID uint64
}

// StatfsFunc reads one mountpoint's usage.
//
// Injected rather than called directly so the collector is testable against
// fixtures: statfs(2) reports whatever the machine running the test happens to
// have mounted, which is not a test.
type StatfsFunc func(mountpoint string) (FsStat, error)

// Filesystems reports usage per mounted filesystem.
//
// Deduplicated by st_dev: the same filesystem mounted twice -- a bind mount,
// or /var and / on one device -- must produce ONE row. Counting both would
// overstate the host's disk usage by however many bind mounts it happens to
// have, and a container host has many.
type Filesystems struct {
	procRoot string
	statfs   StatfsFunc
}

// NewFilesystems builds a Filesystems collector. procRoot supplies the mount
// table (/proc/mounts).
func NewFilesystems(procRoot string, statfs StatfsFunc) *Filesystems {
	return &Filesystems{procRoot: procRoot, statfs: statfs}
}

// Name implements Collector.
func (f *Filesystems) Name() string { return "filesystems" }

// SetProcRootForTest repoints the collector at a different fixture tree.
func (f *Filesystems) SetProcRootForTest(root string) { f.procRoot = root }

// virtualFsTypes are pseudo-filesystems with no storage behind them. Reporting
// them adds a row per kernel interface whose "usage" is meaningless -- tmpfs
// is deliberately NOT here, because a full tmpfs really does break things.
var virtualFsTypes = map[string]bool{
	"proc": true, "sysfs": true, "devpts": true, "cgroup": true,
	"cgroup2": true, "debugfs": true, "tracefs": true, "securityfs": true,
	"pstore": true, "bpf": true, "configfs": true, "fusectl": true,
	"hugetlbfs": true, "mqueue": true, "nsfs": true, "autofs": true,
	"binfmt_misc": true, "rpc_pipefs": true, "selinuxfs": true,
	"devtmpfs": true, "efivarfs": true,
}

// Collect implements Collector.
func (f *Filesystems) Collect(_ context.Context) (*Result, error) {
	mounts, err := f.readMounts()
	if err != nil {
		return nil, err
	}

	ts := time.Now().UnixMilli()
	rows := make([]*netrav1.FilesystemSample, 0, len(mounts))

	// seen holds the st_dev of every filesystem already reported, so a bind
	// mount is skipped rather than counted a second time. A set rather than a
	// map to the winning mountpoint: nothing ever read that mountpoint back.
	seen := make(map[uint64]struct{}, len(mounts))

	for _, m := range mounts {
		st, err := f.statfs(m.mountpoint)
		if err != nil {
			// A mountpoint that cannot be stat'd -- an unreachable NFS
			// server, a mount that vanished between reading the table and
			// this call. Skipping one filesystem must not cost the others.
			continue
		}
		if st.Total == 0 {
			// Nothing behind it. Reporting 0/0 would show as a full disk in
			// any percentage the UI computes.
			continue
		}
		if _, dup := seen[st.DeviceID]; dup {
			continue
		}
		seen[st.DeviceID] = struct{}{}

		row := &netrav1.FilesystemSample{
			TsMs:       ts,
			Label:      m.mountpoint,
			Mountpoint: m.mountpoint,
			DeviceId:   ptrTo(st.DeviceID),
			Total:      ptrTo(st.Total),
			// Used is NOT Total - Free: see FsStat on why the root reserve
			// makes those two different numbers.
			Used:        ptrTo(st.Used),
			Free:        ptrTo(st.Free),
			InodesTotal: ptrTo(st.InodesTotal),
			// read_bytes and write_bytes are deliberately left unset. Per
			// filesystem I/O needs the st_dev -> block device mapping, which
			// is not available here; NULL says "could not attribute I/O to
			// this filesystem", which is a different fact from "did no I/O".
		}

		// Guarded, like every other subtraction in this package. A filesystem
		// reporting a dynamic or synthetic inode count can return f_ffree above
		// f_files, and the unsigned wrap would store a number near 2^64 as
		// though it had been measured. Unset says "could not compute", which is
		// the honest answer.
		if st.InodesTotal >= st.InodesFree {
			row.InodesUsed = ptrTo(st.InodesTotal - st.InodesFree)
		}

		rows = append(rows, row)
	}

	// Deterministic order so failures read the same way twice.
	slices.SortFunc(rows, func(a, b *netrav1.FilesystemSample) int {
		return strings.Compare(a.GetLabel(), b.GetLabel())
	})

	return &Result{Filesystems: rows}, nil
}

// mountEntry is the one field of a /proc/mounts line this collector uses after
// parsing. The device and the filesystem type decide whether a line is kept,
// which readMounts settles as it reads; carrying them further only invited the
// question of what they were for.
type mountEntry struct {
	mountpoint string
}

// readMounts parses /proc/mounts, dropping pseudo-filesystems.
func (f *Filesystems) readMounts() ([]mountEntry, error) {
	path := filepath.Join(f.procRoot, "mounts")
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()

	var out []mountEntry

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 3 {
			continue
		}
		if virtualFsTypes[fields[2]] {
			continue
		}
		// Mountpoints are octal-escaped in /proc/mounts: a space is \040.
		out = append(out, mountEntry{mountpoint: unescapeMount(fields[1])})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	return out, nil
}

// unescapeMount decodes the octal escapes /proc/mounts uses for characters
// that would otherwise break the field split -- most commonly \040 for a
// space, which appears in any mountpoint an operator named with one.
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			var v int
			ok := true
			for _, c := range s[i+1 : i+4] {
				if c < '0' || c > '7' {
					ok = false
					break
				}
				v = v*8 + int(c-'0')
			}
			if ok {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
