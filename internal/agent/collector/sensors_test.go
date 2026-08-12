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
	testee := collector.NewSensors("testdata/hwmon/sys", time.Second)

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
	before := collector.NewSensors("testdata/hwmon/sys", time.Second)
	res, err := before.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect before: %v", err)
	}
	if got := sensorRow(t, res.Sensors, "nvme", "Composite").GetTemp(); got != 38.5 {
		t.Fatalf("nvme/Composite before = %v, want 38.5", got)
	}

	// Same hardware, renumbered directories -- nvme is now hwmon0.
	after := collector.NewSensors("testdata/hwmon-renumbered/sys", time.Second)
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

// BEHAVIOUR CHANGE: an unlabelled input is now reported under its attribute
// name rather than skipped.
//
// The old rule -- a label or nothing -- was defensible while this collector
// read temperatures only, since tempN_label is common. It does not survive
// contact with fans and voltage rails: hwmon publishes fanN_label and
// inN_label far less often, so requiring a label would drop most of exactly
// the sensors added here, and a stopped fan would stay invisible.
//
// fan1 is not a made-up name. It is the kernel's own attribute, stable for
// the life of the chip, and it is scoped by the chip name -- so the identity
// is still (chip, attribute) rather than a bare guess. A label, where there
// is one, is still preferred and still wins.
func TestSensorsReportsUnlabelledInputsUnderTheirAttributeName(t *testing.T) {
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

	testee := collector.NewSensors(root, time.Second)
	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(res.Sensors) != 2 {
		t.Fatalf("sensors = %d, want 2 -- an unlabelled input is reported under its attribute name",
			len(res.Sensors))
	}

	byLabel := map[string]float64{}
	for _, r := range res.Sensors {
		byLabel[r.GetLabel()] = r.GetValue()
	}
	// The label wins where there is one.
	if got, ok := byLabel["Ambient"]; !ok || got != 25 {
		t.Errorf("Ambient = %v (present=%v), want 25", got, ok)
	}
	// And the attribute name identifies the one without.
	if got, ok := byLabel["temp1"]; !ok || got != 50 {
		t.Errorf("temp1 = %v (present=%v), want 50", got, ok)
	}
}

// A fan at 1200 RPM, a rail at 12 V and a package at 45 C are three different
// quantities, and hwmon scales them three different ways: millidegrees and
// millivolts, but RPM raw and power in MICROwatts. One divisor for all of
// them would report a 15 W package as 15000 W or a 1200 RPM fan as 1.2.
func TestSensorsReportsFansVoltagesCurrentsAndPowerInTheirOwnUnits(t *testing.T) {
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
	write("name", "nct6775\n")
	write("temp1_label", "CPU\n")
	write("temp1_input", "45000\n") // millidegrees -> 45 C
	write("fan1_label", "CPU Fan\n")
	write("fan1_input", "1200\n") // already RPM
	write("in0_label", "+12V\n")
	write("in0_input", "12100\n")       // millivolts -> 12.1 V
	write("curr1_input", "2500\n")      // milliamps -> 2.5 A
	write("power1_input", "15000000\n") // microwatts -> 15 W
	// Thresholds and alarms are not readings and must not become series.
	write("temp1_crit", "100000\n")
	write("temp1_max", "90000\n")
	write("fan1_alarm", "0\n")

	testee := collector.NewSensors(root, time.Second)
	res, err := testee.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	type reading struct {
		kind  string
		value float64
	}
	got := map[string]reading{}
	for _, r := range res.Sensors {
		got[r.GetLabel()] = reading{r.GetKind(), r.GetValue()}
	}

	want := map[string]reading{
		"CPU":     {"temperature", 45},
		"CPU Fan": {"fan", 1200},
		"+12V":    {"voltage", 12.1},
		"curr1":   {"current", 2.5},
		"power1":  {"power", 15},
	}
	if len(got) != len(want) {
		t.Fatalf("sensors = %d %v, want %d -- thresholds and alarms must not become series",
			len(got), got, len(want))
	}
	for label, w := range want {
		g, ok := got[label]
		if !ok {
			t.Errorf("no sensor labelled %q", label)
			continue
		}
		if g.kind != w.kind {
			t.Errorf("%s kind = %q, want %q", label, g.kind, w.kind)
		}
		if g.value != w.value {
			t.Errorf("%s value = %v, want %v", label, g.value, w.value)
		}
	}

	// temp stays populated for temperatures, and only for those: it is the
	// column every existing panel reads.
	for _, r := range res.Sensors {
		if r.GetKind() == "temperature" && r.Temp == nil {
			t.Errorf("%s is a temperature with temp unset", r.GetLabel())
		}
		if r.GetKind() != "temperature" && r.Temp != nil {
			t.Errorf("%s is a %s but set temp = %v", r.GetLabel(), r.GetKind(), r.GetTemp())
		}
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

	testee := collector.NewSensors(root, time.Second)
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
	testee := collector.NewSensors(t.TempDir(), time.Second)

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
	absent := collector.NewSensors(t.TempDir(), time.Second)
	if _, err := absent.Collect(context.Background()); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	caps := absent.Capabilities()
	if caps["sensors"] != "absent" {
		t.Errorf("capabilities = %v, want sensors=absent", caps)
	}

	present := collector.NewSensors("testdata/hwmon/sys", time.Second)
	if _, err := present.Collect(context.Background()); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if caps := present.Capabilities(); caps["sensors"] != "" {
		t.Errorf("capabilities = %v, want nothing reported when sensors work", caps)
	}
}
