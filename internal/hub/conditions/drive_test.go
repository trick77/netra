package conditions_test

import (
	"testing"
	"time"

	"github.com/trick77/netra/internal/hub/conditions"
)

func raw(n int64) *int64 { return &n }

func ata(pairs ...any) conditions.DriveReading {
	d := conditions.DriveReading{Device: "sda"}
	for i := 0; i < len(pairs); i += 2 {
		d.Attributes = append(d.Attributes, conditions.DriveAttr{
			ID:  int16(pairs[i].(int)),
			Raw: raw(int64(pairs[i+1].(int))),
		})
	}
	return d
}

func nvme(pairs ...any) conditions.DriveReading {
	d := ata(pairs...)
	d.Device = "nvme0n1"
	return d
}

func texts(findings []conditions.DriveFinding) []string {
	out := make([]string, 0, len(findings))
	for _, f := range findings {
		out = append(out, f.Text)
	}
	return out
}

// The id space is decided by the ids the collector actually wrote, never by the
// device name: a USB enclosure can put an NVMe drive behind an ATA passthrough.
func TestDriveFindingsPickTheIDSpaceFromTheAttributes(t *testing.T) {
	// An ATA-named device reporting NVMe ids is read as NVMe.
	d := nvme(conditions.NVMeMediaErrors, 3)
	d.Device = "sda"
	if got := texts(conditions.DriveFindings(d)); len(got) != 1 || got[0] != "3 media errors" {
		t.Errorf("findings = %v, want the NVMe rule", got)
	}

	// And an NVMe-named device reporting ATA ids is read as ATA.
	a := ata(conditions.ATACurrentPending, 1)
	a.Device = "nvme0n1"
	if got := texts(conditions.DriveFindings(a)); len(got) != 1 || got[0] != "1 pending sector" {
		t.Errorf("findings = %v, want the ATA rule", got)
	}
}

func TestDriveFindingsReadTheATACounters(t *testing.T) {
	for name, tc := range map[string]struct {
		drive    conditions.DriveReading
		severity string
		text     string
	}{
		"pending sectors are unreadable now": {
			ata(conditions.ATACurrentPending, 2), conditions.SeverityCritical, "2 pending sectors",
		},
		"offline uncorrectable": {
			ata(conditions.ATAOfflineUncorrectable, 1), conditions.SeverityCritical, "1 uncorrectable sector",
		},
		"reallocated sectors are damage already absorbed": {
			ata(conditions.ATAReallocatedSectors, 12), conditions.SeverityCritical, "12 reallocated sectors",
		},
		"reported uncorrect": {
			ata(conditions.ATAReportedUncorrect, 4), conditions.SeverityCritical, "4 uncorrectable errors",
		},
		// The cable, not the drive -- and worth saying, because the alternative
		// is somebody replacing a healthy disk.
		"CRC errors name the cable": {
			ata(conditions.ATACRCErrors, 7), conditions.SeverityWarning, "7 CRC errors — check the cable",
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := conditions.DriveFindings(tc.drive)
			if len(got) != 1 {
				t.Fatalf("findings = %v, want one", texts(got))
			}
			if got[0].Severity != tc.severity || got[0].Text != tc.text {
				t.Errorf("finding = %+v, want %q at %q", got[0], tc.text, tc.severity)
			}
		})
	}
}

func TestDriveFindingsReadTheNVMeHealthLog(t *testing.T) {
	// A bitfield the drive sets when it is telling the host it is in trouble:
	// a verdict rather than a reading.
	if got := texts(conditions.DriveFindings(nvme(conditions.NVMeCriticalWarning, 1))); len(got) != 1 ||
		got[0] != "drive reports a critical warning" {
		t.Errorf("findings = %v, want the drive's own verdict", got)
	}

	// Spare against the drive's OWN threshold, which varies per model -- the
	// percentage means nothing without the line it is compared against.
	got := conditions.DriveFindings(nvme(
		conditions.NVMeAvailableSpare, 5,
		conditions.NVMeAvailableSpareThreshold, 10,
	))
	if len(got) != 1 || got[0].Text != "spare blocks at 5%, at or below the drive's 10% floor" {
		t.Errorf("findings = %v, want the spare rule", texts(got))
	}

	// A spare above its own floor is not a finding, however low it reads.
	if got := conditions.DriveFindings(nvme(
		conditions.NVMeAvailableSpare, 11,
		conditions.NVMeAvailableSpareThreshold, 10,
	)); len(got) != 0 {
		t.Errorf("findings = %v, want none -- it is above the drive's own floor", texts(got))
	}
}

// Wear is graded, and is allowed past 100: a drive at 105% is out of rated life
// and still writing, which is worth saying rather than clamping away.
func TestDriveWearIsGradedAndUncapped(t *testing.T) {
	for name, tc := range map[string]struct {
		used     int
		severity string
	}{
		"under the warning floor": {79, ""},
		"at the warning floor":    {conditions.WearWarningPct, conditions.SeverityWarning},
		"at rated life":           {conditions.WearCriticalPct, conditions.SeverityCritical},
		"past rated life":         {105, conditions.SeverityCritical},
	} {
		t.Run(name, func(t *testing.T) {
			got := conditions.DriveFindings(nvme(conditions.NVMePercentageUsed, tc.used))
			if tc.severity == "" {
				if len(got) != 0 {
					t.Fatalf("findings = %v, want none", texts(got))
				}
				return
			}
			if len(got) != 1 || got[0].Severity != tc.severity {
				t.Fatalf("findings = %+v, want one at %q", got, tc.severity)
			}
		})
	}
}

