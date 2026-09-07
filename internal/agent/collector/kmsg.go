package collector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
)

// Kmsg turns the kernel ring buffer into events.
//
// It exists because the event log had three producers -- mdraid, packages,
// systemd units -- and on a host whose packages rarely change and whose units
// do not flap, that leaves mdraid alone. Meanwhile the things an operator
// actually wants to be told about live in the kernel's own log and nowhere
// else: a disk throwing UNC errors, a filesystem remounting read-only, the OOM
// killer, an MCE, a thermal trip.
//
// WHAT IT DOES NOT DO, and both are deliberate:
//
// It does not read journald. Measured across three real hosts, priority
// warning+ over seven days was 272 sshd lines from internet scanners, 239
// containerd "left-over process" lines and 39 firewalld iptables complaints --
// 564 lines and no signal. Ingesting that would have replaced one kind of
// noise with a louder one.
//
// It does not filter by priority either. The top of every surveyed host's ring
// buffer is Docker veth churn ("br-x: port 3(veth9) entered disabled state",
// hundreds of lines), while real trouble is spread across priorities -- the md
// scrub lines are KERN_INFO. So the classifier is an ALLOWLIST: a record
// matching no pattern is dropped and counted. That makes the noise question
// moot, because veth churn simply never matches, and it means a new event type
// is a table entry rather than a new filter.
type Kmsg struct {
	devPath  string
	procRoot string

	// src is the open device, held across scrapes so the read position
	// survives: reopening would seek somewhere and re-import records that were
	// already reported.
	src KmsgSource
	// openErr latches why the device could not be opened, which is what the
	// capability reports. Retried on every scrape, so an operator who adds the
	// device grant gets events without the agent needing to know it changed.
	openErr error

	// suppress holds the cross-tick rate limit, keyed by what an event is
	// ABOUT rather than by its text. See foldKey.
	suppress map[foldKey]*suppression

	// gaps counts EPIPE responses -- records the kernel overwrote before this
	// collector read them, because the host produced them faster than one
	// scrape interval could drain them. Logged, never emitted as an event: a
	// dropped record is the absence of information, not an incident, and a row
	// saying "something happened, unknown what" is not actionable.
	gaps int

	// newSource, uptime and now are the seams the tests replace. Everything
	// else here is pure enough to test directly; these are the only parts that
	// touch a device, a procfs file and the clock.
	newSource func() (KmsgSource, error)
	uptime    func() (time.Duration, error)
	now       func() time.Time
}

// Reading and folding bounds.
const (
	// kmsgReadBuf must hold a WHOLE record: /dev/kmsg returns one record per
	// read() and fails with EINVAL rather than truncating when the buffer is
	// too short. The kernel's own record limit is under 8 KiB including the
	// continuation lines.
	kmsgReadBuf = 8192

	// kmsgMaxRecords bounds one drain, so a host that has just filled its ring
	// buffer cannot spend a whole scrape interval parsing it.
	kmsgMaxRecords = 2000

	// kmsgMaxEvents bounds what one scrape may emit. The agent posts through a
	// fixed-size in-memory ring; a screaming host must not push every other
	// collector's samples out of it.
	kmsgMaxEvents = 50

	// kmsgSuppressWindow is how long one (type, subject) stays quiet after
	// speaking. A failing disk emits for hours -- one surveyed host's ring
	// holds four ATA exceptions inside eleven seconds -- and the second
	// identical row tells an operator nothing the first did not. The count is
	// kept and reported, so nothing is hidden, only folded.
	kmsgSuppressWindow = 10 * time.Minute
)

// errKmsgGap is the EPIPE case: records were overwritten before being read.
var errKmsgGap = errors.New("kmsg: records overwritten")

// NewKmsg builds a Kmsg collector reading devPath (normally "/dev/kmsg"), with
// procRoot supplying /proc/uptime for the timestamp anchor.
func NewKmsg(devPath, procRoot string) *Kmsg {
	k := &Kmsg{
		devPath:  devPath,
		procRoot: procRoot,
		suppress: make(map[foldKey]*suppression),
		now:      time.Now,
	}
	k.newSource = func() (KmsgSource, error) { return openKmsgDevice(k.devPath) }
	k.uptime = func() (time.Duration, error) { return readUptime(k.procRoot) }
	return k
}

