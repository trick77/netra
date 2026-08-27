package collector

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os/exec"
	"slices"
	"strings"
	"time"

	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
)

// SmartRunner runs smartctl and returns its stdout.
//
// Injected so the collector is testable without drives, without root and
// without smartctl installed on the machine running the tests.
type SmartRunner func(ctx context.Context, args ...string) ([]byte, error)

// smartctlWaitDelay bounds how long Wait may spend after the context is done.
//
// The scrape deadline alone does NOT bound this call, which is the whole reason
// this exists. exec.CommandContext cancels by sending SIGKILL, and a process
// blocked in an uninterruptible ioctl -- a drive that will not answer an ATA
// passthrough, a wedged HBA, a USB-SATA bridge that has stopped responding --
// does not die on SIGKILL until the ioctl returns. .Output() then calls Wait,
// which without a WaitDelay waits indefinitely both for that exit and for the
// stdout pipe to close. So the collector never returned, collect never
// returned, and the deadline achieved nothing on precisely the drive hang it
// was added for.
//
// With a WaitDelay, Wait gives up and returns once the delay has elapsed after
// the context is done. The child is left behind -- there is nothing else to be
// done with a process the kernel will not kill -- exactly as the statfs
// goroutine is left behind in filesystems.go, and for the same reason: the
// scrape loop's liveness is worth more than the stray resource. Smart's own
// failure backoff is what stops it accumulating one per scrape.
const smartctlWaitDelay = 2 * time.Second

