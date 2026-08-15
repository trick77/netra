package collector_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/trick77/netra/internal/agent/collector"
	netrav1 "github.com/trick77/netra/internal/shared/gen/netra/v1"
)

const scanJSON = `{"devices":[{"name":"/dev/sda","type":"sat"}]}`

const deviceJSON = `{
  "model_name": "Samsung SSD 870",
  "serial_number": "S123456",
  "ata_smart_attributes": {
    "table": [
      {"id": 5,   "name": "Reallocated_Sector_Ct", "value": 100, "raw": {"value": 0}},
      {"id": 194, "name": "Temperature_Celsius",   "value": 65,  "raw": {"value": 35}},
      {"id": 9,   "name": "Power_On_Hours",        "value": 98,  "raw": {"value": 12345}}
    ]
  }
}`

// fakeSmartctl answers --scan and --all from canned JSON.
func fakeSmartctl(scan, device string) collector.SmartRunner {
	return func(_ context.Context, args ...string) ([]byte, error) {
		for _, a := range args {
			if a == "--scan" {
				return []byte(scan), nil
			}
		}
		return []byte(device), nil
	}
}

func TestSmartReportsEveryAttributePerDrive(t *testing.T) {
	testee := collector.NewSmart(time.Hour, fakeSmartctl(scanJSON, deviceJSON))

	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(res.Smart) != 3 {
		t.Fatalf("attributes = %d, want 3", len(res.Smart))
	}

	byID := map[uint32]int64{}
	for _, a := range res.Smart {
		byID[a.GetAttrId()] = a.GetRaw()
		if a.GetDevice() != "sda" {
			t.Errorf("device = %q, want sda (without the /dev/ prefix)", a.GetDevice())
		}
		if a.GetModel() != "Samsung SSD 870" {
			t.Errorf("model = %q", a.GetModel())
		}
		if a.GetTsMs() == 0 {
			t.Error("attribute carries no ts_ms")
		}
	}

	// Reallocated sectors and power-on hours are the two an operator acts on,
	// and they must carry the RAW value -- the normalized one is a
	// vendor-scaled 0-100 score that says nothing about how many sectors went.
	if got := byID[5]; got != 0 {
		t.Errorf("attr 5 raw = %d, want 0", got)
	}
	if got := byID[9]; got != 12345 {
		t.Errorf("attr 9 raw = %d, want 12345 (hours, not the normalized score)", got)
	}

	for _, a := range res.Smart {
		if a.GetAttrId() == 9 && a.GetNormalized() != 98 {
			t.Errorf("attr 9 normalized = %d, want 98", a.GetNormalized())
		}
	}
}

// Missing device access is a capability, not a failure. An agent without the
// device cgroup rule is correctly configured for a host whose operator
// declined to grant it -- and "no SMART data" must be distinguishable from
// "this host has no drives".
func TestSmartReportsNoDeviceAccessAsACapability(t *testing.T) {
	failing := func(context.Context, ...string) ([]byte, error) {
		return nil, errors.New("permission denied")
	}
	testee := collector.NewSmart(time.Hour, failing)

	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v, want no error when access is denied", err)
	}
	if len(res.Smart) != 0 {
		t.Errorf("attributes = %d, want 0", len(res.Smart))
	}
	if got := testee.Capabilities()["smart"]; got != "no-device-access" {
		t.Errorf("capability = %q, want no-device-access", got)
	}
}

// One unreadable drive must not cost the others their reading: a failing drive
// is exactly when the remaining drives matter most.
func TestSmartSkipsOneUnreadableDriveAndKeepsTheRest(t *testing.T) {
	twoDrives := `{"devices":[{"name":"/dev/sda","type":"sat"},{"name":"/dev/sdb","type":"sat"}]}`
	run := func(_ context.Context, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "--scan"):
			return []byte(twoDrives), nil
		case strings.Contains(joined, "/dev/sdb"):
			return nil, errors.New("device is failing to respond")
		default:
			return []byte(deviceJSON), nil
		}
	}
	testee := collector.NewSmart(time.Hour, run)

	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(res.Smart) != 3 {
		t.Fatalf("attributes = %d, want 3 (sda's, with sdb skipped)", len(res.Smart))
	}
	for _, a := range res.Smart {
		if a.GetDevice() == "sdb" {
			t.Error("sdb reported despite failing to respond")
		}
	}
}

