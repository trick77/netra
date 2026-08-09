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
	testee := collector.NewSensors(root, time.Minute, 50*time.Millisecond)

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
	testee := collector.NewSensors(root, time.Minute, 50*time.Millisecond)

	if _, err := testee.Collect(context.Background()); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	if got := testee.Capabilities()["sensors"]; got != "degraded" {
		t.Errorf("capability = %q, want degraded", got)
	}
}
