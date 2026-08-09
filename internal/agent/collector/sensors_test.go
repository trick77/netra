package collector_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/trick77/netra/internal/agent/collector"
	netrav1 "github.com/trick77/netra/internal/gen/netra/v1"
)

func sensorRow(t *testing.T, rows []*netrav1.SensorSample, chip, label string) *netrav1.SensorSample {
	t.Helper()
	for _, r := range rows {
		if r.GetChip() == chip && r.GetLabel() == label {
			return r
		}
	}
	t.Fatalf("no row for %s/%s in %d rows", chip, label, len(rows))
	return nil
}

// Temperatures are millidegrees in sysfs and degrees in the schema.
func TestSensorsReadsEveryLabelledInput(t *testing.T) {
	testee := collector.NewSensors("testdata/hwmon/sys", time.Minute, time.Second)

	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(res.Sensors) != 3 {
		t.Fatalf("sensors = %d, want 3", len(res.Sensors))
	}

	if got := sensorRow(t, res.Sensors, "coretemp", "Package id 0").GetTemp(); got != 45 {
		t.Errorf("coretemp/Package id 0 = %v, want 45 (45000 millidegrees)", got)
	}
	if got := sensorRow(t, res.Sensors, "coretemp", "Core 0").GetTemp(); got != 42 {
		t.Errorf("coretemp/Core 0 = %v, want 42", got)
	}
	if got := sensorRow(t, res.Sensors, "nvme", "Composite").GetTemp(); got != 38.5 {
		t.Errorf("nvme/Composite = %v, want 38.5", got)
	}
	if res.Sensors[0].GetTsMs() == 0 {
		t.Error("row carries no ts_ms")
	}
}

// The identity is chip + label, NEVER hwmonN.
//
// The N is allocation-order dependent: a kernel or hardware change reorders
// the directories across a reboot, and a collector keyed on hwmonN would then
// silently attribute one chip's history to another -- the series stays
// continuous and the numbers are plausible, so nothing looks wrong.
//
// The two fixture trees hold the same two chips with their hwmon numbers
// swapped. Every identity must survive.
func TestSensorsIdentityIsChipAndLabelNotHwmonNumber(t *testing.T) {
	before := collector.NewSensors("testdata/hwmon/sys", time.Minute, time.Second)
	res, err := before.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect before: %v", err)
	}
	if got := sensorRow(t, res.Sensors, "nvme", "Composite").GetTemp(); got != 38.5 {
		t.Fatalf("nvme/Composite before = %v, want 38.5", got)
	}

	// Same hardware, renumbered directories -- nvme is now hwmon0.
	after := collector.NewSensors("testdata/hwmon-renumbered/sys", time.Minute, time.Second)
	res, err = after.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect after: %v", err)
	}

	// Still found by chip and label, and reading the value the renumbered tree
	// holds rather than the one that happens to sit at the same hwmonN.
	if got := sensorRow(t, res.Sensors, "nvme", "Composite").GetTemp(); got != 39.5 {
		t.Errorf("nvme/Composite after renumbering = %v, want 39.5", got)
	}
	if got := sensorRow(t, res.Sensors, "coretemp", "Package id 0").GetTemp(); got != 46 {
		t.Errorf("coretemp/Package id 0 after renumbering = %v, want 46", got)
	}

	for _, r := range res.Sensors {
		if r.GetChip() == "hwmon0" || r.GetChip() == "hwmon1" {
			t.Errorf("chip reported as %q; the directory name must never be the identity", r.GetChip())
		}
	}
}

// An input with no label cannot be identified stably -- temp3_input on its own
// says nothing about what it measures, and its meaning changes between kernel
// versions. Skipped rather than reported under a made-up name.
func TestSensorsSkipsUnlabelledInputs(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "class", "hwmon", "hwmon0")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	write("name", "acpitz\n")
	write("temp1_input", "50000\n")   // no label
	write("temp2_label", "Ambient\n") // labelled
	write("temp2_input", "25000\n")

	testee := collector.NewSensors(root, time.Minute, time.Second)
	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(res.Sensors) != 1 {
		t.Fatalf("sensors = %d, want 1 (only the labelled input)", len(res.Sensors))
	}
	if res.Sensors[0].GetLabel() != "Ambient" {
		t.Errorf("label = %q, want Ambient", res.Sensors[0].GetLabel())
	}
}

// A chip directory with no name file is not a sensor chip worth reporting: the
// chip half of the identity would be empty, and every such directory would
// collide with every other.
func TestSensorsSkipsChipsWithoutAName(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "class", "hwmon", "hwmon0")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "temp1_input"), []byte("50000\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	testee := collector.NewSensors(root, time.Minute, time.Second)
	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(res.Sensors) != 0 {
		t.Errorf("sensors = %d, want 0 for a chip with no name", len(res.Sensors))
	}
}

// A host with no hwmon directory has no sensors. That is an absent subsystem,
// not a failure -- most VPSes have none, and an error every 60s would be noise
// the operator learns to ignore.
func TestSensorsReportsNothingWhenHwmonIsAbsent(t *testing.T) {
	testee := collector.NewSensors(t.TempDir(), time.Minute, time.Second)

	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v, want no error for an absent subsystem", err)
	}
	if len(res.Sensors) != 0 {
		t.Errorf("sensors = %d, want 0", len(res.Sensors))
	}
}

// A capability is reported so "no sensors" is a stated fact rather than an
// ambiguous absence: the hub cannot otherwise tell a host with no hwmon from
// one whose collector never ran.
func TestSensorsReportsItsAvailabilityAsACapability(t *testing.T) {
	absent := collector.NewSensors(t.TempDir(), time.Minute, time.Second)
	if _, err := absent.Collect(context.Background()); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	caps := absent.Capabilities()
	if caps["sensors"] != "absent" {
		t.Errorf("capabilities = %v, want sensors=absent", caps)
	}

	present := collector.NewSensors("testdata/hwmon/sys", time.Minute, time.Second)
	if _, err := present.Collect(context.Background()); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if caps := present.Capabilities(); caps["sensors"] != "" {
		t.Errorf("capabilities = %v, want nothing reported when sensors work", caps)
	}
}
