package collector_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trick77/netra/internal/agent/collector"
)

// blockingStatfs answers for every mountpoint except one, which never returns.
//
// That is what statfs(2) does against a dead NFS or CIFS server, or a FUSE
// mount whose userspace daemon has died: the call parks in uninterruptible D
// state and cannot be cancelled by a context or a signal. The collector's only
// defence is to stop waiting for it.
func blockingStatfs(block string, stats map[string]collector.FsStat, calls *atomic.Int64) collector.StatfsFunc {
	return func(mountpoint string) (collector.FsStat, error) {
		if mountpoint == block {
			calls.Add(1)
			select {} // never returns, exactly like the syscall it stands for
		}
		st, ok := stats[mountpoint]
		if !ok {
			return collector.FsStat{}, errNoSuchMount
		}
		return st, nil
	}
}

var errNoSuchMount = errNoMount{}

type errNoMount struct{}

func (errNoMount) Error() string { return "no such mount" }

// A mountpoint that never answers must cost its own row, not the scrape.
//
// Before the deadline this blocked the collector, and with it the single
// goroutine that runs every other collector, the flush and the ring -- so one
// dead NFS server silenced the whole agent with no error anywhere.
func TestFilesystemsAbandonsAWedgedMountpoint(t *testing.T) {
	var calls atomic.Int64
	testee := collector.NewFilesystems("testdata/mounts", nil, blockingStatfs("/data",
		map[string]collector.FsStat{
			"/":            {Total: 1000, Free: 400, Used: 600, DeviceID: 1},
			"/run":         {Total: 500, Free: 250, Used: 250, DeviceID: 3},
			"/mnt/my disk": {Total: 300, Free: 100, Used: 200, DeviceID: 4},
		}, &calls))
	testee.SetStatfsTimeoutForTest(50 * time.Millisecond)

	done := make(chan struct{})
	go func() {
		defer close(done)
		res, err := testee.Collect(context.Background())
		if err != nil {
			t.Errorf("Collect: %v", err)
			return
		}
		// Every other filesystem still reported.
		if len(res.Filesystems) == 0 {
			t.Error("a wedged mountpoint cost every other filesystem its row")
		}
		for _, r := range res.Filesystems {
			if r.GetMountpoint() == "/data" {
				t.Error("the wedged mountpoint produced a row; it measured nothing")
			}
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Collect never returned; a wedged statfs is still stalling the scrape")
	}
}

// The abandoned goroutine must be paid for once, not once per scrape.
//
// A permanently dead mount is the case the deadline exists for, and it is also
// the case that would strand a goroutine every 60s for the life of the agent.
// The backoff is what makes the deadline's cost bounded rather than cumulative.
func TestFilesystemsBacksOffAWedgedMountpointRatherThanRetryingEveryScrape(t *testing.T) {
	var calls atomic.Int64
	testee := collector.NewFilesystems("testdata/mounts", nil, blockingStatfs("/data",
		map[string]collector.FsStat{
			"/":            {Total: 1000, Free: 400, Used: 600, DeviceID: 1},
			"/run":         {Total: 500, Free: 250, Used: 250, DeviceID: 3},
			"/mnt/my disk": {Total: 300, Free: 100, Used: 200, DeviceID: 4},
		}, &calls))
	testee.SetStatfsTimeoutForTest(20 * time.Millisecond)

	const scrapes = 6
	for range scrapes {
		if _, err := testee.Collect(context.Background()); err != nil {
			t.Fatalf("Collect: %v", err)
		}
	}

	// Backoff doubles per failure (1, 2, 4, ... scrapes), so six scrapes must
	// attempt the dead mount far fewer than six times.
	if got := calls.Load(); got >= scrapes {
		t.Errorf("statfs attempted %d times across %d scrapes; the backoff is not holding it off",
			got, scrapes)
	}
	if calls.Load() == 0 {
		t.Error("statfs was never attempted; the mount should be tried at least once")
	}
}

// A scrape whose own budget ran out must not be mistaken for a wedged mount.
//
// deadlined derives its context from the scrape's, so once the scrape deadline
// fires every remaining mountpoint returns DeadlineExceeded whether or not it
// ever blocked. Backing those off would hold healthy filesystems at a
// seventeen-hour cadence because some earlier collector was slow.
func TestFilesystemsDoesNotMarkAMountWedgedWhenTheScrapeItselfExpired(t *testing.T) {
	var calls atomic.Int64
	stats := map[string]collector.FsStat{
		"/":            {Total: 1000, Free: 400, Used: 600, DeviceID: 1},
		"/data":        {Total: 2000, Free: 500, Used: 1500, DeviceID: 2},
		"/run":         {Total: 500, Free: 250, Used: 250, DeviceID: 3},
		"/mnt/my disk": {Total: 300, Free: 100, Used: 200, DeviceID: 4},
	}
	testee := collector.NewFilesystems("testdata/mounts", nil,
		func(mountpoint string) (collector.FsStat, error) {
			calls.Add(1)
			st, ok := stats[mountpoint]
			if !ok {
				return collector.FsStat{}, errNoSuchMount
			}
			return st, nil
		})
	testee.SetStatfsTimeoutForTest(time.Second)

	// An already-cancelled scrape: every statfs reports DeadlineExceeded
	// without any of them having blocked.
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	// A scrape with no budget left measures nothing, and says so: an empty
	// Result with a nil error would be recorded as a healthy collector that
	// simply found no filesystems.
	if _, err := testee.Collect(expired); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Collect on an expired scrape returned %v, want a deadline error", err)
	}

	// Nothing was marked, so the next healthy scrape measures everything.
	before := calls.Load()
	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if calls.Load() == before {
		t.Fatal("no statfs was attempted on the next scrape; healthy mounts were backed off")
	}
	if len(res.Filesystems) == 0 {
		t.Error("no filesystems reported after an expired scrape; the mounts were wrongly wedged")
	}
}
