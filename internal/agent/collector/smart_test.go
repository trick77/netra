package collector_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/trick77/netra/internal/agent/collector"
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
