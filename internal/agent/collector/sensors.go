package collector

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

// milliDegrees is the unit hwmon reports temperatures in.
const milliDegrees = 1000.0

// Sensors reports temperatures from /sys/class/hwmon.
//
// Identity is chip name + label, NEVER the hwmonN directory name. The N is
// allocation-order dependent, so a kernel upgrade or a hardware change
// reorders the directories across a reboot -- and a collector keyed on hwmonN
// would then attribute one chip's history to another with nothing raised. The
// series stays continuous and the numbers stay plausible, which is exactly
// what makes it dangerous.
//
// Every labelled input is reported rather than a "hottest" one: hottest-wins
// reports a different physical sensor from one minute to the next, so the
// resulting series is not a measurement of anything.
type Sensors struct {
	sysRoot  string
	interval time.Duration

	// readTimeout bounds a single sysfs read. A wedged driver blocks read(2)
	// indefinitely, and without a deadline that stalls the whole scrape loop --
	// every other collector included.
	readTimeout time.Duration

	// absent records that this host has no hwmon at all, so the hub is told
	// rather than left to infer it from missing rows.
	absent bool

	// wedged holds the sysfs paths whose read did not return within
	// readTimeout, and when each may be tried again.
	//
	// Backing off rather than blacklisting outright. Each attempt on a truly
	// wedged path strands a goroutine in an uninterruptible read(2), so
	// retrying every scrape would leak one a minute for the life of the agent
	// -- but never retrying is too strong the other way, because the deadline
	// cannot tell a wedged driver from a slow one. A contended i2c bus or a
	// momentarily loaded host can exceed the timeout and recover, and giving
	// up permanently would cost that sensor until someone restarted the agent.
	//
	// Doubling the wait means a transient blip recovers on the next scrape,
	// while a permanently stuck path is retried a logarithmic number of times
	// -- roughly ten stranded goroutines over a year, not half a million.
	wedged map[string]*wedgedPath

	// scrapes counts Collect calls, which is the clock the backoff is measured
	// in. Scrapes rather than wall time because the retry is only meaningful
	// when a scrape actually happens.
	scrapes uint64
}

// wedgedPath is one path's backoff state.
type wedgedPath struct {
	// failures is how many times this path has timed out, which sets the wait.
	failures uint
	// retryAt is the scrape number at which it may be read again.
	retryAt uint64
}

// maxWedgedBackoff caps the doubling at 1024 scrapes -- about seventeen hours
// at the 60s cadence. A path is never abandoned entirely, so a sensor that
// comes back after a firmware reset or a rebind is picked up again without an
// agent restart.
const maxWedgedBackoff = 1024

// NewSensors builds a Sensors collector reading from sysRoot (normally "/sys").
func NewSensors(sysRoot string, interval, readTimeout time.Duration) *Sensors {
	return &Sensors{sysRoot: sysRoot, interval: interval, readTimeout: readTimeout}
}

// Name implements Collector.
func (s *Sensors) Name() string { return "sensors" }

// Interval implements Collector.
func (s *Sensors) Interval() time.Duration { return s.interval }

// Capabilities implements CapabilityReporter.
//
// "sensors is absent" is a fact the hub needs stated: without it, a host with
// no hwmon and a host whose collector never ran look identical.
func (s *Sensors) Capabilities() map[string]string {
	if s.absent {
		return map[string]string{"sensors": "absent"}
	}
	if len(s.wedged) > 0 {
		// A sensor abandoned for a wedged driver stops producing rows, which
		// is indistinguishable from a sensor that vanished unless it is said
		// out loud. "degraded" rather than "absent": the other chips on this
		// host are still being read.
		return map[string]string{"sensors": "degraded"}
	}
	return nil
}

// Collect implements Collector.
func (s *Sensors) Collect(ctx context.Context) (*Result, error) {
	s.scrapes++

	dir := filepath.Join(s.sysRoot, "class", "hwmon")
	entries, err := s.readDir(ctx, dir)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, errWedged) {
			// NOT absent: the host has hwmon, it is the tree that will not
			// answer. Saying "absent" here would report a hardware fault as a
			// host that simply has no sensors.
			return &Result{}, nil
		}
		if os.IsNotExist(err) {
			// An absent subsystem, not a failure: most VPSes expose no hwmon,
			// and erroring every 60s would be noise an operator learns to
			// ignore -- which is worse than silence.
			s.absent = true
			return &Result{}, nil
		}
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	s.absent = len(entries) == 0

	ts := time.Now().UnixMilli()
	var rows []*netrav1.SensorSample

	for _, e := range entries {
		chipDir := filepath.Join(dir, e.Name())

		chip, err := s.readTrimmed(ctx, filepath.Join(chipDir, "name"))
		if err != nil || chip == "" {
			// No name means no stable chip identity. Reporting it under the
			// directory name would reintroduce exactly the hwmonN keying this
			// collector exists to avoid.
			continue
		}

		labels, err := s.readDir(ctx, chipDir)
		if err != nil {
			continue
		}

		names := make([]string, 0, len(labels))
		for _, f := range labels {
			if strings.HasPrefix(f.Name(), "temp") && strings.HasSuffix(f.Name(), "_label") {
				names = append(names, f.Name())
			}
		}
		// Deterministic order so failures read the same way twice.
		slices.Sort(names)

		for _, labelFile := range names {
			label, err := s.readTrimmed(ctx, filepath.Join(chipDir, labelFile))
			if err != nil || label == "" {
				continue
			}

			// temp1_label -> temp1_input
			inputFile := strings.TrimSuffix(labelFile, "_label") + "_input"
			raw, err := s.readTrimmed(ctx, filepath.Join(chipDir, inputFile))
			if err != nil {
				continue
			}
			milli, err := strconv.ParseFloat(raw, 64)
			if err != nil {
				continue
			}

			rows = append(rows, &netrav1.SensorSample{
				TsMs:  ts,
				Chip:  chip,
				Label: label,
				Temp:  ptrTo(milli / milliDegrees),
			})
		}
	}

	return &Result{Sensors: rows}, nil
}

