package collector

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os/exec"
	"path/filepath"
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

	// Smartctl carries the run's own verdict on whether it reached the drive.
	//
	// Needed because a smartctl that could NOT open the device still exits
	// having written a complete JSON document, and SystemSmartctl treats
	// output-with-content as success on purpose (see its comment: the exit
	// status is a bitfield whose upper bits are drive health, and failing on
	// those would blind netra to failing drives). So `err == nil` and a clean
	// unmarshal are not evidence that anything was read.
	Smartctl struct {
		ExitStatus int `json:"exit_status"`
	} `json:"smartctl"`
}

// exitNotRead are the smartctl exit bits that mean no reading was obtained:
// bit 0, the command line did not parse, and bit 1, the device could not be
// opened or would not return an IDENTIFY structure.
//
// Deliberately NOT bit 2 and above. Bit 2 is a failed sub-command, which a
// drive that returned a perfectly good attribute table can still set, and bits
// 3+ are health verdicts -- "DISK FAILING" is the single most important thing
// this collector can ever be told, and treating it as a read failure would
// discard exactly the reading netra exists to take.
const exitNotRead = 0x03

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
	sysRoot  string

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

	// usbSkipped is the device list the previous scan left alone for being
	// USB-attached, so the log fires on the transition rather than every
	// interval forever.
	usbSkipped []string

	// usbOnly is a scan that found devices and skipped every one of them for
	// being USB-attached. The host has drives; this collector will not drive
	// them. A state of its own because the remedy is not any of the others:
	// nothing is misconfigured and nothing failed.
	usbOnly bool

	// noAttributes is a scan whose devices all ANSWERED and not one of them
	// carried a reading this collector can store: no ata_smart_attributes
	// table, no NVMe health log.
	//
	// The quietest state there is, and the one that hid the -d scsi bug for as
	// long as it existed. Nothing fails on this path -- every --all succeeds
	// and parses -- so unavailable, noDevices, noReadableDevices and usbOnly
	// were all false, Capabilities() returned nil, and a host reporting no
	// drives at all was indistinguishable from one whose first hourly reading
	// had simply not landed yet. It is also a real, permanent state on a
	// genuine SAS host, whose drives answer with SCSI health pages rather than
	// an attribute table -- so it has to be sayable rather than merely fixed.
	noAttributes bool
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
//
// sysRoot is where /sys is readable. It is used for one thing: telling a
// USB-attached drive from a directly attached one. Empty disables that check,
// which is what the tests pass -- a collector that filtered on a sysfs it
// cannot read would drop every drive on the host.
func NewSmart(interval time.Duration, run SmartRunner, sysRoot string) *Smart {
	return &Smart{interval: interval, run: run, sysRoot: sysRoot, now: time.Now}
}