// Absent is not zero, anywhere. A drive that does not report reallocated
// sectors has not reported zero of them.
func TestDriveFindingsTreatAnAbsentAttributeAsUnknown(t *testing.T) {
	d := conditions.DriveReading{
		Device: "sda",
		Attributes: []conditions.DriveAttr{
			{ID: conditions.ATAReallocatedSectors, Raw: nil},
		},
	}
	if got := conditions.DriveFindings(d); len(got) != 0 {
		t.Errorf("findings = %v, want none -- a null reading is not a clean one", texts(got))
	}

	// And an id the rule has never heard of is not a finding either: every
	// drive reports attributes this file does not know, and guessing that a
	// non-zero unknown counter is bad would flag every healthy disk.
	unknown := ata(42, 9999)
	if got := conditions.DriveFindings(unknown); len(got) != 0 {
		t.Errorf("findings = %v, want none for an unknown id", texts(got))
	}
}

// Everything that escalates is critical, so severity alone cannot order them.
// Urgency is what puts an unreadable sector ahead of a counter that is merely
// climbing.
func TestDriveFindingsOrderAcuteAheadOfAccrued(t *testing.T) {
	d := ata(
		conditions.ATAReallocatedSectors, 12,
		conditions.ATACurrentPending, 1,
		conditions.ATACRCErrors, 3,
	)
	got := texts(conditions.DriveFindings(d))
	want := []string{"1 pending sector", "12 reallocated sectors", "3 CRC errors — check the cable"}
	if len(got) != len(want) {
		t.Fatalf("findings = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("findings = %v, want %v", got, want)
			break
		}
	}
}

// Only critical findings become a condition on the host. CRC errors and wear
// short of the limit are counters that never reset, so escalating them would
// park a host in the attention list for ever with nothing anybody could do.
func TestDriveAlarmsLeaveWarningsBehind(t *testing.T) {
	d := ata(conditions.ATACRCErrors, 7, conditions.ATAReallocatedSectors, 2)
	alarms := conditions.DriveAlarms(d)
	if len(alarms) != 1 || alarms[0].Text != "2 reallocated sectors" {
		t.Fatalf("alarms = %+v, want only the critical one", alarms)
	}
	if alarms[0].Device != "sda" {
		t.Errorf("device = %q, want the drive it came from", alarms[0].Device)
	}

	if got := conditions.DriveAlarms(nvme(conditions.NVMePercentageUsed, 85)); len(got) != 0 {
		t.Errorf("alarms = %+v, want none -- wear short of the limit does not escalate", got)
	}
}

// The 7-day gate, against the HOST'S OWN last_seen rather than the wall clock:
// an agent with a skewed clock must not lose its whole inventory to a fact
// about its NTP config.
func TestDriveIsCurrentIsDatedAgainstTheHost(t *testing.T) {
	host := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	fresh := conditions.DriveReading{Device: "sda", LastSeen: host.Add(-time.Hour)}
	edge := conditions.DriveReading{Device: "sdb", LastSeen: host.Add(-conditions.DriveStaleAfter)}
	gone := conditions.DriveReading{Device: "sdc", LastSeen: host.Add(-conditions.DriveStaleAfter - time.Minute)}

	if !conditions.DriveIsCurrent(fresh, &host) {
		t.Error("a drive read an hour ago is current")
	}
	if !conditions.DriveIsCurrent(edge, &host) {
		t.Error("exactly at the window is still current")
	}
	if conditions.DriveIsCurrent(gone, &host) {
		t.Error("a drive last read past the window is not current")
	}

	// Fail open with no reference point: never silently drop a possibly-dying
	// disk because a timestamp was missing.
	if !conditions.DriveIsCurrent(gone, nil) {
		t.Error("with no host timestamp the honest answer is the reading netra holds")
	}
	if !conditions.DriveIsCurrent(conditions.DriveReading{Device: "sdd"}, &host) {
		t.Error("a drive with no timestamp of its own is kept")
	}

	// A host that is OFF keeps its drives: its last_seen stops moving too, so
	// the gap between the two does not grow.
	old := host.Add(-30 * 24 * time.Hour)
	stale := conditions.DriveReading{Device: "sde", LastSeen: old.Add(-time.Hour)}
	if !conditions.DriveIsCurrent(stale, &old) {
		t.Error("an offline host does not lose its drives")
	}
}

func TestSortAlarmsIsWorstFirstAndStable(t *testing.T) {
	in := []conditions.DriveAlarm{
		{Device: "sdb", Severity: conditions.SeverityCritical, Urgency: conditions.UrgencyAccrued, Text: "b"},
		{Device: "sda", Severity: conditions.SeverityCritical, Urgency: conditions.UrgencyAcute, Text: "a"},
		{Device: "sdc", Severity: conditions.SeverityCritical, Urgency: conditions.UrgencyAccrued, Text: "c"},
	}
	got := conditions.SortAlarms(in)
	if got[0].Device != "sda" {
		t.Errorf("first = %q, want the acute one", got[0].Device)
	}
	// Stable: the two that tie keep the order their drives were read in, which
	// is device order and the only thing left to break the tie with.
	if got[1].Device != "sdb" || got[2].Device != "sdc" {
		t.Errorf("ties = %q, %q, want sdb then sdc", got[1].Device, got[2].Device)
	}
}
