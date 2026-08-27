package collector_test

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"testing"

	"github.com/trick77/netra/internal/agent/collector"
	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
)

// The fixtures hold seven records of which three are USER_PROCESS. Counting
// records rather than sessions gives 7; counting a LOGIN_PROCESS (a getty
// waiting on an empty tty) as a login gives 4. Only 3 is right.
//
// Both fixtures were written by glibc's own pututline inside a container --
// see testdata/utmp/README.md. Their whole value is that netra did not
// produce them, so a layout error here cannot cancel itself out.
func TestUsersCountsOnlyUserProcess(t *testing.T) {
	for _, tc := range []struct {
		name       string
		file       string
		recordSize int
	}{
		{"glibc amd64", "testdata/utmp/glibc-amd64.utmp", 384},
		{"glibc arm64", "testdata/utmp/glibc-arm64.utmp", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := collector.NewUsers(nil, tc.file)

			var sample netrav1.HostSample
			if err := collectInto(u, &sample); err != nil {
				t.Fatalf("Collect: %v", err)
			}

			if sample.UsersLoggedIn == nil || *sample.UsersLoggedIn != 3 {
				t.Errorf("UsersLoggedIn = %v, want 3", sample.UsersLoggedIn)
			}
			if got := u.Capabilities()["users"]; got != "ok" {
				t.Errorf("capability = %q, want %q", got, "ok")
			}
		})
	}
}

// Detection must not be getting the right answer by luck. Pinning the size to
// the one the fixture was actually written with proves the parse itself is
// correct, independently of the candidate ordering.
func TestUsersParsesWithThePinnedRecordSize(t *testing.T) {
	for _, tc := range []struct {
		file       string
		recordSize int
	}{
		{"testdata/utmp/glibc-amd64.utmp", 384},
		{"testdata/utmp/glibc-arm64.utmp", 400},
	} {
		u := collector.NewUsers(nil, tc.file)
		u.SetRecordSizesForTest(tc.recordSize)

		var sample netrav1.HostSample
		if err := collectInto(u, &sample); err != nil {
			t.Fatalf("Collect %s: %v", tc.file, err)
		}
		if sample.UsersLoggedIn == nil || *sample.UsersLoggedIn != 3 {
			t.Errorf("%s at size %d: UsersLoggedIn = %v, want 3",
				tc.file, tc.recordSize, sample.UsersLoggedIn)
		}
	}
}

// The reason the record size is detected rather than assumed: an amd64 build
// may be reading an arm64 host's utmp layout, or a glibc build a musl file.
// Forcing the wrong size must fail loudly rather than return a number.
func TestUsersWrongRecordSizeIsRejectedNotMiscounted(t *testing.T) {
	u := collector.NewUsers(nil, "testdata/utmp/glibc-arm64.utmp")
	u.SetRecordSizesForTest(384) // the file is 400-byte records

	var sample netrav1.HostSample
	if err := collectInto(u, &sample); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if sample.UsersLoggedIn != nil {
		t.Errorf("UsersLoggedIn = %v, want nil when the layout does not match",
			*sample.UsersLoggedIn)
	}
	if got := u.Capabilities()["users"]; got != "unsupported-format" {
		t.Errorf("capability = %q, want %q", got, "unsupported-format")
	}
}

// Alpine and other busybox systems ship no utmp writer, and a container
// without the bind mount sees no file. Neither is an error, and neither may
// report zero sessions as though it had looked.
func TestUsersMissingFileLeavesFieldUnsetAndReportsCapability(t *testing.T) {
	u := collector.NewUsers(nil, t.TempDir()+"/absent-utmp")

	var sample netrav1.HostSample
	if err := collectInto(u, &sample); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if sample.UsersLoggedIn != nil {
		t.Errorf("UsersLoggedIn = %v, want nil when utmp is absent", *sample.UsersLoggedIn)
	}
	if got := u.Capabilities()["users"]; got != "unavailable" {
		t.Errorf("capability = %q, want %q", got, "unavailable")
	}
}

// musl's pututline is a no-op stub, so an Alpine host that does have the file
// has an empty one. Zero is the honest count there, not a missing reading.
func TestUsersEmptyFileCountsZero(t *testing.T) {
	path := t.TempDir() + "/utmp"
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	u := collector.NewUsers(nil, path)

	var sample netrav1.HostSample
	if err := collectInto(u, &sample); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if sample.UsersLoggedIn == nil || *sample.UsersLoggedIn != 0 {
		t.Errorf("UsersLoggedIn = %v, want 0 for an empty utmp", sample.UsersLoggedIn)
	}
}