// Name implements Collector.
func (k *Kmsg) Name() string { return "kmsg" }

// EmitsBaseline implements BaselineEmitter, and returns FALSE on purpose.
//
// Unlike mdraid and systemd, this collector has no state to report on arrival.
// It seeks to the END of the ring buffer when it opens -- see openKmsgDevice --
// so its first Collect has nothing to say by construction, and the agent's
// priming discarding an empty Result costs nothing. Saying true would ask the
// agent to preserve a Result that is always empty.
func (k *Kmsg) EmitsBaseline() bool { return false }

// Kmsg deliberately does NOT implement InventoryResender.
//
// That re-arm exists for collectors whose last event IS the state, so a dropped
// scrape leaves the hub serving a stale one forever. This collector has no such
// state: a kernel record is a point in time, the agent is stateless, and the
// records behind a dropped scrape have already been read past. There is nothing
// to re-report, and a ResendInventory that re-read the ring would replay
// history as fresh events -- precisely what the opening seek exists to prevent.

// SetSourceForTest replaces the device, the uptime anchor and the clock.
func (k *Kmsg) SetSourceForTest(
	open func() (KmsgSource, error),
	uptime func() (time.Duration, error),
	now func() time.Time,
) {
	k.newSource, k.uptime, k.now = open, uptime, now
	k.src, k.openErr = nil, nil
}

// Capabilities implements CapabilityReporter.
func (k *Kmsg) Capabilities() map[string]string {
	switch {
	case k.openErr == nil:
		return nil
	case errors.Is(k.openErr, os.ErrNotExist):
		// No device node in the container at all: the operator did not grant
		// it. Distinct from a refusal, because the remedy differs.
		return map[string]string{"kmsg": "unavailable"}
	case errors.Is(k.openErr, os.ErrPermission):
		// TWO causes, and the errno cannot separate them. /dev/kmsg is char
		// device 1:11 and is not on Docker's default device cgroup allowlist,
		// so a bind mount without a `devices:` entry is refused by
		// chrdev_open; and check_syslog_permissions refuses the open outright
		// under kernel.dmesg_restrict=1 without CAP_SYSLOG. Both are EPERM.
		// One value names the state and the compose file documents both
		// remedies, rather than this guessing which applies.
		return map[string]string{"kmsg": "denied"}
	default:
		return map[string]string{"kmsg": "error"}
	}
}

// StartupSummary implements StartupSummarizer.
func (k *Kmsg) StartupSummary() string {
	if k.openErr != nil {
		return fmt.Sprintf("%s unreadable (%v)", k.devPath, k.openErr)
	}
	return fmt.Sprintf("reading %s, %d patterns classified", k.devPath, len(kmsgClasses))
}

// Collect implements Collector.
//
// Never returns an error for an unreadable device. A host that declines the
// device grant is a supported deployment rather than a fault, and failing here
// would discard every other collector's contribution to the same scrape.
func (k *Kmsg) Collect(_ context.Context) (*Result, error) {
	if k.src == nil {
		src, err := k.newSource()
		if err != nil {
			k.openErr = err
			return &Result{}, nil
		}
		k.src, k.openErr = src, nil
	}

	records := k.drain()

	// The anchor is read AFTER the drain and applied to every record in it.
	// Computing a boot time once at startup and adding each record's monotonic
	// stamp to it accumulates the drift of the kernel's clock against wall
	// time for as long as the agent runs -- which is why `dmesg -T` is
	// documented as inaccurate, and on a host with a 1200-day uptime it is off
	// by the better part of an hour. Every record in a drain is at most one
	// scrape old, so anchoring per drain bounds the error to one interval's
	// worth of drift.
	now := k.now()
	up, err := k.uptime()
	if err != nil {
		// No anchor available. The records are still real, so they are stamped
		// with the scrape time rather than dropped: a slightly late event beats
		// no event.
		up = 0
	}

	return &Result{Events: k.eventsFor(records, now, up)}, nil
}