// A host with no drives smartctl can see -- a VPS on virtio -- reports nothing
// and no error.
func TestSmartReportsNothingWhenNoDrivesAreFound(t *testing.T) {
	testee := collector.NewSmart(time.Hour, fakeSmartctl(`{"devices":[]}`, ""))

	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(res.Smart) != 0 {
		t.Errorf("attributes = %d, want 0", len(res.Smart))
	}
}

// Unparseable output is treated as unavailable rather than as a crash: an old
// smartctl without --json prints human-readable text, and the agent must keep
// running.
func TestSmartTreatsUnparseableOutputAsUnavailable(t *testing.T) {
	testee := collector.NewSmart(time.Hour, func(context.Context, ...string) ([]byte, error) {
		return []byte("smartctl 6.6 2016-05-31 r4324\nUnknown option --json\n"), nil
	})

	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v, want no error for unparseable output", err)
	}
	if len(res.Smart) != 0 {
		t.Errorf("attributes = %d, want 0", len(res.Smart))
	}
	if got := testee.Capabilities()["smart"]; got != "no-device-access" {
		t.Errorf("capability = %q, want no-device-access", got)
	}
}

// SMART gates ITSELF rather than relying on the scrape loop, which runs every
// collector on every tick. Without the gate smartctl would spin up every
// sleeping drive on the host once a minute, shortening their life -- exactly
// what a monitoring agent must not do.
//
// Self-gating is safe here because SMART writes its own table and contributes
// nothing to host_samples: a skipped scrape leaves no column NULL, so nothing
// reads as an absent subsystem.
func TestSmartRunsOnlyOncePerInterval(t *testing.T) {
	var runs int
	counting := func(_ context.Context, args ...string) ([]byte, error) {
		for _, a := range args {
			if a == "--scan" {
				runs++
				return []byte(scanJSON), nil
			}
		}
		return []byte(deviceJSON), nil
	}

	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	testee := collector.NewSmart(time.Hour, counting)
	testee.SetClockForTest(func() time.Time { return base })

	if _, err := testee.Collect(context.Background()); err != nil {
		t.Fatalf("first Collect: %v", err)
	}
	if runs != 1 {
		t.Fatalf("smartctl runs after the first scrape = %d, want 1", runs)
	}

	// Sixty scrapes over the next hour, as the 60s loop would deliver.
	for i := 1; i <= 59; i++ {
		testee.SetClockForTest(func() time.Time { return base.Add(time.Duration(i) * time.Minute) })
		if _, err := testee.Collect(context.Background()); err != nil {
			t.Fatalf("Collect %d: %v", i, err)
		}
	}
	if runs != 1 {
		t.Errorf("smartctl runs within the interval = %d, want 1 -- drives must not be woken every minute", runs)
	}

	// Past the interval, it runs again.
	testee.SetClockForTest(func() time.Time { return base.Add(time.Hour) })
	if _, err := testee.Collect(context.Background()); err != nil {
		t.Fatalf("Collect after the interval: %v", err)
	}
	if runs != 2 {
		t.Errorf("smartctl runs after the interval elapsed = %d, want 2", runs)
	}
}

// A transient scan failure must not cost a full hour of SMART data.
//
// lastRun used to be stamped BEFORE the run, so a single failed --scan --
// smartctl not yet installed, a device node appearing late in boot, a
// momentary EBUSY -- consumed the whole interval and pinned the
// no-device-access capability for that hour, even though the very next scrape
// would have succeeded.
func TestSmartRetriesSoonAfterAFailedScan(t *testing.T) {
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	now := base

	var calls int
	testee := collector.NewSmart(time.Hour, func(_ context.Context, args ...string) ([]byte, error) {
		if args[1] == "--scan" {
			calls++
			if calls == 1 {
				return nil, errors.New("smartctl: transient failure")
			}
			return []byte(`{"devices":[{"name":"/dev/sda","type":"sat"}]}`), nil
		}
		return []byte(`{"model_name":"Samsung","serial_number":"S1",
			"ata_smart_attributes":{"table":[{"id":5,"value":100,"raw":{"value":0}}]}}`), nil
	})
	testee.SetClockForTest(func() time.Time { return now })

	if _, err := testee.Collect(context.Background()); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if got := testee.Capabilities()["smart"]; got != "no-device-access" {
		t.Errorf("capability = %q after a failed scan, want no-device-access", got)
	}

	// A minute later -- far short of the hour interval -- it must try again.
	now = base.Add(time.Minute)
	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("second Collect: %v", err)
	}
	if calls != 2 {
		t.Fatalf("scan calls = %d, want 2 -- a failed scan must be retried well before the full interval", calls)
	}
	if len(res.Smart) == 0 {
		t.Error("no attributes after the retry succeeded")
	}
	if testee.Capabilities() != nil {
		t.Errorf("capability = %v after a successful scan, want none", testee.Capabilities())
	}
}

