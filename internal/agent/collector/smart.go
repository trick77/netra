package collector

import (
	"context"
	"encoding/json"
	"os/exec"
	"slices"
	"strings"
	"time"

	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

// SmartRunner runs smartctl and returns its stdout.
//
// Injected so the collector is testable without drives, without root and
// without smartctl installed on the machine running the tests.
type SmartRunner func(ctx context.Context, args ...string) ([]byte, error)

// SystemSmartctl is the production SmartRunner.
//
// Non-zero exit is NOT treated as failure: smartctl uses its exit status as a
// bitfield -- bit 0 is a command-line error, but bits 2 and above report drive
// conditions like "some SMART attribute is below threshold", which is exactly
// the case netra exists to notice. Failing on those would blind the collector
// to failing drives.
func SystemSmartctl(ctx context.Context, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, "smartctl", args...).Output()
	if len(out) > 0 {
		return out, nil
	}
	return out, err
}

// smartctlScan is the shape of `smartctl --json --scan`.
type smartctlScan struct {
	Devices []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"devices"`
}

// smartctlDevice is the subset of `smartctl --json --all DEV` netra reads.
type smartctlDevice struct {
	ModelName          string `json:"model_name"`
	SerialNumber       string `json:"serial_number"`
	AtaSmartAttributes struct {
		Table []struct {
			ID    int    `json:"id"`
			Name  string `json:"name"`
			Value int    `json:"value"`
			Raw   struct {
				Value int64 `json:"value"`
			} `json:"raw"`
		} `json:"table"`
	} `json:"ata_smart_attributes"`
	// NVMe drives report a fixed health log rather than the ATA table.
	NvmeSmartHealthInformationLog map[string]json.RawMessage `json:"nvme_smart_health_information_log"`
}

// Smart reports SMART attributes per drive.
//
// Runs on a long interval (1h by default) rather than the scrape interval: the
// values change slowly, smartctl spins up sleeping drives, and it is one of
// only two collectors permitted a non-default cadence because it writes its
// own table and contributes nothing to host_samples.
//
// The attribute set is deliberately generic (attr_id, raw, normalized): SMART
// attributes vary per drive model, so a typed field per attribute would need a
// schema change for every new drive (spec §5.3).
type Smart struct {
	interval time.Duration
	run      SmartRunner

	now     func() time.Time
	lastRun time.Time
	hasRun  bool

	// failures counts consecutive failed scans, which sets how long to wait
	// before the next attempt. Reset to zero by any successful scan.
	failures int

	unavailable bool
}

// failureBackoff is the wait after a failed --scan, doubling per consecutive
// failure up to the collector's own interval.
//
// A transient failure -- smartctl not yet installed, a device node appearing
// late in boot, a momentary EBUSY -- must not cost a full hour of SMART data
// and pin the no-device-access capability for that hour, which is what setting
// lastRun before the run did. But a host where SMART is permanently
// unavailable must not be probed every 60s either.
//
// Retrying `--scan` is cheap in the way that matters: it enumerates devices
// and does NOT wake sleeping drives. Only `--all DEV` spins a drive up, and
// that only runs for devices a successful scan returned. So the fast retry
// costs nothing on the host where it fires most often -- the one with no
// drives to wake.
const failureBackoff = time.Minute

// NewSmart builds a Smart collector.
func NewSmart(interval time.Duration, run SmartRunner) *Smart {
	return &Smart{interval: interval, run: run, now: time.Now}
}

// SetClockForTest replaces the clock used for the interval gate.
func (s *Smart) SetClockForTest(fn func() time.Time) { s.now = fn }

// due reports whether the interval has elapsed since the last run.
//
// The collector gates ITSELF rather than relying on the scrape loop, which
// runs every collector on every tick. Without this, smartctl would spin up
// every sleeping drive on the host once a minute -- which shortens their life
// and is exactly the behaviour a monitoring agent must not have.
//
// Self-gating is safe here, and only here, because SMART writes its own table
// and contributes nothing to host_samples: a scrape that skips it leaves no
// column NULL, so nothing reads as an absent subsystem.
func (s *Smart) due() bool {
	if !s.hasRun {
		return true
	}
	return s.now().Sub(s.lastRun) >= s.wait()
}

// wait is how long to hold off before the next attempt: the full interval
// after a success, an exponentially growing but interval-capped delay after
// consecutive failures.
//
// Capping at the interval is what keeps a host with no smartctl at all from
// being probed more often than a host with working drives. It converges there
// after six failures rather than costing an hour after the first one.
func (s *Smart) wait() time.Duration {
	if s.failures == 0 {
		return s.interval
	}

	backoff := failureBackoff << min(s.failures-1, 16)
	if backoff <= 0 || backoff > s.interval {
		// Also catches the shift overflowing into a negative duration on a
		// host that has been failing for a very long time.
		return s.interval
	}
	return backoff
}

// Name implements Collector.
func (s *Smart) Name() string { return "smart" }

// Interval implements Collector.
func (s *Smart) Interval() time.Duration { return s.interval }

// Capabilities implements CapabilityReporter.
//
// Missing device access is reported rather than treated as failure: an agent
// without the device cgroup rule is correctly configured for a host whose
// operator declined to grant it, and "no SMART data" must be distinguishable
// from "no drives".
func (s *Smart) Capabilities() map[string]string {
	if s.unavailable {
		return map[string]string{"smart": "no-device-access"}
	}
	return nil
}

// fail records a scan that did not produce a device list, so the next attempt
// comes sooner than the full interval.
func (s *Smart) fail() {
	s.lastRun, s.hasRun = s.now(), true
	s.failures++
	s.unavailable = true
}

// Collect implements Collector.
func (s *Smart) Collect(ctx context.Context) (*Result, error) {
	if !s.due() {
		return &Result{}, nil
	}

	// lastRun is stamped AFTER the scan, together with the outcome. Stamping
	// it first made every failure cost a full interval: one transient --scan
	// error lost an hour of SMART data and pinned the no-device-access
	// capability for that hour, even though the next scrape would have
	// succeeded.
	raw, err := s.run(ctx, "--json", "--scan")
	if err != nil {
		s.fail()
		return &Result{}, nil
	}

	var scan smartctlScan
	if err := json.Unmarshal(raw, &scan); err != nil {
		s.fail()
		return &Result{}, nil
	}

	s.lastRun, s.hasRun = s.now(), true
	s.failures, s.unavailable = 0, false

	ts := time.Now().UnixMilli()
	var rows []*netrav1.SmartAttribute

	for _, dev := range scan.Devices {
		args := []string{"--json", "--all", dev.Name}
		if dev.Type != "" {
			args = append(args, "-d", dev.Type)
		}

		out, err := s.run(ctx, args...)
		if err != nil {
			// One unreadable drive must not cost the others their reading.
			continue
		}

		var d smartctlDevice
		if err := json.Unmarshal(out, &d); err != nil {
			continue
		}

		name := strings.TrimPrefix(dev.Name, "/dev/")
		for _, attr := range d.AtaSmartAttributes.Table {
			rows = append(rows, &netrav1.SmartAttribute{
				TsMs:       ts,
				Device:     name,
				Model:      d.ModelName,
				Serial:     d.SerialNumber,
				AttrId:     uint32(attr.ID),
				Raw:        ptrTo(attr.Raw.Value),
				Normalized: ptrTo(uint32(attr.Value)),
			})
		}
	}

	// Deterministic order so failures read the same way twice.
	slices.SortFunc(rows, func(a, b *netrav1.SmartAttribute) int {
		if c := strings.Compare(a.GetDevice(), b.GetDevice()); c != 0 {
			return c
		}
		return int(a.GetAttrId()) - int(b.GetAttrId())
	})

	return &Result{Smart: rows}, nil
}
