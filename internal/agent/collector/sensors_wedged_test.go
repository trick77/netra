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

// A wedged driver must cost ONE scrape, not the loop -- and not one leaked
// goroutine per scrape thereafter.
//
// The deadline alone does not achieve that. A read(2) blocked in the kernel
// cannot be cancelled from Go, so the goroutine behind a timed-out read stays
// resident until the process exits. Retrying the same path every 60s would
// therefore strand a new one every minute for the life of the agent. The fix
// is to stop reading a path that has already timed out once, and that is what
// this test pins.
func TestSensorsDoesNotRereadAWedgedPath(t *testing.T) {
	root := wedgedHwmon(t)
	testee := collector.NewSensors(root, time.Minute, 50*time.Millisecond)

	// First scrape pays the deadline once and gives up on the path.
	start := time.Now()
	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(res.Sensors) != 0 {
		t.Errorf("rows = %d, want 0 -- the chip never identified itself", len(res.Sensors))
	}
	if elapsed := time.Since(start); elapsed < 50*time.Millisecond {
		t.Errorf("first scrape took %v, want at least the 50ms deadline", elapsed)
	}

	before := runtime.NumGoroutine()

	// Every later scrape must skip the path outright: no deadline waited, no
	// second goroutine stranded.
	for i := range 5 {
		start = time.Now()
		if _, err := testee.Collect(context.Background()); err != nil {
			t.Fatalf("Collect %d: %v", i, err)
		}
		if elapsed := time.Since(start); elapsed >= 50*time.Millisecond {
			t.Fatalf("scrape %d took %v; a path that already timed out must not be read again", i, elapsed)
		}
	}

	// Five more scrapes must not have stranded five more goroutines.
	if after := runtime.NumGoroutine(); after > before+1 {
		t.Errorf("goroutines went from %d to %d across five scrapes of a wedged path", before, after)
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