// A success must restore the full interval, so a working host is not scanned
// every minute for the rest of its uptime.
func TestSmartReturnsToTheFullIntervalAfterASuccess(t *testing.T) {
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	now := base

	var calls int
	testee := collector.NewSmart(time.Hour, func(_ context.Context, args ...string) ([]byte, error) {
		if args[1] == "--scan" {
			calls++
			return []byte(`{"devices":[]}`), nil
		}
		return nil, errors.New("unexpected")
	})
	testee.SetClockForTest(func() time.Time { return now })

	if _, err := testee.Collect(context.Background()); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	// Ten minutes on, the hour has not elapsed and nothing should run.
	now = base.Add(10 * time.Minute)
	if _, err := testee.Collect(context.Background()); err != nil {
		t.Fatalf("second Collect: %v", err)
	}
	if calls != 1 {
		t.Errorf("scan calls = %d, want 1 -- a successful scan must wait the full interval", calls)
	}
}

// A host where SMART is permanently unavailable must settle back to the full
// interval rather than probing forever at the retry cadence.
//
// The backoff doubles per consecutive failure and is capped at the interval,
// so a host with no smartctl at all ends up scanned exactly as often as a host
// with working drives.
func TestSmartBacksOffOnRepeatedFailures(t *testing.T) {
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	now := base

	var calls int
	testee := collector.NewSmart(time.Hour, func(context.Context, ...string) ([]byte, error) {
		calls++
		return nil, errors.New("smartctl: not found")
	})
	testee.SetClockForTest(func() time.Time { return now })

	// Drive the backoff up by always waiting the longest it could ask for.
	// The clock is NOT advanced after the last one, so `now` sits exactly on
	// the most recent attempt and the check below measures from there.
	for i := range 8 {
		if _, err := testee.Collect(context.Background()); err != nil {
			t.Fatalf("Collect: %v", err)
		}
		if i < 7 {
			now = now.Add(time.Hour)
		}
	}
	if calls != 8 {
		t.Fatalf("scan calls = %d, want 8", calls)
	}

	// Now that the backoff has saturated, a minute must no longer be enough.
	before := calls
	now = now.Add(time.Minute)
	if _, err := testee.Collect(context.Background()); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if calls != before {
		t.Errorf("scan calls = %d, want %d -- a permanently unavailable host must not be probed every minute",
			calls, before)
	}
}

const nvmeScanJSON = `{"devices":[{"name":"/dev/nvme0","type":"nvme"}]}`

// A real smartctl NVMe report: no ata_smart_attributes table at all, and a
// temperature_sensors array alongside the scalars.
const nvmeDeviceJSON = `{
  "model_name": "Samsung SSD 990 PRO 2TB",
  "serial_number": "S7HENL0X123456",
  "nvme_smart_health_information_log": {
    "critical_warning": 0,
    "temperature": 41,
    "available_spare": 100,
    "available_spare_threshold": 10,
    "percentage_used": 3,
    "data_units_read": 55432198,
    "power_cycles": 412,
    "power_on_hours": 8921,
    "unsafe_shutdowns": 17,
    "media_errors": 2,
    "num_err_log_entries": 5,
    "temperature_sensors": [41, 52]
  }
}`

func smartRow(t *testing.T, rows []*netrav1.SmartAttribute, id uint32) *netrav1.SmartAttribute {
	t.Helper()
	for _, r := range rows {
		if r.GetAttrId() == id {
			return r
		}
	}
	t.Fatalf("no row for attr_id %d in %d rows", id, len(rows))
	return nil
}

