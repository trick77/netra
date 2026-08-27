//go:build unix

package collector_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/trick77/netra/internal/agent/collector"
)

// wedgedHwmon builds a hwmon tree whose chip name file is a FIFO with no
// writer, so reading it blocks in the kernel exactly as a stuck driver does.
//
// A real wedged hwmon read is uninterruptible, and so is this one: that is the
// point. The cleanup opens the write end to release whatever goroutine the
// collector stranded on it, so the leak under test does not outlive the test.
func wedgedHwmon(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	chip := filepath.Join(root, "class", "hwmon", "hwmon0")
	if err := os.MkdirAll(chip, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	fifo := filepath.Join(chip, "name")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}

	t.Cleanup(func() {
		// Opening the write end completes the blocked open/read so the
		// stranded goroutine can finish rather than outliving the test binary.
		f, err := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0)
		if err != nil {
			return
		}
		_, _ = f.WriteString("coretemp\n")
		_ = f.Close()
	})

	return root
}

// A wedged driver must cost ONE scrape, not the loop -- and must not strand a
// goroutine on every scrape thereafter.
//
// The deadline alone does not achieve that. A read(2) blocked in the kernel
// cannot be cancelled from Go, so the goroutine behind a timed-out read stays
// resident until the process exits. Retrying the same path every 60s would
// strand a new one every minute for the life of the agent.
//
// The answer is an exponential backoff, not a permanent blacklist: the
// deadline cannot tell a wedged driver from a merely slow one, so a path is
// retried on a doubling schedule. Over 20 scrapes that is a handful of
// attempts instead of 20.
func TestSensorsBacksOffAWedgedPathInsteadOfRereadingIt(t *testing.T) {
	root := wedgedHwmon(t)
	testee := collector.NewSensors(root, 50*time.Millisecond)

	const scrapes = 20
	attempts := 0
	before := runtime.NumGoroutine()

	for i := range scrapes {
		start := time.Now()
		res, err := testee.Collect(context.Background())
		if err != nil {
			t.Fatalf("Collect %d: %v", i, err)
		}
		if len(res.Sensors) != 0 {
			t.Errorf("scrape %d produced rows; the chip never identified itself", i)
		}
		// A scrape that waited out the deadline actually attempted the read.
		if time.Since(start) >= 50*time.Millisecond {
			attempts++
		}
	}

	// Doubling from the first failure gives attempts at scrapes 1, 2, 4, 8 and
	// 16 -- five of twenty. The bound is loose enough not to encode the exact
	// schedule, tight enough to fail a collector that retries every scrape.
	if attempts > 8 {
		t.Errorf("attempted the wedged read %d times in %d scrapes; the backoff is not holding",
			attempts, scrapes)
	}
	// ...but it must NOT be permanent: a path that recovers has to be retried.
	if attempts < 2 {
		t.Errorf("attempted the wedged read %d times; a slow read that recovers would never be picked up again",
			attempts)
	}

	// And the stranded goroutines are bounded by the attempts, not the scrapes.
	if after := runtime.NumGoroutine(); after > before+attempts+1 {
		t.Errorf("goroutines went from %d to %d across %d scrapes", before, after, scrapes)
	}
}

// A wedged sensor stops producing rows, which is indistinguishable from a
// sensor that vanished unless the agent says so.
//
// "degraded" rather than "absent": absent means this host has no hwmon at all,
// and reporting a hardware fault that way would hide it.
func TestSensorsReportsDegradedWhenAPathIsWedged(t *testing.T) {
	root := wedgedHwmon(t)
	testee := collector.NewSensors(root, 50*time.Millisecond)

	if _, err := testee.Collect(context.Background()); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if got := testee.Capabilities()["sensors"]; got != "degraded" {
		t.Errorf("capability = %q, want degraded", got)
	}
}