// A file whose length is not a whole number of records is refused outright,
// rather than parsed up to the last whole one.
//
// Tolerating a remainder is what makes the detection unsound: see
// TestUsersWrongRecordSizeIsRejectedNotMiscounted for the case it lets
// through. A torn write -- utmp is rewritten in place with no locking this
// reader takes part in -- therefore costs one NULL sample and is corrected on
// the next scrape, which is the right trade against a silently wrong count.
func TestUsersTruncatedTrailingRecordIsRefused(t *testing.T) {
	raw, err := os.ReadFile("testdata/utmp/glibc-amd64.utmp")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	path := t.TempDir() + "/utmp"
	// Six whole records plus half of the seventh.
	if err := os.WriteFile(path, raw[:6*384+100], 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	u := collector.NewUsers(nil, path)
	u.SetRecordSizesForTest(384)

	var sample netrav1.HostSample
	if err := collectInto(u, &sample); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if sample.UsersLoggedIn != nil {
		t.Errorf("UsersLoggedIn = %v, want nil for a partial trailing record",
			*sample.UsersLoggedIn)
	}
	if got := u.Capabilities()["users"]; got != "unsupported-format" {
		t.Errorf("capability = %q, want %q", got, "unsupported-format")
	}
}

// The detector must survive a file that both candidate strides divide
// exactly. 9600 is lcm(384, 400), so this is 25 records at one stride or 24
// at the other, and only the correct reading has plausible ut_types
// throughout.
func TestUsersAmbiguousFileLengthPicksTheValidLayout(t *testing.T) {
	raw, err := os.ReadFile("testdata/utmp/glibc-arm64.utmp")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	// 24 records of 400 bytes = 9600, which is also 25 x 384.
	var padded []byte
	for len(padded) < 9600 {
		padded = append(padded, raw...)
	}
	padded = padded[:9600]

	path := t.TempDir() + "/utmp"
	if err := os.WriteFile(path, padded, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	u := collector.NewUsers(nil, path)

	var sample netrav1.HostSample
	if err := collectInto(u, &sample); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	// 24 records of the repeating 7-record fixture: 3 full cycles (21
	// records, 9 USER_PROCESS) plus records 0,1,2 of the fourth cycle, which
	// are RUN_LVL, BOOT_TIME and LOGIN_PROCESS -- none of them counted.
	if sample.UsersLoggedIn == nil || *sample.UsersLoggedIn != 9 {
		t.Errorf("UsersLoggedIn = %v, want 9", sample.UsersLoggedIn)
	}
}

// A file that is not a utmp at all must not yield a number. This is the guard
// that keeps a big-endian or foreign-layout host from reporting a count read
// out of the middle of somebody's hostname.
func TestUsersImplausibleRecordsReportUnsupportedFormat(t *testing.T) {
	path := t.TempDir() + "/utmp"

	// 400 bytes of 0xAA: ut_type reads as 43690, far outside 0..9.
	junk := make([]byte, 400)
	for i := range junk {
		junk[i] = 0xAA
	}
	if err := os.WriteFile(path, junk, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	u := collector.NewUsers(nil, path)

	var sample netrav1.HostSample
	if err := collectInto(u, &sample); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if sample.UsersLoggedIn != nil {
		t.Errorf("UsersLoggedIn = %v, want nil for a non-utmp file", *sample.UsersLoggedIn)
	}
	if got := u.Capabilities()["users"]; got != "unsupported-format" {
		t.Errorf("capability = %q, want %q", got, "unsupported-format")
	}
}

// Verifies the fixtures against the layout documented in users.go and
// testdata/utmp/README.md, rather than trusting the opaque binaries. If a
// fixture is ever regenerated wrongly, this fails with a specific reason
// instead of the count tests failing with an arithmetic one.
func TestUtmpFixturesMatchTheDocumentedLayout(t *testing.T) {
	for _, tc := range []struct {
		file       string
		recordSize int
	}{
		{"testdata/utmp/glibc-amd64.utmp", 384},
		{"testdata/utmp/glibc-arm64.utmp", 400},
	} {
		raw, err := os.ReadFile(tc.file)
		if err != nil {
			t.Fatalf("read %s: %v", tc.file, err)
		}

		if len(raw) != 7*tc.recordSize {
			t.Errorf("%s: %d bytes, want %d (7 records of %d)",
				tc.file, len(raw), 7*tc.recordSize, tc.recordSize)
			continue
		}

		// The exact ut_type sequence the README documents.
		want := []uint16{1, 2, 6, 7, 7, 7, 8}
		for i, wantType := range want {
			rec := raw[i*tc.recordSize:]
			if got := binary.LittleEndian.Uint16(rec[0:]); got != wantType {
				t.Errorf("%s record %d: ut_type = %d, want %d", tc.file, i, got, wantType)
			}
			// The two bytes after ut_type are padding the detector relies on.
			if got := binary.LittleEndian.Uint16(rec[2:]); got != 0 {
				t.Errorf("%s record %d: padding = %d, want 0", tc.file, i, got)
			}
		}

		// ut_user at offset 44 -- proof the offsets are real and not just
		// consistent with themselves.
		rec := raw[3*tc.recordSize:]
		if got := cstring(rec[44:76]); got != "jan" {
			t.Errorf("%s record 3: ut_user = %q, want %q", tc.file, got, "jan")
		}
		// ut_host at offset 76.
		rec = raw[4*tc.recordSize:]
		if got := cstring(rec[76:332]); got != "10.0.0.5" {
			t.Errorf("%s record 4: ut_host = %q, want %q", tc.file, got, "10.0.0.5")
		}
	}
}

// cstring reads a NUL-terminated string out of a fixed-width field.
func cstring(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}

func TestUsersName(t *testing.T) {
	u := collector.NewUsers(nil, "testdata/utmp/glibc-amd64.utmp")

	if got := u.Name(); got != "users" {
		t.Errorf("Name() = %q, want %q", got, "users")
	}
}

func TestUsersImplementsCapabilityReporter(t *testing.T) {
	var _ collector.CapabilityReporter = collector.NewUsers(nil, "x")
}

// The reason this collector grew a second source: a host running systemd 257
// has no /run/utmp at all, and the utmp path alone reports "unavailable" for
// ever while people are logged in.
func TestUsersPrefersLogindOverUtmp(t *testing.T) {
	// A utmp that WOULD parse, to prove the count came from logind rather
	// than from the file happening to agree.
	u := collector.NewUsers(
		func(context.Context) (int, error) { return 5, nil },
		"testdata/utmp/glibc-amd64.utmp",
	)

	var sample netrav1.HostSample
	if err := collectInto(u, &sample); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if sample.UsersLoggedIn == nil || *sample.UsersLoggedIn != 5 {
		t.Errorf("UsersLoggedIn = %v, want 5 from logind (utmp holds 3)", sample.UsersLoggedIn)
	}
	if got := u.Capabilities()["users"]; got != "ok" {
		t.Errorf("capability = %q, want %q", got, "ok")
	}
	if got := u.Capabilities()["users_source"]; got != "logind" {
		t.Errorf("source = %q, want %q", got, "logind")
	}
}

// No system bus, or no logind on it: the utmp path is still there and still
// answers, and the source says which one did.
func TestUsersFallsBackToUtmpWhenLogindFails(t *testing.T) {
	u := collector.NewUsers(
		func(context.Context) (int, error) { return 0, errors.New("no such file: /run/dbus/system_bus_socket") },
		"testdata/utmp/glibc-amd64.utmp",
	)

	var sample netrav1.HostSample
	if err := collectInto(u, &sample); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if sample.UsersLoggedIn == nil || *sample.UsersLoggedIn != 3 {
		t.Errorf("UsersLoggedIn = %v, want 3 from utmp", sample.UsersLoggedIn)
	}
	if got := u.Capabilities()["users_source"]; got != "utmp" {
		t.Errorf("source = %q, want %q", got, "utmp")
	}
}

// Neither source: the field stays unset and NO source is claimed. A stale
// "logind" left beside "unavailable" would say the bus is still being read.
func TestUsersReportsNoSourceWhenNothingAnswers(t *testing.T) {
	u := collector.NewUsers(
		func(context.Context) (int, error) { return 0, errors.New("no bus") },
		t.TempDir()+"/absent-utmp",
	)

	var sample netrav1.HostSample
	if err := collectInto(u, &sample); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if sample.UsersLoggedIn != nil {
		t.Errorf("UsersLoggedIn = %v, want nil", *sample.UsersLoggedIn)
	}
	if got := u.Capabilities()["users"]; got != "unavailable" {
		t.Errorf("capability = %q, want %q", got, "unavailable")
	}
	if got, ok := u.Capabilities()["users_source"]; ok {
		t.Errorf("source = %q, want it absent when nothing answered", got)
	}
}

// A logind that answers zero is a real zero -- nobody is logged in -- and it
// must NOT fall through to utmp, which on a host that still has the file
// would then report a count logind has just contradicted.
func TestUsersZeroFromLogindIsNotAFallback(t *testing.T) {
	u := collector.NewUsers(
		func(context.Context) (int, error) { return 0, nil },
		"testdata/utmp/glibc-amd64.utmp",
	)

	var sample netrav1.HostSample
	if err := collectInto(u, &sample); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if sample.UsersLoggedIn == nil || *sample.UsersLoggedIn != 0 {
		t.Errorf("UsersLoggedIn = %v, want 0", sample.UsersLoggedIn)
	}
	if got := u.Capabilities()["users_source"]; got != "logind" {
		t.Errorf("source = %q, want %q", got, "logind")
	}
}