// An NVMe drive publishes a fixed health log instead of the ATA attribute
// table, and the collector used to unmarshal it and then never read it -- so
// an all-NVMe host, which is most modern servers, ran smartctl against every
// drive on the interval and emitted nothing at all.
func TestSmartReportsTheNvmeHealthLog(t *testing.T) {
	// Given: a host whose only drive is NVMe.
	testee := collector.NewSmart(time.Hour, fakeSmartctl(nvmeScanJSON, nvmeDeviceJSON))

	// When: it is collected.
	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	// Then: the health fields arrive as attributes on the drive.
	if len(res.Smart) == 0 {
		t.Fatal("no SMART rows for an NVMe drive")
	}
	for _, r := range res.Smart {
		if got := r.GetDevice(); got != "nvme0" {
			t.Errorf("device = %q, want nvme0", got)
		}
		if got := r.GetModel(); got != "Samsung SSD 990 PRO 2TB" {
			t.Errorf("model = %q, want the drive's model", got)
		}
		// ATA ids are 1-255, so nothing here may collide with a real one --
		// and the hub's column is SMALLINT, so nothing may approach 32767.
		if id := r.GetAttrId(); id < 1000 || id > 32767 {
			t.Errorf("attr_id = %d, want a synthetic id in [1000, 32767]", id)
		}
		// NVMe has no 1-253 vendor scale, and inventing one would be the
		// collector asserting a health verdict it has no basis for.
		if r.Normalized != nil {
			t.Errorf("attr_id %d has normalized = %d, want unset for NVMe",
				r.GetAttrId(), r.GetNormalized())
		}
	}

	if got := smartRow(t, res.Smart, 1001).GetRaw(); got != 3 {
		t.Errorf("percentage_used raw = %d, want 3", got)
	}
	if got := smartRow(t, res.Smart, 1004).GetRaw(); got != 2 {
		t.Errorf("media_errors raw = %d, want 2", got)
	}
	if got := smartRow(t, res.Smart, 1005).GetRaw(); got != 17 {
		t.Errorf("unsafe_shutdowns raw = %d, want 17", got)
	}
	// critical_warning is zero and must still be reported: "the drive says it
	// is fine" is a measurement, and its absence would read as an unread drive.
	if got := smartRow(t, res.Smart, 1000).GetRaw(); got != 0 {
		t.Errorf("critical_warning raw = %d, want 0", got)
	}
}

// A field the drive does not publish, or publishes as something other than a
// number, is an absent fact. Skipping beats defaulting: a zero would read as a
// measured value.
func TestSmartSkipsNvmeFieldsItCannotRead(t *testing.T) {
	// Given: a drive publishing only two of the fields netra reports, one of
	// them as an array rather than a scalar.
	const partial = `{
	  "model_name": "Sparse NVMe",
	  "nvme_smart_health_information_log": {
	    "percentage_used": 7,
	    "temperature": [40, 45]
	  }
	}`
	testee := collector.NewSmart(time.Hour, fakeSmartctl(nvmeScanJSON, partial))

	// When: it is collected.
	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	// Then: only the readable scalar is reported.
	if len(res.Smart) != 1 {
		t.Fatalf("rows = %d, want 1 -- absent and unreadable fields must be skipped", len(res.Smart))
	}
	if got := res.Smart[0].GetAttrId(); got != 1001 {
		t.Errorf("attr_id = %d, want 1001 (percentage_used)", got)
	}
	if got := res.Smart[0].GetRaw(); got != 7 {
		t.Errorf("raw = %d, want 7", got)
	}
}

// A host with both drive kinds must report both, under ids that cannot
// collide: ATA attribute ids run 1-255 and the synthetic NVMe ones start at
// 1000, so one drive's temperature can never overwrite the other's.
func TestSmartReportsAtaAndNvmeDrivesTogether(t *testing.T) {
	// Given: a host with one SATA and one NVMe drive.
	run := func(_ context.Context, args ...string) ([]byte, error) {
		for _, a := range args {
			switch a {
			case "--scan":
				return []byte(`{"devices":[
					{"name":"/dev/sda","type":"sat"},
					{"name":"/dev/nvme0","type":"nvme"}]}`), nil
			case "/dev/sda":
				return []byte(deviceJSON), nil
			case "/dev/nvme0":
				return []byte(nvmeDeviceJSON), nil
			}
		}
		return nil, nil
	}
	testee := collector.NewSmart(time.Hour, run)

	// When: it is collected.
	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	// Then: both drives are present, each with its own id space.
	byDevice := map[string]int{}
	for _, r := range res.Smart {
		byDevice[r.GetDevice()]++
		if r.GetDevice() == "sda" && r.GetAttrId() >= 1000 {
			t.Errorf("sda carries synthetic attr_id %d, want only ATA ids", r.GetAttrId())
		}
		if r.GetDevice() == "nvme0" && r.GetAttrId() < 1000 {
			t.Errorf("nvme0 carries ATA attr_id %d, want only synthetic ids", r.GetAttrId())
		}
	}
	if byDevice["sda"] != 3 {
		t.Errorf("sda rows = %d, want 3", byDevice["sda"])
	}
	if byDevice["nvme0"] == 0 {
		t.Error("nvme0 rows = 0, want its health log reported alongside the SATA drive")
	}
}
