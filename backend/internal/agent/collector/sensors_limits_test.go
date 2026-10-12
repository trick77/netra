package collector_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/trick77/netra/internal/agent/collector"
)

// hwmonChip writes one fake chip directory and returns the sysfs root to point
// the collector at.
//
// Built here rather than added to testdata/hwmon, because the limit attributes
// are exactly the thing these tests vary -- present, absent, unreadable,
// sentinel -- and a shared fixture can only hold one of those.
func hwmonChip(t *testing.T, name string, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "class", "hwmon", "hwmon0")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "name"), []byte(name+"\n"), 0o644); err != nil {
		t.Fatalf("write name: %v", err)
	}
	for file, body := range files {
		if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", file, err)
		}
	}
	return root
}

func collectOne(t *testing.T, root string) (temp float64, high, crit *float64) {
	t.Helper()
	res, err := collector.NewSensors(root, time.Second).Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(res.Sensors) != 1 {
		t.Fatalf("sensors = %d, want 1", len(res.Sensors))
	}
	row := res.Sensors[0]
	return row.GetTemp(), row.LimitHigh, row.LimitHighCrit
}

// The limits are millidegrees in sysfs and degrees on the wire, exactly as the
// reading beside them is.
func TestSensorsReadsPublishedLimits(t *testing.T) {
	root := hwmonChip(t, "coretemp", map[string]string{
		"temp1_label": "Package id 0\n",
		"temp1_input": "45000\n",
		"temp1_max":   "84000\n",
		"temp1_crit":  "100000\n",
	})

	temp, high, crit := collectOne(t, root)

	if temp != 45 {
		t.Errorf("temp = %v, want 45", temp)
	}
	if high == nil || *high != 84 {
		t.Errorf("limit_high = %v, want 84", high)
	}
	if crit == nil || *crit != 100 {
		t.Errorf("limit_high_crit = %v, want 100", crit)
	}
}

// A chip that publishes nothing -- k10temp, acpitz -- leaves both unset rather
// than sending a zero, so the hub can tell "no limit" from "a limit of zero".
func TestSensorsLeavesLimitsUnsetWhenTheChipPublishesNone(t *testing.T) {
	root := hwmonChip(t, "k10temp", map[string]string{
		"temp1_label": "Tctl\n",
		"temp1_input": "52000\n",
	})

	temp, high, crit := collectOne(t, root)

	if temp != 52 {
		t.Errorf("temp = %v, want 52", temp)
	}
	if high != nil || crit != nil {
		t.Errorf("limits = %v/%v, want both unset", high, crit)
	}
}

// A chip that publishes only one of the pair is taken at its word for that one.
func TestSensorsAcceptsOneLimitWithoutTheOther(t *testing.T) {
	// coretemp rather than drivetemp: a storage chip with no device/ link
	// cannot have its block device resolved and the collector drops the row
	// entirely, which is its own rule and not what this test is about.
	root := hwmonChip(t, "coretemp", map[string]string{
		"temp1_label": "Package id 0\n",
		"temp1_input": "41000\n",
		"temp1_crit":  "70000\n",
	})

	_, high, crit := collectOne(t, root)

	if high != nil {
		t.Errorf("limit_high = %v, want unset", high)
	}
	if crit == nil || *crit != 70 {
		t.Errorf("limit_high_crit = %v, want 70", crit)
	}
}

// A driver disables a limit by publishing a sentinel rather than by omitting
// the file. A 0 C critical would make every reading critical forever, which is
// the loudest possible way to be wrong about a drive that is fine.
func TestSensorsIgnoresAZeroLimit(t *testing.T) {
	root := hwmonChip(t, "coretemp", map[string]string{
		"temp1_label": "Package id 0\n",
		"temp1_input": "45000\n",
		"temp1_max":   "0\n",
		"temp1_crit":  "100000\n",
	})

	_, high, crit := collectOne(t, root)

	if high != nil {
		t.Errorf("limit_high = %v, want unset for a zero sentinel", high)
	}
	if crit == nil || *crit != 100 {
		t.Errorf("limit_high_crit = %v, want 100", crit)
	}
}

// THE RULE THIS PINS, and it is the opposite of the label's. A label that
// cannot be read drops the whole row, because a row under the wrong identity is
// worse than no row. A limit that cannot be read costs only the limit -- a slow
// sysfs read must never delete the hardware reading it came with.
func TestSensorsKeepsTheReadingWhenALimitCannotBeRead(t *testing.T) {
	root := hwmonChip(t, "coretemp", map[string]string{
		"temp1_label": "Package id 0\n",
		"temp1_input": "45000\n",
		"temp1_crit":  "not a number\n",
	})

	temp, _, crit := collectOne(t, root)

	if temp != 45 {
		t.Errorf("temp = %v, want the reading kept at 45", temp)
	}
	if crit != nil {
		t.Errorf("limit_high_crit = %v, want unset", crit)
	}
}

// Only temperatures. inN_max brackets a rail's nominal range and fanN_min is a
// fan's interesting limit, so neither means "how high may this go" and neither
// may be mapped onto the same field.
func TestSensorsReadsNoLimitsForNonTemperatureKinds(t *testing.T) {
	root := hwmonChip(t, "nct6775", map[string]string{
		"fan1_label": "CPU fan\n",
		"fan1_input": "1200\n",
		"fan1_min":   "300\n",
		"fan1_max":   "2000\n",
	})

	res, err := collector.NewSensors(root, time.Second).Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(res.Sensors) != 1 {
		t.Fatalf("sensors = %d, want 1", len(res.Sensors))
	}
	row := res.Sensors[0]
	if row.GetValue() != 1200 {
		t.Errorf("fan value = %v, want 1200 RPM", row.GetValue())
	}
	if row.LimitHigh != nil || row.LimitHighCrit != nil {
		t.Errorf("fan limits = %v/%v, want both unset", row.LimitHigh, row.LimitHighCrit)
	}
}
