package collector_test

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/trick77/netra/internal/agent/collector"
)

// The scrape deadline does not, on its own, bound a child process.
//
// exec.CommandContext cancels by sending SIGKILL, and .Output() calls Wait,
// which without a WaitDelay waits indefinitely for the process to exit AND for
// its stdout pipe to close. Neither is guaranteed: a smartctl blocked in an
// uninterruptible ioctl does not die on SIGKILL until the ioctl returns, and a
// surviving grandchild holds the pipe open regardless.
//
// This exercises the pipe half, which is the half a test can actually produce:
// the shell exits immediately while a background sleep inherits and holds
// stdout. Without a WaitDelay, Output blocks for the sleep's full duration.
// With one, it returns.
func TestCommandWithWaitDelayReturnsWhileAPipeIsStillHeld(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh is unavailable")
	}

	const script = "sleep 10 & echo scanned"

	run := func(delay time.Duration) time.Duration {
		t.Helper()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		cmd := exec.CommandContext(ctx, "sh", "-c", script)
		cmd.WaitDelay = delay

		start := time.Now()
		out, _ := cmd.Output()
		elapsed := time.Since(start)

		if delay > 0 && len(out) == 0 {
			t.Error("output was lost; WaitDelay must still deliver what the child wrote")
		}
		return elapsed
	}

	// The guard itself: the call returns while a grandchild still holds the
	// pipe, rather than waiting the full ten seconds.
	done := make(chan time.Duration, 1)
	go func() { done <- run(500 * time.Millisecond) }()

	select {
	case elapsed := <-done:
		if elapsed > 5*time.Second {
			t.Errorf("Output took %s; WaitDelay did not bound the wait", elapsed)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("Output never returned; the child's held pipe is still unbounded")
	}
}

// SystemSmartctl must carry the delay, not merely be capable of it.
//
// smartctl is almost never installed on the machine running this suite, so the
// assertion is on the bound rather than on a real scan: with a cancelled
// context and no WaitDelay, a hung child would hold this forever.
func TestSystemSmartctlReturnsOnACancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = collector.SystemSmartctl(ctx, "--json", "--scan")
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("SystemSmartctl did not return on a cancelled context")
	}
}