// drain reads whole records until the device says there are no more.
func (k *Kmsg) drain() []kmsgRecord {
	var out []kmsgRecord

	// Gaps count against the same budget as records, and must. An EPIPE
	// consumes no record, so a `continue` that did not count would spin
	// forever on a ring wrapping faster than one scrape can drain it -- and
	// the agent runs its collectors sequentially in one goroutine, so that is
	// not one stuck collector, it is the scrape loop never coming back.
	reads := 0

	for reads < kmsgMaxRecords {
		reads++
		raw, err := k.src.ReadRecord()
		switch {
		case err == nil:
		case errors.Is(err, io.EOF):
			// Nothing further to read right now: the normal end of a drain.
			return out
		case errors.Is(err, errKmsgGap):
			// The ring wrapped past this reader. The fd REPOSITIONS ITSELF --
			// per Documentation/ABI/testing/dev-kmsg one read returns EPIPE and
			// the next returns the oldest surviving record -- so the correct
			// response is to count the gap and keep reading.
			//
			// Reopening and seeking would be actively wrong. SEEK_DATA lands
			// after the last SYSLOG_ACTION_CLEAR, which on a ring nobody has
			// cleared is the START of the buffer: replaying the whole history
			// as fresh events, which is what the opening seek prevents.
			k.gaps++
			slog.Warn("kmsg records overwritten before they were read",
				"device", k.devPath, "gaps_total", k.gaps)
			continue
		default:
			// Anything else: drop the handle so the next scrape reopens, and
			// report what was read up to here.
			_ = k.src.Close()
			k.src, k.openErr = nil, err
			return out
		}

		if rec, ok := parseKmsgRecord(raw); ok {
			out = append(out, rec)
		}
	}

	return out
}

// kmsgRecord is one parsed line of the ring buffer.
type kmsgRecord struct {
	// Priority is the syslog priority byte: facility*8 + level. Carried into
	// the detail because it is cheap and occasionally settles an argument, but
	// never used to filter -- see the type comment.
	Priority int
	Seq      uint64
	// Mono is the record's own timestamp, measured from boot.
	Mono    time.Duration
	Message string
}

// parseKmsgRecord parses one record.
//
// The format is "prio,seq,usec,flag[,extras];message", optionally followed by
// continuation lines beginning with a space that carry key=value metadata. Only
// the first line is kept: the extras name a subsystem and a device path, and
// the classifier below recovers both from the message itself for every record
// it cares about.
func parseKmsgRecord(raw []byte) (kmsgRecord, bool) {
	line := string(raw)
	// Continuation lines go with the record they belong to; taking only up to
	// the first newline is what drops them.
	if nl := strings.IndexByte(line, '\n'); nl >= 0 {
		line = line[:nl]
	}

	semi := strings.IndexByte(line, ';')
	if semi < 0 {
		return kmsgRecord{}, false
	}
	fields := strings.Split(line[:semi], ",")
	if len(fields) < 3 {
		return kmsgRecord{}, false
	}

	prio, err := strconv.Atoi(strings.TrimSpace(fields[0]))
	if err != nil {
		return kmsgRecord{}, false
	}
	seq, err := strconv.ParseUint(strings.TrimSpace(fields[1]), 10, 64)
	if err != nil {
		return kmsgRecord{}, false
	}
	usec, err := strconv.ParseInt(strings.TrimSpace(fields[2]), 10, 64)
	if err != nil {
		return kmsgRecord{}, false
	}

	msg := strings.TrimSpace(line[semi+1:])
	if msg == "" {
		return kmsgRecord{}, false
	}

	return kmsgRecord{
		Priority: prio,
		Seq:      seq,
		Mono:     time.Duration(usec) * time.Microsecond,
		Message:  unescapeKmsg(msg),
	}, true
}

