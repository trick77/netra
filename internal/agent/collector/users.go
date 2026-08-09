package collector

import (
	"context"
	"encoding/binary"
	"errors"
	"io/fs"
	"os"
	"sync"
	"time"

	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

// Capability values reported by Users.
const (
	usersCapOK                = "ok"
	usersCapUnavailable       = "unavailable"
	usersCapUnsupportedFormat = "unsupported-format"
)

// utmp ut_type values. Only USER_PROCESS is counted: the others are the
// runlevel, the boot timestamp, getty processes waiting on a tty that nobody
// has logged into, and the tombstones of sessions that have ended.
const (
	// utTypeEmpty is an unused slot. glibc leaves these behind, and a stride
	// misread turns real data into a run of them -- see countWithRecordSize.
	utTypeEmpty = 0

	utTypeUserProcess = 7

	// utTypeMax is the highest ut_type Linux defines (ACCOUNTING). A record
	// outside 0..utTypeMax means the file is not laid out the way this parser
	// believes, which is the signal to report nothing rather than a number
	// derived from a misread struct.
	utTypeMax = 9
)

// utmp field offsets. These are stable across every combination measured;
// only the total record size varies. ut_type is the only field this parser
// reads, and the rest is recorded so the fixtures can be checked against a
// documented layout rather than trusted.
//
//	offset  size  field
//	     0     2  ut_type      <- the only field this parser reads
//	     2     2  (padding, always zero)
//	     4     4  ut_pid
//	     8    32  ut_line
//	    40     4  ut_id
//	    44    32  ut_user
//	    76   256  ut_host
//
// Everything after ut_host -- ut_exit, ut_session, ut_tv, ut_addr_v6 -- is
// where the width varies, and none of it is parsed.
const (
	utmpOffsetType = 0
	utmpOffsetPad  = 2
)

// utmpRecordSizes are the possible values of sizeof(struct utmp), most likely
// first. Measured, not assumed:
//
//	                amd64   arm64
//	glibc             384     400
//	musl              400     400
//
// The size therefore depends on the C library of the HOST whose utmp is being
// read, which a statically linked agent in a container cannot know at build
// time -- keying it on GOARCH alone would misparse a musl host on amd64. It
// is detected per file instead, which costs one extra validation pass and
// removes the guess entirely.
//
// glibc is listed first because musl's pututline is a no-op stub that writes
// nothing at all: a utmp file with records in it was, in practice, written by
// glibc. On a musl system the file is absent or empty and this collector
// reports users=unavailable, which is the correct answer there.
var utmpRecordSizes = []int{384, 400}

// Users reports how many interactive sessions are logged in, from utmp.
//
// Nothing about a session is transmitted -- not the user, not the tty, not
// the remote host -- only the count. The parser reads ut_type and nothing
// else, so the usernames and hostnames in the file are never even decoded.
//
// Absence is the normal case rather than an error: Alpine and other
// busybox-based systems ship no utmp writer at all, and a container without
// the bind mount sees no file. Both report a capability and leave the field
// unset.
type Users struct {
	path     string
	interval time.Duration

	// recordSizes are the sizeof(struct utmp) candidates to try, in order. A
	// field rather than the package variable directly so a test can pin a
	// single size and prove the detection is not just getting lucky.
	recordSizes []int

	mu           sync.Mutex
	capabilities map[string]string
}

// NewUsers builds a Users collector reading the utmp file at path.
func NewUsers(path string, interval time.Duration) *Users {
	return &Users{path: path, interval: interval, recordSizes: utmpRecordSizes}
}

// Name implements Collector.
func (u *Users) Name() string { return "users" }

// Interval implements Collector.
func (u *Users) Interval() time.Duration { return u.interval }

// SetPathForTest repoints the collector at a fixture file.
func (u *Users) SetPathForTest(path string) { u.path = path }

// SetRecordSizesForTest pins the candidate sizes, so a test can prove a
// fixture parses under one specific layout rather than relying on detection
// happening to pick the right one.
func (u *Users) SetRecordSizesForTest(sizes ...int) { u.recordSizes = sizes }

// Capabilities implements CapabilityReporter.
func (u *Users) Capabilities() map[string]string {
	u.mu.Lock()
	defer u.mu.Unlock()

	out := make(map[string]string, len(u.capabilities))
	for k, v := range u.capabilities {
		out[k] = v
	}
	return out
}

func (u *Users) setCapability(value string) {
	u.mu.Lock()
	defer u.mu.Unlock()

	if u.capabilities == nil {
		u.capabilities = make(map[string]string, 1)
	}
	u.capabilities["users"] = value
}

// Collect implements Collector.
func (u *Users) Collect(_ context.Context) (*Result, error) {
	sample := &netrav1.HostSample{}

	raw, err := os.ReadFile(u.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission) {
			u.setCapability(usersCapUnavailable)
			return &Result{Host: sample}, nil
		}
		return nil, err
	}

	// An empty file is the normal state on a musl system, whose pututline
	// writes nothing. Zero sessions is the honest answer, and detection has
	// nothing to work with anyway.
	if len(raw) == 0 {
		u.setCapability(usersCapOK)
		n := uint32(0)
		sample.UsersLoggedIn = &n
		return &Result{Host: sample}, nil
	}

	count, ok := u.countSessions(raw)
	if !ok {
		u.setCapability(usersCapUnsupportedFormat)
		return &Result{Host: sample}, nil
	}

	u.setCapability(usersCapOK)
	n := uint32(count)
	sample.UsersLoggedIn = &n

	return &Result{Host: sample}, nil
}