// SystemSmartctl is the production SmartRunner.
//
// Non-zero exit is NOT treated as failure: smartctl uses its exit status as a
// bitfield -- bit 0 is a command-line error, but bits 2 and above report drive
// conditions like "some SMART attribute is below threshold", which is exactly
// the case netra exists to notice. Failing on those would blind the collector
// to failing drives.
func SystemSmartctl(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "smartctl", args...)
	cmd.WaitDelay = smartctlWaitDelay

	out, err := cmd.Output()

	// Output-with-content is success only when the run ENDED ON ITS OWN, in
	// which case the rule above holds and a non-zero exit is still a reading.
	//
	// A run the context ended is different: the WaitDelay expiring, or a child
	// SIGKILLed mid-write, hands back whatever the pipe happened to hold -- a
	// half-written JSON document from a scan that never finished. Reporting
	// that as success fed the parser a truncated body and, worse, hid the
	// abandoned child from the caller, which backs off only on an error -- so a
	// drive that wedges after writing its first bytes would strand one
	// unkillable smartctl per run forever, which is exactly the accumulation
	// smartctlWaitDelay's comment says the backoff prevents.
	//
	// Narrowly: err != nil AND the context is done. A scan that COMPLETED just
	// as the scrape deadline fired still succeeds, because failing it would
	// spend Smart's failure backoff on a reading that is perfectly good.
	abandoned := errors.Is(err, exec.ErrWaitDelay) || (err != nil && ctx.Err() != nil)
	if len(out) > 0 && !abandoned {
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

// nvmeAttrs maps the NVMe health log keys netra reports onto synthetic
// attr_ids, in the order they are emitted.
//
// SYNTHETIC because NVMe has no attribute ids: the health log is a fixed
// struct of named fields, where ATA has a per-model table of numbered
// attributes. The two have to share smart_attributes, so the names need
// numbers.
//
// The range starts at 1000 for two reasons. ATA attribute ids are 1-255, so
// nothing here can collide with a real one whatever drive turns up. And the
// hub's column is SMALLINT, which its insert casts to int16 -- so the ids must
// also stay well under 32767 or they would wrap negative on the way into the
// database.
//
// Only fields an operator would act on. The health log also carries cumulative
// data_units_read/written and host_reads/writes, which are throughput
// accounting rather than health, and belong in disk_io_samples if anywhere.
var nvmeAttrs = []struct {
	key string
	id  uint32
}{
	// Bitfield: any non-zero bit is the drive telling the host it is in
	// trouble. First because it is the one field that is a verdict rather
	// than a reading.
	{"critical_warning", 1000},
	// Percent of rated write endurance consumed. Passes 100 before the drive
	// refuses writes, so it is the field that gives warning rather than news.
	{"percentage_used", 1001},
	// Remaining spare blocks, and the threshold the drive itself considers
	// critical. Reported as a pair: the percentage means nothing without the
	// line it is being compared against, which varies per model.
	{"available_spare", 1002},
	{"available_spare_threshold", 1003},
	// Uncorrected data-integrity errors. Non-zero is always worth a look.
	{"media_errors", 1004},
	// Power lost without a clean shutdown -- context for media_errors, and a
	// PSU or host problem in its own right when it climbs.
	{"unsafe_shutdowns", 1005},
	{"power_on_hours", 1006},
	{"power_cycles", 1007},
	// Degrees Celsius in smartctl's JSON, unlike the raw log's Kelvin.
	{"temperature", 1008},
	{"num_err_log_entries", 1009},
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

	// noDevices is a scan that RAN and returned an empty device list. It is a
	// different fact from unavailable and needs its own flag: smartctl worked,
	// so nothing failed, yet the host has nothing to read. On a container
	// agent that is almost always a missing devices: mapping rather than a
	// machine without disks, and until this flag existed the collector
	// reported the two the same way -- as silence.
	noDevices bool

	// noReadableDevices is a scan that found devices of which not one produced
	// a single attribute row: every --all failed, timed out or would not
	// parse. The drives are there and none of them answered.
	noReadableDevices bool
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

// EmitsBaseline implements BaselineEmitter, keeping this collector out of the
// agent's startup priming.
//
// Its first Collect is a full scan plus a --all per drive, and it stamps
// lastRun on success -- so priming would spin up every sleeping drive on the
// host to produce a reading that is then thrown away, AND start the hour-long
// interval, costing the first real hour of SMART data on every agent restart.
// That is the exact opposite of what the due() self-gate exists to prevent.
func (s *Smart) EmitsBaseline() bool { return true }

// Name implements Collector.
func (s *Smart) Name() string { return "smart" }

// Capabilities implements CapabilityReporter.
//
// Missing device access is reported rather than treated as failure: an agent
// without the device cgroup rule is correctly configured for a host whose
// operator declined to grant it, and "no SMART data" must be distinguishable
// from "no drives".
func (s *Smart) Capabilities() map[string]string {
	// Ordered by how far the collector got, so the value names the FIRST thing
	// that stopped it: smartctl would not run, or it ran and found nothing, or
	// it found drives that would not answer. Reporting a later state while an
	// earlier one holds would send the operator to the wrong remedy.
	switch {
	case s.unavailable:
		return map[string]string{"smart": "no-device-access"}
	case s.noDevices:
		return map[string]string{"smart": "no-devices"}
	case s.noReadableDevices:
		return map[string]string{"smart": "no-readable-devices"}
	}
	return nil
}

// fail records a scan that did not produce a device list, so the next attempt
// comes sooner than the full interval.
func (s *Smart) fail() {
	s.lastRun, s.hasRun = s.now(), true
	s.failures++
	s.unavailable = true
	// Cleared, not left standing: a scan that no longer runs at all cannot
	// still be asserting what it did or did not find last hour, and
	// Capabilities reports the first state that holds -- so a stale flag here
	// would be invisible rather than wrong, which is worse.
	s.noDevices, s.noReadableDevices = false, false
}

// nvmeRows turns an NVMe drive's health log into attribute rows.
//
// Without this the collector was blind to NVMe entirely: the health log was
// unmarshalled and then never read, so an all-NVMe host -- most modern servers
// -- ran smartctl against every drive on the interval, spun them up, and
// emitted nothing at all.
//
// Normalized is deliberately left UNSET. It is ATA's 1-253 vendor scale
// against a failure threshold, and NVMe has no equivalent; inventing one from
// the raw value would be this collector asserting a health verdict it has no
// basis for. Unset means "not measured", which is this codebase's rule and the
// honest answer.
//
// A key the drive does not publish, or publishes as something other than a
// number -- temperature_sensors is an array on some firmware -- is skipped
// rather than defaulted. A missing field is an absent fact, and a zero would
// read as a measured one.
func nvmeRows(ts int64, device string, d smartctlDevice) []*netrav1.SmartAttribute {
	if len(d.NvmeSmartHealthInformationLog) == 0 {
		return nil
	}

	rows := make([]*netrav1.SmartAttribute, 0, len(nvmeAttrs))
	for _, attr := range nvmeAttrs {
		raw, ok := d.NvmeSmartHealthInformationLog[attr.key]
		if !ok {
			continue
		}
		var v int64
		if err := json.Unmarshal(raw, &v); err != nil {
			continue
		}
		rows = append(rows, &netrav1.SmartAttribute{
			TsMs:   ts,
			Device: device,
			Model:  d.ModelName,
			Serial: d.SerialNumber,
			AttrId: attr.id,
			Raw:    ptrTo(v),
		})
	}
	return rows
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

	// An empty scan is the quietest way SMART goes missing, and it was silent:
	// no rows, no capability, no log. On a container agent it means no device
	// was passed through -- the host's Storage tab then said "no drives
	// reported" and nothing anywhere said why. Logged as well as reported,
	// because the log answers the question without a round trip through the
	// hub and the UI.
	//
	// Recorded BEFORE the per-device loop so the two states are decided in the
	// order they are discovered, and cleared here on the way past so a host
	// whose passthrough was just fixed stops claiming it has none.
	//
	// Logged on the TRANSITION only. A legitimately diskless VPS is a healthy
	// host in this state permanently, and a line every interval forever is
	// noise it would learn to ignore -- including on the hour it stops being
	// true.
	empty := len(scan.Devices) == 0
	if empty && !s.noDevices {
		slog.Info("smartctl found no devices to read",
			"collector", "smart",
			"hint", "a container agent needs the device mapped in; see setup-agent.sh")
	}
	s.noDevices = empty

	ts := time.Now().UnixMilli()
	var rows []*netrav1.SmartAttribute
	unreadable := 0

	for _, dev := range scan.Devices {
		args := []string{"--json", "--all", dev.Name}
		if dev.Type != "" {
			args = append(args, "-d", dev.Type)
		}

		out, err := s.run(ctx, args...)
		if err != nil {
			// One unreadable drive must not cost the others their reading.
			unreadable++
			continue
		}

		var d smartctlDevice
		if err := json.Unmarshal(out, &d); err != nil {
			unreadable++
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
		rows = append(rows, nvmeRows(ts, name, d)...)
	}

	// Drives were found and the --all on every one of them failed, timed out
	// or would not parse. A different fault from an empty scan and a different
	// remedy, so it gets its own value rather than sharing one.
	//
	// Counted from the reads that FAILED rather than from len(rows) == 0. A
	// SAS drive answers --all perfectly and returns a SCSI error counter log,
	// which carries no ata_smart_attributes table and no NVMe health log --
	// so it produces no rows through no fault of anything, and keying on the
	// row count would tell a healthy SAS host its drives had stopped
	// answering and send its operator after the passthrough.
	s.noReadableDevices = len(scan.Devices) > 0 && unreadable == len(scan.Devices)

	// Deterministic order so failures read the same way twice.
	slices.SortFunc(rows, func(a, b *netrav1.SmartAttribute) int {
		if c := strings.Compare(a.GetDevice(), b.GetDevice()); c != 0 {
			return c
		}
		return int(a.GetAttrId()) - int(b.GetAttrId())
	})

	return &Result{Smart: rows}, nil
}