// unescapeKmsg undoes the kernel's \xNN escaping of bytes outside printable
// ASCII. Device names and error strings are ASCII in practice, so this exists
// so that an escaped byte cannot make a message unreadable, not because it is
// load-bearing.
func unescapeKmsg(s string) string {
	if !strings.Contains(s, `\x`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == '\\' && i+3 < len(s) && s[i+1] == 'x' {
			if v, err := strconv.ParseUint(s[i+2:i+4], 16, 8); err == nil {
				b.WriteByte(byte(v))
				i += 4
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// readUptime returns how long the host has been up, from /proc/uptime.
func readUptime(procRoot string) (time.Duration, error) {
	data, err := os.ReadFile(filepath.Join(procRoot, "uptime"))
	if err != nil {
		return 0, err
	}
	first, _, _ := strings.Cut(strings.TrimSpace(string(data)), " ")
	secs, err := strconv.ParseFloat(first, 64)
	if err != nil {
		return 0, fmt.Errorf("parse uptime %q: %w", first, err)
	}
	return time.Duration(secs * float64(time.Second)), nil
}

// KmsgSource yields whole records, one per call.
//
// Exported because it is the seam the tests and the simulator stand in on --
// see NewSliceSource -- and an unexported interface cannot be named from
// either.
type KmsgSource interface {
	// ReadRecord returns the next record. io.EOF means the drain is finished
	// for now -- the device is non-blocking and has nothing more to give --
	// and errKmsgGap means records were overwritten before they were read.
	ReadRecord() ([]byte, error)
	Close() error
}

// deviceSource reads the real /dev/kmsg.
//
// It holds a raw file descriptor and calls syscall.Read rather than wrapping an
// *os.File, and that is not a style choice. os.OpenFile registers a pollable fd
// with the runtime poller, which turns EAGAIN into a WAIT -- so a drain of a
// quiet ring buffer would block the whole scrape until the next kernel message
// arrived, which on a healthy host could be days.
type deviceSource struct {
	fd  int
	buf []byte
}

// openKmsgDevice opens the ring buffer and seeks past everything already in it.
//
// The seek is not optional. The ring is not cleared on read and survives for as
// long as the host is up: one surveyed host had 1206 days of uptime and its
// buffer still held ATA errors from December 2023. Without SEEK_END the first
// scrape after an agent start would import years of history as events that all
// happened just now.
func openKmsgDevice(path string) (KmsgSource, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	if _, err := syscall.Seek(fd, 0, io.SeekEnd); err != nil {
		_ = syscall.Close(fd)
		return nil, &os.PathError{Op: "seek", Path: path, Err: err}
	}
	return &deviceSource{fd: fd, buf: make([]byte, kmsgReadBuf)}, nil
}

// ReadRecord implements KmsgSource.
func (d *deviceSource) ReadRecord() ([]byte, error) {
	n, err := syscall.Read(d.fd, d.buf)
	if err != nil {
		switch {
		case errors.Is(err, syscall.EAGAIN):
			return nil, io.EOF
		case errors.Is(err, syscall.EPIPE):
			return nil, errKmsgGap
		case errors.Is(err, syscall.EINTR):
			// A signal arrived mid-read. Nothing was consumed, so ending the
			// drain here simply defers the record to the next scrape.
			return nil, io.EOF
		default:
			return nil, err
		}
	}
	if n <= 0 {
		return nil, io.EOF
	}
	return d.buf[:n], nil
}

// Close implements KmsgSource.
func (d *deviceSource) Close() error { return syscall.Close(d.fd) }

// sliceSource serves pre-split records, for tests and for the simulator.
type sliceSource struct {
	records [][]byte
	gapAt   int // index at which to return errKmsgGap once; -1 for never
	i       int
	gapDone bool
}

// NewSliceSource builds a KmsgSource over records already in memory. gapAt is
// the index at which one errKmsgGap is returned, mimicking a ring that wrapped
// past the reader; -1 never returns one.
func NewSliceSource(records [][]byte, gapAt int) KmsgSource {
	return &sliceSource{records: records, gapAt: gapAt}
}

// ReadRecord implements KmsgSource.
func (s *sliceSource) ReadRecord() ([]byte, error) {
	if s.gapAt >= 0 && !s.gapDone && s.i == s.gapAt {
		s.gapDone = true
		return nil, errKmsgGap
	}
	if s.i >= len(s.records) {
		return nil, io.EOF
	}
	rec := s.records[s.i]
	s.i++
	return rec, nil
}

// Close implements KmsgSource.
func (s *sliceSource) Close() error { return nil }

// kmsgClass is one entry of the allowlist.
type kmsgClass struct {
	// Type is the event type stored in the events table, and the word the UI
	// offers in its type filter.
	Type string
	// Severity is what the emitter states about itself, carried in the detail.
	// Both event views read detail.severity before anything else, so stating
	// it here is what makes a kernel event render as critical without either
	// of them growing a table of kernel words.
	Severity string
	// re must define a named group "subj" whenever the event is about a
	// nameable thing -- a device, an array, a process. Absent, the event is
	// about the host as a whole and the subject is stored NULL.
	re *regexp.Regexp
}

// kmsgClasses is the allowlist, in match order: the first hit wins, so a
// narrower pattern must precede a broader one.
//
// Every pattern here was written against real output from three surveyed hosts
// or from the kernel source that emits it. The table is deliberately short:
// each entry is a promise that a row carrying that type means what its name
// says, and a pattern nobody can name an incident for is a row an operator
// learns to scroll past.
var kmsgClasses = []kmsgClass{
	// --- Storage: the block layer's own complaint -------------------------
	{
		Type:     "disk_error",
		Severity: "critical",
		re: regexp.MustCompile(
			`^(?:blk_update_request|print_req_error): (?:critical (?:medium|target|nexus) error|I/O error), dev (?P<subj>[a-zA-Z0-9_.-]+), sector`),
	},
	{
		Type:     "disk_error",
		Severity: "critical",
		re:       regexp.MustCompile(`^Buffer I/O error on dev (?P<subj>[a-zA-Z0-9_.-]+),`),
	},

	// --- Storage: the transport ------------------------------------------
	//
	// "configured for UDMA/133" and the other ata lines a healthy boot emits
	// are excluded by naming the failure verbs rather than the ata prefix.
	{
		Type:     "ata_error",
		Severity: "critical",
		re: regexp.MustCompile(
			`^(?P<subj>ata[0-9]+(?:\.[0-9]+)?): (?:exception Emask|SError:|failed command:|error: \{|hard resetting link|COMRESET failed|device reported invalid CHS)`),
	},
	{
		Type:     "nvme_error",
		Severity: "critical",
		re: regexp.MustCompile(
			`^nvme (?P<subj>nvme[0-9]+n?[0-9]*): (?:I/O .*\btimeout\b|resetting controller|Device not ready|Removing after probe failure|controller is down)`),
	},
	{
		Type:     "scsi_error",
		Severity: "critical",
		re: regexp.MustCompile(
			`^sd [0-9:]+: \[(?P<subj>sd[a-z]+)\].*(?:Unrecovered read error|Medium Access Timeout|Critical Medium Error|Medium Error|FAILED Result|timing out command)`),
	},

	// --- md: only the failures ---------------------------------------------
	//
	// The scrub is NOT classified here, and that is a decision rather than an
	// omission. The mdraid collector already emits one event when sync_action
	// leaves idle and one when it returns, from sysfs, needing no device grant
	// at all -- that is the whole scrub story. Classifying "md: data-check of
	// RAID array md3" and its "done" line too would put four rows on the page
	// for one scrub.
	//
	// The failures stay, because sysfs cannot express them: `degraded` is a
	// COUNT, and only the kernel line says which disk it was.
	{
		Type:     "md_fail",
		Severity: "critical",
		re: regexp.MustCompile(
			`^md/raid[0-9]*:(?P<subj>md[0-9]+): (?:Disk failure on|Operation continuing on|Cannot continue)`),
	},
	{
		Type:     "md_fail",
		Severity: "critical",
		re:       regexp.MustCompile(`^md: kicking non-fresh (?P<subj>[a-zA-Z0-9_.-]+) from array`),
	},
	{
		Type:     "md_fail",
		Severity: "critical",
		re:       regexp.MustCompile(`^md: super_written gets error=`),
	},

	// --- Filesystems -------------------------------------------------------
	{
		Type:     "fs_error",
		Severity: "critical",
		re:       regexp.MustCompile(`^EXT4-fs error \(device (?P<subj>[^)]+)\)`),
	},
	{
		Type:     "fs_error",
		Severity: "critical",
		re: regexp.MustCompile(
			`^EXT4-fs \((?P<subj>[^)]+)\): (?:.*Remounting filesystem read-only|error count|failed to convert|Delayed block allocation failed)`),
	},
	{
		Type:     "fs_error",
		Severity: "critical",
		re: regexp.MustCompile(
			`^XFS \((?P<subj>[^)]+)\): (?:Corruption|Internal error|metadata I/O error|log I/O error|Unmount and run xfs_repair|Metadata corruption)`),
	},
	{
		Type:     "fs_error",
		Severity: "critical",
		re:       regexp.MustCompile(`^BTRFS (?:error|critical) \(device (?P<subj>[^)]+)\)`),
	},
	{
		Type:     "fs_error",
		Severity: "critical",
		re: regexp.MustCompile(
			`^FAT-fs \((?P<subj>[^)]+)\): (?:Volume was not properly unmounted|FAT read failed|Directory bread)`),
	},

	// --- Memory pressure ---------------------------------------------------
	{
		Type:     "oom_kill",
		Severity: "critical",
		re: regexp.MustCompile(
			`^(?:Memory cgroup out of memory|Out of memory): Killed process [0-9]+ \((?P<subj>[^)]+)\)`),
	},
	{
		Type:     "oom_kill",
		Severity: "critical",
		re:       regexp.MustCompile(`^oom-kill:.*,task=(?P<subj>[^,]+)`),
	},

	// --- Hardware ----------------------------------------------------------
	{
		Type:     "hw_error",
		Severity: "critical",
		re:       regexp.MustCompile(`^mce: \[Hardware Error\]`),
	},
	{
		Type:     "hw_error",
		Severity: "critical",
		re:       regexp.MustCompile(`^EDAC [A-Za-z0-9]+[0-9]*: [0-9]+ (?:CE|UE) `),
	},
	{
		Type:     "hw_error",
		Severity: "critical",
		re:       regexp.MustCompile(`^Machine check events logged`),
	},
	{
		Type:     "thermal",
		Severity: "warning",
		re: regexp.MustCompile(
			`^(?:mce: )?(?P<subj>CPU[0-9]+): (?:Core|Package) temperature above threshold`),
	},
	{
		Type:     "thermal",
		Severity: "warning",
		re:       regexp.MustCompile(`^thermal (?P<subj>thermal_zone[0-9]+): critical temperature reached`),
	},

	// --- The kernel itself -------------------------------------------------
	{
		Type:     "kernel_fault",
		Severity: "critical",
		re: regexp.MustCompile(
			`^(?:Kernel panic|BUG: unable to handle|BUG: kernel NULL pointer|general protection fault|watchdog: BUG: soft lockup|Oops[: ]|Internal error: Oops)`),
	},

	// --- Link state --------------------------------------------------------
	//
	// Last, because its subject pattern is the loosest in the table and a
	// narrower entry above must get the first look.
	{
		Type:     "link_change",
		Severity: "info",
		re:       regexp.MustCompile(`(?:^|\s)(?P<subj>[a-zA-Z0-9._-]+): (?:NIC )?[Ll]ink is (?:Down|Up)`),
	},
	{
		Type:     "link_change",
		Severity: "info",
		re:       regexp.MustCompile(`^IPv6: ADDRCONF\(NETDEV_CHANGE\): (?P<subj>[a-zA-Z0-9._-]+): link becomes ready`),
	},
}

// virtualIface matches the interface names a container runtime creates and
// destroys as a matter of routine.
//
// These are why link_change needs an exclusion where nothing else in the table
// does: on every surveyed Docker host the veth and bridge churn IS the ring
// buffer, hundreds of lines of "entered disabled state" and "renamed from
// eth0". They are real link changes and they are worth nothing -- a container
// started. The physical interfaces they are mixed in with are worth a lot.
var virtualIface = regexp.MustCompile(`^(?:veth|br-|docker[0-9]|virbr|tap|tun|vnet|lo$)`)

// classifyKmsg maps a record onto the allowlist, or reports that nothing
// matched.
func classifyKmsg(rec kmsgRecord) (typ, subject, severity string, ok bool) {
	for _, c := range kmsgClasses {
		m := c.re.FindStringSubmatch(rec.Message)
		if m == nil {
			continue
		}

		if i := c.re.SubexpIndex("subj"); i > 0 && i < len(m) {
			subject = m[i]
		}

		if c.Type == "link_change" && virtualIface.MatchString(subject) {
			// Matched, and deliberately dropped rather than falling through to
			// a later pattern: a veth's link state is not an event, and no
			// other entry would describe it any better.
			return "", "", "", false
		}

		return c.Type, subject, c.Severity, true
	}
	return "", "", "", false
}

// foldKey is what makes two records the same event.
//
// Type and subject, never the message: a failing disk emits a dozen different
// sentences about one incident -- exception, failed command, status, error --
// and folding on the text would report each as its own thing. What an operator
// needs to know is that sdd is throwing errors, once, with a count.
type foldKey struct {
	Type    string
	Subject string
}

// suppression is one key's cross-tick rate limit.
type suppression struct {
	// until is when this key may speak again.
	until time.Time
	// held counts records folded away while suppressed, reported on the next
	// event so the fold is visible rather than silent.
	held int
	// message is the most recent sample seen while suppressed, so a rollup
	// emitted with no new record still says something concrete.
	message string
	// severity and priority belong to that sample, for the same reason.
	severity string
	priority int
	// last is when the most recent held record happened.
	last time.Time
}

// kmsgDetail is what lands in events.detail.
type kmsgDetail struct {
	// Severity is read by both event views before anything else, which is what
	// makes a kernel event render correctly with no new severity logic in
	// either of them.
	Severity string `json:"severity"`
	Message  string `json:"message"`
	Priority int    `json:"priority"`
	// Count is the number of records folded into this event within one scrape,
	// omitted when it is the uninteresting 1.
	Count int `json:"count,omitempty"`
	// Suppressed is how many further records this key produced during the
	// preceding quiet window. Omitted when none, so an ordinary event carries
	// no evidence of machinery.
	Suppressed int `json:"suppressed,omitempty"`
}

// eventsFor folds a drain into events.
//
// Two levels of folding, and both are needed. WITHIN a scrape, every record
// about one (type, subject) becomes one event carrying a count -- one surveyed
// host's ring holds four ATA exceptions and four block-layer errors inside
// eleven seconds, and a dying disk sustains that for hours. ACROSS scrapes, a
// key that has just spoken stays quiet for kmsgSuppressWindow and reports what
// it withheld when it speaks again.
func (k *Kmsg) eventsFor(records []kmsgRecord, now time.Time, up time.Duration) []*netrav1.Event {
	folds := make(map[foldKey]*suppression)
	order := make([]foldKey, 0, len(records))

	for _, rec := range records {
		typ, subject, severity, ok := classifyKmsg(rec)
		if !ok {
			continue
		}

		key := foldKey{Type: typ, Subject: subject}
		ts := kmsgWallClock(rec.Mono, now, up)

		f, seen := folds[key]
		if !seen {
			f = &suppression{message: rec.Message, severity: severity, priority: rec.Priority, last: ts}
			folds[key] = f
			order = append(order, key)
		}
		f.held++
		f.last = ts
	}

	// Deterministic order, so a scrape carrying two incidents emits them the
	// same way twice -- the same reason mdraid sorts its array names.
	slices.SortFunc(order, func(a, b foldKey) int {
		if a.Type != b.Type {
			return strings.Compare(a.Type, b.Type)
		}
		return strings.Compare(a.Subject, b.Subject)
	})

	var events []*netrav1.Event

	for _, key := range order {
		f := folds[key]

		// Past the cap, a key is SUPPRESSED rather than dropped. `break` here
		// discarded the remaining folds outright -- no event, and no
		// suppression entry either, so their count never surfaced in a later
		// rollup. A wide incident touching more than kmsgMaxEvents devices at
		// once would lose records silently, which is the one thing the folding
		// is documented not to do.
		if len(events) >= kmsgMaxEvents {
			k.hold(key, f, now)
			continue
		}

		if s, quiet := k.suppress[key]; quiet && now.Before(s.until) {
			// Still inside this key's quiet window: keep the count and the
			// newest sample, say nothing.
			k.hold(key, f, now)
			continue
		}

		carried := 0
		if s, ok := k.suppress[key]; ok {
			carried = s.held
		}

		events = append(events, k.event(key, f, carried, f.last))
		k.suppress[key] = &suppression{until: now.Add(kmsgSuppressWindow)}
	}

	// Windows that expired holding records nobody has spoken for since. Without
	// this the tail of an incident is lost: a disk that threw fifty errors and
	// then went quiet would report the first and silently drop the rest.
	events = append(events, k.flushExpired(now, len(events))...)

	return events
}

// hold folds this scrape's records into a key's quiet window instead of
// emitting them, keeping the newest sample so a later rollup has something
// concrete to say.
//
// Reached two ways -- the key spoke recently, or this scrape has already
// emitted its cap -- and both mean the same thing to the operator: the records
// happened, and they are counted rather than lost.
func (k *Kmsg) hold(key foldKey, f *suppression, now time.Time) {
	s, ok := k.suppress[key]
	if !ok {
		s = &suppression{until: now.Add(kmsgSuppressWindow)}
		k.suppress[key] = s
	}
	s.held += f.held
	s.message, s.severity, s.priority, s.last = f.message, f.severity, f.priority, f.last
}

// flushExpired emits a rollup for every key whose quiet window has closed with
// records still held, and forgets keys that closed with nothing to say.
func (k *Kmsg) flushExpired(now time.Time, emitted int) []*netrav1.Event {
	keys := make([]foldKey, 0, len(k.suppress))
	for key := range k.suppress {
		keys = append(keys, key)
	}
	slices.SortFunc(keys, func(a, b foldKey) int {
		if a.Type != b.Type {
			return strings.Compare(a.Type, b.Type)
		}
		return strings.Compare(a.Subject, b.Subject)
	})

	var events []*netrav1.Event
	for _, key := range keys {
		s := k.suppress[key]
		if now.Before(s.until) {
			continue
		}
		if s.held == 0 {
			// Quiet window closed with nothing withheld: the incident is over
			// and the key can stop being remembered.
			delete(k.suppress, key)
			continue
		}
		if emitted+len(events) >= kmsgMaxEvents {
			break
		}
		// The held records are reported as SUPPRESSED, not as a count. They
		// did not arrive together in this scrape -- they trickled in across a
		// ten-minute window -- and `count` means a burst, which the UI renders
		// as "x118". Saying 118 records landed at once when they arrived over
		// ten minutes describes an incident that did not happen.
		rollup := &suppression{
			message:  s.message,
			severity: s.severity,
			priority: s.priority,
			held:     1,
		}
		events = append(events, k.event(key, rollup, s.held, s.last))
		k.suppress[key] = &suppression{until: now.Add(kmsgSuppressWindow)}
	}
	return events
}

// event builds one event from a fold.
func (k *Kmsg) event(key foldKey, f *suppression, carried int, ts time.Time) *netrav1.Event {
	detail := kmsgDetail{
		Severity:   f.severity,
		Message:    f.message,
		Priority:   f.priority,
		Suppressed: carried,
	}
	if f.held > 1 {
		detail.Count = f.held
	}

	body, err := json.Marshal(detail)
	if err != nil {
		// Marshalling a struct of strings and ints cannot fail in practice; if
		// it somehow does, the fact that something happened to this device is
		// worth more than the detail.
		body = []byte("{}")
	}

	return &netrav1.Event{
		TsMs:       ts.UnixMilli(),
		Type:       key.Type,
		Subject:    key.Subject,
		DetailJson: string(body),
	}
}

// kmsgWallClock converts a record's monotonic stamp to wall time against an
// anchor taken at drain time.
//
// A record from the future -- the anchor read a moment after a record written a
// moment before it -- is clamped to now rather than allowed through: an event
// stamped ahead of the scrape that carried it sorts above rows that genuinely
// came later.
func kmsgWallClock(mono time.Duration, now time.Time, up time.Duration) time.Time {
	if up <= 0 {
		return now
	}
	age := up - mono
	if age < 0 {
		return now
	}
	return now.Add(-age)
}