// A path that times out once and then answers must be read again.
//
// This is the difference between backing off and blacklisting. The deadline
// cannot tell a wedged driver from a contended i2c bus or a momentarily loaded
// host, so a single slow read must not cost that sensor until someone restarts
// the agent.
func TestSensorsRecoversAfterATransientTimeout(t *testing.T) {
	root := t.TempDir()
	chip := filepath.Join(root, "class", "hwmon", "hwmon0")
	if err := os.MkdirAll(chip, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for name, content := range map[string]string{
		"temp1_label": "Package id 0",
		"temp1_input": "45000",
	} {
		if err := os.WriteFile(filepath.Join(chip, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	// The chip name is a FIFO with no writer for the first scrape, so reading
	// it blocks exactly as a stuck driver does.
	name := filepath.Join(chip, "name")
	if err := syscall.Mkfifo(name, 0o644); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}

	testee := collector.NewSensors(root, 50*time.Millisecond)

	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("first Collect: %v", err)
	}
	if len(res.Sensors) != 0 {
		t.Fatalf("first scrape produced %d rows; the chip never identified itself", len(res.Sensors))
	}
	if got := testee.Capabilities()["sensors"]; got != "degraded" {
		t.Errorf("capability = %q after the timeout, want degraded", got)
	}

	// The condition clears: replace the FIFO with a readable file. The stranded
	// reader is released by opening the write end first.
	f, err := os.OpenFile(name, os.O_WRONLY|syscall.O_NONBLOCK, 0)
	if err == nil {
		_, _ = f.WriteString("coretemp\n")
		_ = f.Close()
	}
	if err := os.Remove(name); err != nil {
		t.Fatalf("remove fifo: %v", err)
	}
	if err := os.WriteFile(name, []byte("coretemp\n"), 0o644); err != nil {
		t.Fatalf("write name: %v", err)
	}

	// The very next scrape is inside the first backoff window (one scrape), so
	// the path is tried again and now succeeds.
	res, err = testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("second Collect: %v", err)
	}
	if len(res.Sensors) != 1 {
		t.Fatalf("rows after recovery = %d, want 1 -- a path that recovered must be read again",
			len(res.Sensors))
	}
	if got := res.Sensors[0].GetChip(); got != "coretemp" {
		t.Errorf("chip = %q, want coretemp", got)
	}
	if testee.Capabilities() != nil {
		t.Errorf("capability = %v after recovery, want none -- the backoff must be cleared",
			testee.Capabilities())
	}
}

// Every collector whose FIRST Collect returns data must declare itself to
// Prime, or that data is silently discarded at agent startup.
//
// The list is spelled out rather than derived, because the property cannot be
// inferred from the type: it is a statement about what the first Collect
// MEANS. Each entry below has a different reason, and all four were found the
// hard way -- systemd and mdraid report what they found on arrival, while
// packages and smart additionally gate themselves afterwards, so a discarded
// first result is not re-taken for up to 24 hours and an hour respectively.
func TestBaselineCollectorsDeclareThemselves(t *testing.T) {
	for _, c := range []struct {
		name string
		col  any
	}{
		{"systemd", collector.NewSystemd(nil)},
		{"mdraid", collector.NewMdraid("/sys")},
		{"packages", collector.NewPackages("/var/lib/dpkg/status", "/lib/apk/db/installed")},
		{"smart", collector.NewSmart(time.Hour, nil, "")},
	} {
		b, ok := c.col.(collector.BaselineEmitter)
		if !ok {
			t.Errorf("%s does not implement BaselineEmitter; Prime will discard its first Collect", c.name)
			continue
		}
		if !b.EmitsBaseline() {
			t.Errorf("%s.EmitsBaseline() = false, want true", c.name)
		}
	}
}

// A path that times out and then DISAPPEARS must not pin the host at
// "degraded" forever.
//
// Clearing the backoff only on a successful read left the entry behind for a
// chip that timed out once and was then unbound, or renumbered by a driver
// rebind (hwmon3 -> hwmon4). ENOENT hits neither the timeout branch nor the
// success branch, so nothing ever removed it: Capabilities() kept reporting
// degraded for the life of the agent with no sensor actually failing, and the
// metadata hash never flipped back.
func TestSensorsClearsTheBackoffWhenAWedgedPathDisappears(t *testing.T) {
	root := t.TempDir()
	chip := filepath.Join(root, "class", "hwmon", "hwmon0")
	if err := os.MkdirAll(chip, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	name := filepath.Join(chip, "name")
	if err := syscall.Mkfifo(name, 0o644); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}

	testee := collector.NewSensors(root, 50*time.Millisecond)

	if _, err := testee.Collect(context.Background()); err != nil {
		t.Fatalf("first Collect: %v", err)
	}
	if got := testee.Capabilities()["sensors"]; got != "degraded" {
		t.Fatalf("capability = %q after the timeout, want degraded", got)
	}

	// The chip goes away entirely: release the stranded reader, then remove it.
	if f, err := os.OpenFile(name, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
		_, _ = f.WriteString("coretemp\n")
		_ = f.Close()
	}
	if err := os.Remove(name); err != nil {
		t.Fatalf("remove fifo: %v", err)
	}

	// The next scrape is past the first backoff window, reads ENOENT, and must
	// forget the path rather than hold the host at degraded.
	if _, err := testee.Collect(context.Background()); err != nil {
		t.Fatalf("second Collect: %v", err)
	}
	if got := testee.Capabilities(); got != nil {
		t.Errorf("capability = %v after the path vanished, want none -- ENOENT is an answer, not a wedge", got)
	}
}
