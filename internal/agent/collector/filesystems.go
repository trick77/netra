package collector

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
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
	fsMounts map[string]string
	statfs   StatfsFunc

	// warnedEmpty keeps the "nothing to measure" warning to one line rather
	// than one per scrape, forever.
	warnedEmpty bool

	// wedged holds the backoff state of mountpoints whose statfs did not
	// return in time, keyed by target. Same shape and same reasoning as the
	// hwmon tracking in sensors.go -- the two are deliberately not shared yet,
	// because unifying them means changing a collector that works.
	wedged map[string]*wedgedPath

	// scrapes counts Collect calls, which is the clock the backoff is measured
	// in -- a retry is only meaningful when a scrape actually happens.
	scrapes uint64

	// statfsTimeout bounds one statfs call. Always the statfsTimeout constant
	// in production; a field so a test need not spend two seconds per wedged
	// mount to watch the deadline fire.
	statfsTimeout time.Duration
}

// statfsTimeout bounds one statfs call.
//
// statfs(2) is not context-aware and, against a dead NFS/CIFS server or a FUSE
// mount whose userspace daemon has died, blocks in uninterruptible D state
// forever. Because every collector runs serially on the one goroutine that also
// owns the ring and the flush, that stalled the entire agent -- no samples from
// ANY subsystem, no error, nothing but a restart to recover it.
//
// virtualFsTypes has carried a network-filesystem blocklist for exactly this
// hazard, but a blocklist cannot be the guard: it is inevitably incomplete (it
// names nfs, nfs4, cifs, smb3 and fuse.sshfs, and says nothing about
// fuse.rclone, fuse.s3fs, glusterfs, ceph, 9p or lustre), and name() applies it
// only when no marker is present -- so the containerised deployment, where
// setup-agent.sh's --include-network-fs deliberately mounts a network
// filesystem as a marker, was the case it did not cover.
//
// Two seconds is generous for a call that normally returns in microseconds, and
// the cost of exceeding it is one filesystem's row for one scrape.
const statfsTimeout = 2 * time.Second

// NewFilesystems builds a Filesystems collector. procRoot supplies the mount
// table (/proc/mounts); fsMounts maps a marker label to the host mountpoint it
// stands for, and may be nil.
func NewFilesystems(procRoot string, fsMounts map[string]string, statfs StatfsFunc) *Filesystems {
	return &Filesystems{
		procRoot:      procRoot,
		fsMounts:      fsMounts,
		statfs:        statfs,
		statfsTimeout: statfsTimeout,
	}
}

// markerPrefix is where setup-agent.sh bind-mounts the marker files.
//
// The agent never mounts host data. The setup script creates one empty .netra
// marker file per measurable filesystem and bind-mounts it read-only to
// /netra/fs/<label>, so statfs on that target reports the host filesystem
// without the agent being able to read a byte of it.
//
// That path exists ONLY inside this container. It names nothing on the host --
// no directory, no dataset, no mount -- so it must never reach a Label, a
// Mountpoint, or anything else that leaves this collector. Reporting it is the
// bug this prefix exists to prevent: an operator reading "/netra/fs/ark is 94%
// full" is being shown the inside of the agent instead of their own disk.
const markerPrefix = "/netra/fs/"

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

	// overlay and squashfs are the container's own image layers, not host
	// storage: reporting overlay meant the agent listed a filesystem at "/"
	// whose size was Docker's storage driver, alongside the host root that
	// arrived properly through a marker. Its anonymous st_dev never dedupes
	// against anything, so it survived every other guard here.
	"overlay": true, "squashfs": true, "ramfs": true,

	// Network filesystems, matching isnet() in setup-agent.sh: a dead server
	// makes statfs block, and the scrape budget cannot absorb that.
	"nfs": true, "nfs4": true, "cifs": true, "smb3": true, "fuse.sshfs": true,
}

