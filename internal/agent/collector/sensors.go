package collector

import (
	"context"
	"fmt"
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
}

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
	return nil
}

// Collect implements Collector.
func (s *Sensors) Collect(ctx context.Context) (*Result, error) {
	dir := filepath.Join(s.sysRoot, "class", "hwmon")
	entries, err := os.ReadDir(dir)
	if err != nil {
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

		labels, err := os.ReadDir(chipDir)
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

// readTrimmed reads a sysfs file under a deadline and trims its trailing
// newline.
//
// The deadline is the point: a wedged hwmon driver blocks read(2) forever, and
// a collector without one would hold the scrape loop -- and therefore every
// other collector -- for as long as the driver stayed stuck.
func (s *Sensors) readTrimmed(ctx context.Context, path string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, s.readTimeout)
	defer cancel()

	type result struct {
		data []byte
		err  error
	}
	done := make(chan result, 1)

	go func() {
		data, err := os.ReadFile(path)
		done <- result{data, err}
	}()

	select {
	case <-ctx.Done():
		return "", fmt.Errorf("read %s: %w", path, ctx.Err())
	case r := <-done:
		if r.err != nil {
			return "", r.err
		}
		return strings.TrimSpace(string(r.data)), nil
	}
}