// usbAttached reports whether a scanned device hangs off a USB bridge.
//
// Driving one with `-d sat` is unreliable and can hang the enclosure, and a
// hung enclosure does not die on SIGKILL -- it stalls the scrape until
// smartctlWaitDelay gives up on it, once per drive, every interval. The setup
// script used to keep these out by leaving them out of the devices: list it
// computed. The agent finds its own drives now, so the exclusion has to live
// here or not at all.
//
// sysfs, not smartctl: `--scan` reports a transport TYPE (sat, scsi, nvme),
// which a USB bridge shares with the directly attached drives it emulates.
// /sys/block/<name> is a symlink into the devices tree whose path names the
// bus -- .../usb1/1-1/... -- which is the same signal the setup script read.
//
// Only /dev/sdX is checked. A USB bridge presents as a SCSI disk and nothing
// else; an NVMe controller or a RAID pseudo-device (/dev/bus/0) has no
// /sys/block entry under that name, and guessing at one would drop drives that
// answer perfectly.
func (s *Smart) usbAttached(devName string) bool {
	if s.sysRoot == "" {
		return false
	}
	base, ok := strings.CutPrefix(devName, "/dev/")
	if !ok || !strings.HasPrefix(base, "sd") {
		return false
	}

	// EvalSymlinks rather than Readlink: /sys/block/sda is a relative link
	// (../devices/...) and only the resolved path names the bus.
	resolved, err := filepath.EvalSymlinks(filepath.Join(s.sysRoot, "block", base))
	if err != nil {
		// Unreadable sysfs is not evidence of USB. Reporting it as such would
		// silently drop every drive the moment the mount changed.
		return false
	}

	// CRITICAL: match on the path RELATIVE to sysRoot. A sysfs root that
	// itself sits under a directory named usb1 -- a fixture tree, a bind
	// mount, an AGENT_SYSFS_ROOT someone picked -- would otherwise classify
	// EVERY sd* drive on the host as USB-attached and silently drop all of
	// them. setup-agent.sh's device_transport strips AGENT_SETUP_ROOT before
	// matching for exactly this reason, and carries a fixture that fails if
	// the strip is ever removed; the port must not lose it.
	//
	// The root is resolved too, because only two resolved paths are
	// comparable: /sys is a symlink on some layouts, and macOS resolves a
	// temp root under /var to /private/var.
	root, err := filepath.EvalSymlinks(s.sysRoot)
	if err != nil {
		root = s.sysRoot
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		// Resolved outside the tree we were pointed at: nothing this check can
		// reason about, and not evidence of USB.
		return false
	}
	return strings.Contains(string(filepath.Separator)+rel, "/usb")
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
	case s.usbOnly:
		return map[string]string{"smart": "usb-only-devices"}
	case s.noReadableDevices:
		return map[string]string{"smart": "no-readable-devices"}
	case s.noAttributes:
		// Last, because it is the furthest the collector gets without
		// producing a row: the drives were found, not skipped, and every one
		// of them answered. Only the reading is missing.
		return map[string]string{"smart": "no-attributes"}
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
	s.noDevices, s.noReadableDevices, s.usbOnly = false, false, false
	s.noAttributes = false
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

// readType is the `-d` to pass to `--all`, or "" to let smartctl decide.
//
// `--scan` finds its devices by globbing /dev (glob(3) over /dev/sd[a-z],
// /dev/nvme[0-9] and friends, in linux_smart_interface::scan_smart_devices)
// and does NOT open them. The `type` it attaches is therefore derived from the
// node's NAME, not from anything the hardware said.
//
// That guess is "scsi" for every /dev/sd*, because libata presents SATA disks
// through the SCSI layer. Passing it back as `-d scsi` FORCES the SCSI command
// path on a drive that speaks ATA: smartctl succeeds, returns SCSI health
// pages, and the ata_smart_attributes table is never read. The collector then
// emits no rows for a drive that answered perfectly -- and with nothing having
// failed there is no capability, no log and an empty Storage tab saying only
// "No drives reported". That was SMART reporting nothing on every ordinary
// SATA host.
//
// Dropped rather than translated to "sat": `--all` opens the device, and an
// opened device can be identified properly, which is exactly what smartctl's
// own auto-detection does with it. Substituting "sat" here would be this
// collector making the same unfounded claim in the other direction, and would
// break a genuine SAS drive that "scsi" describes correctly.
//
// Every other value IS a detection and is passed through. `nvme` and `sat`
// come from a scan that could tell, and a RAID pseudo-device carries the one
// type that cannot be rediscovered from the node -- `/dev/bus/0 -d megaraid,7`
// is unreadable without it.
//
// `--scan-open` would resolve the type properly for all of them, and is not
// used on purpose: it OPENS every device on the host, which spins up sleeping
// drives. The failure path retries every minute (failureBackoff), and its
// whole justification is that `--scan` wakes nothing.
func readType(scanned string) string {
	if scanned == "scsi" {
		return ""
	}
	return scanned
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
	// silent counts devices that answered and parsed and still carried no
	// attribute this collector stores.
	silent := 0
	var skippedUSB []string

	for _, dev := range scan.Devices {
		if s.usbAttached(dev.Name) {
			// Counted as unreadable would be a lie -- nothing was attempted.
			// It is simply not a drive this collector drives.
			skippedUSB = append(skippedUSB, dev.Name)
			continue
		}

		args := []string{"--json", "--all", dev.Name}
		if t := readType(dev.Type); t != "" {
			args = append(args, "-d", t)
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

		// A drive smartctl could not open is unreadable, not quiet. Without
		// this it landed in `silent` below and the host was told
		// "nothing is misconfigured" -- which is the opposite of true for the
		// case that produces it: device nodes present and the device cgroup
		// rules never granted, so every open returns EACCES. That is the exact
		// state setup-agent.sh exists to fix, and it must not be reassured.
		if d.Smartctl.ExitStatus&exitNotRead != 0 {
			unreadable++
			continue
		}

		// Where this device's rows start, so a drive that answered with
		// nothing storable can be counted. Length rather than a bool per
		// branch: an ATA table and an NVMe log are both appended below, and
		// the question is whether EITHER produced anything.
		before := len(rows)

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
		if len(rows) == before {
			silent++
		}
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
	// answering and send its operator after the passthrough. That host has its
	// own state below.
	// Against the devices actually ATTEMPTED. A host whose only disk is a USB
	// enclosure skipped nothing and read nothing, and calling that "no drive
	// answered" would send its operator after a passthrough that is working.
	attempted := len(scan.Devices) - len(skippedUSB)

	// At least one read FAILED and not one drive produced a reading.
	//
	// Not `unreadable == attempted`, which left a hole exactly where the
	// silence was worst: two drives, one timing out and one answering with no
	// attribute table, satisfied neither this nor noAttributes below, so a
	// host with an empty Storage tab got no capability at all -- the state
	// both of these exist to end. Every zero-row outcome is now named by one
	// of the two.
	s.noReadableDevices = attempted > 0 && unreadable > 0 &&
		unreadable+silent == attempted

	// Every device found, every one skipped. Reported as well as logged: the
	// log answers the question on the host, and the capability answers it on
	// the Storage tab of an operator who is not going to read agent logs.
	s.usbOnly = attempted == 0 && len(skippedUSB) > 0

	// Every drive attempted answered, and not one of them carried a reading.
	// The state the SAS caveat above describes, said out loud instead of left
	// to look like silence.
	//
	// silent == attempted, not len(rows) == 0: a host where one drive answers
	// and a second returns SCSI health pages is reporting SMART, and claiming
	// otherwise on behalf of the quiet one would put a capability on a host
	// whose Storage tab has rows in it.
	s.noAttributes = attempted > 0 && silent == attempted

	// A skip nobody is told about is the same silence the empty scan used to
	// be. The setup script used to name the excluded USB drive in its finish
	// report; now that the exclusion happens here, the operator of a host
	// whose only disk is a USB enclosure would otherwise see an empty Storage
	// tab with no capability, no row and nothing anywhere saying why.
	//
	// Logged on the TRANSITION only, for the same reason the empty scan is: a
	// host with a permanently attached enclosure is healthy in this state
	// forever, and a line every interval is noise it would learn to ignore.
	if len(skippedUSB) > 0 && !slices.Equal(skippedUSB, s.usbSkipped) {
		slog.Info("smartctl found USB-attached drives and left them alone",
			"collector", "smart",
			"devices", strings.Join(skippedUSB, ","),
			"attempted", attempted,
			"hint", "driving a USB bridge with -d sat can hang the enclosure and stall the scrape")
	}
	s.usbSkipped = skippedUSB

	// Deterministic order so failures read the same way twice.
	slices.SortFunc(rows, func(a, b *netrav1.SmartAttribute) int {
		if c := strings.Compare(a.GetDevice(), b.GetDevice()); c != 0 {
			return c
		}
		return int(a.GetAttrId()) - int(b.GetAttrId())
	})

	return &Result{Smart: rows}, nil
}