// statfsDeadlined calls statfs on its own goroutine and gives up on it after
// statfsTimeout.
//
// The goroutine is deliberately abandoned rather than cancelled, because a
// D-state syscall cannot be cancelled -- not by a context, not by a signal. It
// unblocks if and when the kernel lets it, and the buffered channel inside
// deadlined lets it send and exit even though nothing is waiting. Stranding it
// is the price of keeping the scrape loop alive, and skipWedged is what keeps
// that price bounded: without the backoff, a permanently dead mount would
// strand one goroutine per scrape for the life of the agent.
func (f *Filesystems) statfsDeadlined(ctx context.Context, target string) (FsStat, error) {
	if f.skipWedged(target) {
		return FsStat{}, errWedged
	}
	// No budget left, so nothing is started. deadlined would otherwise launch
	// the statfs anyway and abandon it at once -- on a scrape that keeps
	// running out of time (a slow collector ahead of this one), a dead mount
	// would strand one goroutine EVERY scrape while never being marked wedged,
	// since markWedged deliberately skips an expired scrape. That is exactly
	// the unbounded accumulation the backoff exists to prevent. It also makes
	// the healthy mounts deterministic: with an already-done context both
	// select cases are ready, so deadlined would return a value or a deadline
	// error at random.
	if err := ctx.Err(); err != nil {
		return FsStat{}, err
	}

	st, err := deadlined(ctx, f.statfsTimeout, func() (FsStat, error) {
		return f.statfs(target)
	})
	if errors.Is(err, context.DeadlineExceeded) {
		// Only if THIS call's own deadline is what expired. deadlined derives
		// its context from the scrape's, so a scrape that has already run out
		// of budget -- or is being torn down -- returns DeadlineExceeded here
		// for every remaining mountpoint. Marking those would back off healthy
		// filesystems because some earlier collector was slow, and a marker on
		// a busy host would drift into a seventeen-hour cadence having never
		// once blocked.
		if ctx.Err() == nil {
			f.markWedged(target)
		}
		return FsStat{}, err
	}
	// Cleared on any outcome that was not a timeout, including an error: a
	// mount that returns ENOENT promptly is not wedged, it is gone.
	f.clearWedged(target)

	return st, err
}

// skipWedged reports whether this target is still inside its backoff window.
func (f *Filesystems) skipWedged(target string) bool {
	w := f.wedged[target]
	return w != nil && f.scrapes < w.retryAt
}

// markWedged records a statfs that did not return in time and schedules when
// the mountpoint may be tried again.
func (f *Filesystems) markWedged(target string) {
	if f.wedged == nil {
		f.wedged = make(map[string]*wedgedPath)
	}

	w := f.wedged[target]
	if w == nil {
		w = &wedgedPath{}
		f.wedged[target] = w
	}
	w.failures++

	backoff := uint64(1) << min(w.failures-1, wedgedBackoffShifts)
	w.retryAt = f.scrapes + backoff

	slog.Warn("statfs timed out; backing off this mountpoint",
		"mountpoint", target, "timeout", f.statfsTimeout,
		"failures", w.failures, "skipping_scrapes", backoff)
}

// clearWedged forgets a target's backoff once it answers again, so a server
// that comes back is not held at a seventeen-hour cadence forever.
func (f *Filesystems) clearWedged(target string) {
	if f.wedged[target] != nil {
		slog.Info("mountpoint recovered", "mountpoint", target)
		delete(f.wedged, target)
	}
}