// countSessions counts USER_PROCESS records, and reports false when the bytes
// do not look like a utmp under any layout this parser knows.
//
// The record size is detected rather than assumed, because it depends on the
// host's C library and word size, not on how the agent was built. A candidate
// is accepted only if it divides the file exactly and every record it implies
// has a plausible ut_type and a zeroed pad -- a wrong stride lands ut_type in
// the middle of a username or an address and fails almost immediately.
func (u *Users) countSessions(raw []byte) (int, bool) {
	// The stride must divide the file EXACTLY. A partial trailing record is
	// rejected rather than tolerated, and that strictness is the whole
	// mechanism -- without it the detection is unsound.
	//
	// A file of 400-byte records also validates at a 384-byte stride if
	// remainders are allowed: the 16 bytes each record then contributes out
	// of alignment are zeros, which read as perfectly legal EMPTY records, so
	// an arm64 host's three sessions come back as zero. Exact division is
	// what distinguishes the two, and there is no weaker rule that does.
	//
	// The cost is that a torn write -- utmp is rewritten in place by login
	// processes with no locking this reader takes part in -- yields no count
	// for that scrape. That is a NULL once in a blue moon, self-corrected 60
	// seconds later, against silently reporting the wrong number forever.
	for _, size := range u.recordSizes {
		if size <= 0 || len(raw) < size || len(raw)%size != 0 {
			continue
		}
		if count, ok := countWithRecordSize(raw, size); ok {
			return count, true
		}
	}

	return 0, false
}

// countWithRecordSize counts USER_PROCESS records assuming a given stride,
// and reports whether every record it implies is plausible. Callers only pass
// a stride that divides the input exactly.
func countWithRecordSize(raw []byte, size int) (int, bool) {
	count := 0
	whole := 0
	nonEmpty := 0

	for off := 0; off+size <= len(raw); off += size {
		rec := raw[off : off+size]
		whole++

		utType := binary.LittleEndian.Uint16(rec[utmpOffsetType:])
		if utType > utTypeMax {
			return 0, false
		}
		// The two bytes after the 16-bit ut_type are structure padding and
		// are always zero. Checking them roughly squares the odds against a
		// wrong stride surviving, and costs nothing.
		if binary.LittleEndian.Uint16(rec[utmpOffsetPad:]) != 0 {
			return 0, false
		}

		if utType != utTypeEmpty {
			nonEmpty++
		}
		if utType == utTypeUserProcess {
			count++
		}
	}

	if whole == 0 {
		return 0, false
	}

	// A non-empty utmp always contains at least a RUN_LVL or BOOT_TIME
	// record, so a file that reads as nothing but EMPTY at this stride is
	// almost certainly being read at the wrong one -- which is exactly how a
	// 400-byte-record file looks at a 384-byte stride, since the bytes that
	// fall out of alignment are zeros.
	//
	// The cost is that a freshly truncated, genuinely all-zero utmp reports
	// unsupported-format instead of zero sessions. That is the safe
	// direction: it says "could not read this" rather than asserting that
	// nobody is logged in.
	if nonEmpty == 0 {
		return 0, false
	}

	return count, true
}