// errWedged is returned for a path whose read has already timed out once.
var errWedged = errors.New("path previously timed out; not read again")

// deadlined runs fn on its own goroutine and returns either its result or the
// deadline's error, whichever comes first.
//
// It does NOT cancel fn, and cannot. A read(2) blocked in the kernel on a
// wedged hwmon driver is uninterruptible from userspace: there is no way to
// take the call back, so the goroutine and its stack stay resident until the
// driver returns or the process exits. Closing the file from another goroutine
// does not unblock it either.
//
// What the deadline buys is therefore narrower than "the read is abandoned
// safely": it buys the SCRAPE LOOP not being the thing left waiting, so every
// other collector still runs on time. The abandoned goroutine is a real leak,
// and bounding it is the caller's job -- see the wedged set below, which is
// what keeps it to one goroutine per stuck file rather than one per stuck file
// per scrape, forever.
//
// The channel is buffered so a read that finishes after the deadline can still
// send and exit, rather than blocking on a receiver that is long gone.
func deadlined[T any](ctx context.Context, timeout time.Duration, fn func() (T, error)) (T, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	type result struct {
		val T
		err error
	}
	done := make(chan result, 1)

	go func() {
		v, err := fn()
		done <- result{v, err}
	}()

	select {
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	case r := <-done:
		return r.val, r.err
	}
}

// markWedged records a path whose read did not return in time and schedules
// when it may be tried again.
//
// This is what makes the deadline's cost bounded. Without it, a driver stuck
// forever -- the failure mode this whole mechanism exists for -- strands one
// goroutine every 60s for the life of the agent.
func (s *Sensors) markWedged(path string) {
	if s.wedged == nil {
		s.wedged = make(map[string]*wedgedPath)
	}

	w := s.wedged[path]
	if w == nil {
		w = &wedgedPath{}
		s.wedged[path] = w
	}
	w.failures++

	backoff := uint64(1) << min(w.failures-1, 10)
	if backoff > maxWedgedBackoff {
		backoff = maxWedgedBackoff
	}
	w.retryAt = s.scrapes + backoff

	slog.Warn("hwmon read timed out; backing off this path",
		"path", path, "timeout", s.readTimeout,
		"failures", w.failures, "skipping_scrapes", backoff)
}

// skipWedged reports whether path is still inside its backoff window.
func (s *Sensors) skipWedged(path string) bool {
	w := s.wedged[path]
	return w != nil && s.scrapes < w.retryAt
}

// clearWedged forgets a path's backoff after a read that succeeded, so a
// sensor that recovers is not held at a seventeen-hour cadence forever.
func (s *Sensors) clearWedged(path string) {
	if s.wedged[path] != nil {
		slog.Info("hwmon path recovered", "path", path)
		delete(s.wedged, path)
	}
}

// readTrimmed reads a sysfs file under a deadline and trims its trailing
// newline.
func (s *Sensors) readTrimmed(ctx context.Context, path string) (string, error) {
	if s.skipWedged(path) {
		return "", errWedged
	}

	data, err := deadlined(ctx, s.readTimeout, func() ([]byte, error) {
		return os.ReadFile(path)
	})
	if errors.Is(err, context.DeadlineExceeded) {
		s.markWedged(path)
		return "", fmt.Errorf("read %s: %w", path, err)
	}

	// Cleared on ANY non-timeout outcome, not just success. A read that
	// returns ENOENT answered -- the path is gone, which is the opposite of
	// wedged. Clearing only on success left the entry behind forever for a
	// chip that timed out once and was then unbound or renumbered by a driver
	// rebind, so Capabilities() reported "degraded" for the life of the agent
	// with no sensor actually failing.
	s.clearWedged(path)

	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// readDir lists a directory under the same deadline as a file read.
//
// The hwmon tree is sysfs, so a ReadDir on it is as capable of blocking on a
// wedged driver as a read of one of its files -- and this one is worse,
// because it happens before any per-file deadline could apply and would hold
// the entire scrape loop.
func (s *Sensors) readDir(ctx context.Context, path string) ([]os.DirEntry, error) {
	if s.skipWedged(path) {
		return nil, errWedged
	}

	entries, err := deadlined(ctx, s.readTimeout, func() ([]os.DirEntry, error) {
		return os.ReadDir(path)
	})
	if errors.Is(err, context.DeadlineExceeded) {
		s.markWedged(path)
		return nil, fmt.Errorf("read dir %s: %w", path, err)
	}

	// Any non-timeout outcome clears the backoff, for the reason readTrimmed
	// gives: a directory that reports ENOENT is answering, not wedged.
	s.clearWedged(path)
	return entries, err
}