// Collect implements Collector.
func (f *Filesystems) Collect(ctx context.Context) (*Result, error) {
	f.scrapes++

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
		st, err := f.statfsDeadlined(ctx, m.target)
		if err != nil {
			// A mountpoint that cannot be stat'd -- an unreachable NFS
			// server, a mount that vanished between reading the table and
			// this call, or one whose statfs blocked past statfsTimeout.
			// Skipping one filesystem must not cost the others.
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
			Label:      m.label,
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

	// The scrape ran out of budget before this collector measured anything.
	// Reported as the error it is: every per-mount failure above is skipped
	// with a continue, so an expired scrape otherwise returned an empty Result
	// and a nil error, which collect() records as ok -- the "silently missing"
	// state the whole deadline exists to replace, in the collector it was built
	// for. errorCode maps this to "timeout". Guarded on having measured
	// nothing, so a partial scrape keeps the rows it did take, and it stays
	// ahead of warnedEmpty: that latch fires once for the life of the process,
	// and spending it on a timeout would suppress the genuine
	// nothing-is-mounted warning forever.
	if len(rows) == 0 && ctx.Err() != nil {
		return nil, ctx.Err()
	}

	if len(rows) == 0 && !f.warnedEmpty {
		f.warnedEmpty = true
		slog.Warn("no filesystem could be measured; disk usage and inode metrics will be missing",
			"marker_prefix", markerPrefix,
			"hint", "re-run setup-agent.sh: it bind-mounts one .netra marker per filesystem under "+markerPrefix)
	}

	// Deterministic order so failures read the same way twice.
	slices.SortFunc(rows, func(a, b *netrav1.FilesystemSample) int {
		return strings.Compare(a.GetLabel(), b.GetLabel())
	})

	return &Result{Filesystems: rows}, nil
}

// mountEntry is what one kept /proc/mounts line becomes. The device and the
// filesystem type decide whether a line is kept, which readMounts settles as
// it reads; carrying them further only invited the question of what they were
// for.
//
// target is where the agent must call statfs. label and mountpoint are what
// the host calls the filesystem, and are the only two that leave this package.
// On a containerised agent they differ from target by construction -- that is
// the whole point of the marker scheme.
type mountEntry struct {
	target     string
	label      string
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

	var lines []mountLine

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 3 {
			continue
		}
		// Mountpoints are octal-escaped in /proc/mounts: a space is \040.
		//
		// The fstype is carried rather than acted on here, because whether it
		// disqualifies a mount depends on whether markers are in play at all.
		lines = append(lines, mountLine{target: unescapeMount(fields[1]), fstype: fields[2]})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	return f.name(lines), nil
}

// mountLine is one /proc/mounts row, before anything has decided what to do
// with it.
type mountLine struct {
	target string
	fstype string
}

// name turns raw mount targets into the entries the hub is told about.
//
// When any marker mount is present the rest of the table is dropped, rather
// than filtered again here. setup-agent.sh already decided which filesystems
// are worth measuring -- it rejected network shares, snap loopbacks,
// /var/lib/docker, shadowed mounts and duplicate maj:min, with a reason for
// each -- and re-deriving that decision from inside the container produces a
// worse answer than the one already rendered into the compose file.
//
// With no marker present the whole table is used as-is, minus the
// pseudo-filesystems: an agent running directly on a host, and the
// fixture-driven tests, both depend on that.
//
// virtualFsTypes is applied ONLY there, for the same reason. A marker's
// /proc/mounts line carries the underlying filesystem's type, so an operator
// who passed --include-network-fs has an nfs4 marker mounted on purpose --
// setup accepted it, told them so, and rendered the bind. Re-checking the
// fstype here would drop it silently, second-guessing a decision this
// collector cannot see the inputs to.
func (f *Filesystems) name(lines []mountLine) []mountEntry {
	markers := false
	for _, l := range lines {
		if strings.HasPrefix(l.target, markerPrefix) {
			markers = true
			break
		}
	}

	out := make([]mountEntry, 0, len(lines))
	for _, l := range lines {
		t := l.target
		if !markers {
			if virtualFsTypes[l.fstype] {
				continue
			}
			// Belt and braces on the invariant this whole file exists for.
			// markerPrefix has a trailing slash, so a bind of the marker
			// DIRECTORY itself (/netra/fs, no label under it) does not turn
			// markers on -- and would then be reported here, verbatim, as a
			// filesystem named after the inside of the container. Nothing on a
			// host is called /netra, so dropping the whole subtree costs a real
			// install nothing.
			if strings.HasPrefix(t, "/netra/") || t == "/netra" {
				continue
			}
			out = append(out, mountEntry{target: t, label: t, mountpoint: t})
			continue
		}
		if !strings.HasPrefix(t, markerPrefix) {
			continue
		}
		// Stripped FIRST, so every path out of here -- including the fallback
		// below -- is already free of the container-internal prefix.
		label := strings.TrimPrefix(t, markerPrefix)
		mountpoint := f.fsMounts[label]
		if mountpoint == "" {
			// An agent upgraded ahead of its .env: AGENT_FS_MOUNTS is not
			// there yet. Reporting the bare label is imperfect but honest;
			// it is a name the operator recognises, and the next setup run
			// replaces it with the real mountpoint.
			mountpoint = label
		}
		out = append(out, mountEntry{target: t, label: label, mountpoint: mountpoint})
	}
	return out
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
