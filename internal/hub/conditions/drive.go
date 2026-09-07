package conditions

import (
	"fmt"
	"sort"
	"time"
)

// What a SMART attribute id MEANS, and whether its reading is a problem.
//
// Ported from ui/src/features/host/smart.ts, which is where this rule was
// written and where it still renders the Storage tab's per-attribute findings.
// What moved here is the part that decides whether a drive is a CONDITION on
// its host -- driveAlarms -- because that judgement is one an alerting engine
// has to be able to make, and an alerting engine cannot call into a browser.
//
// TWO ID SPACES share the attribute table. ATA attributes are 1-255, numbered
// by the drive's own table. NVMe has no attribute ids at all -- its health log
// is a fixed struct of named fields -- so the collector maps those fields onto
// SYNTHETIC ids from 1000 up (nvmeAttrs in internal/agent/collector/smart.go).
// The two cannot collide, which is why the range starts where it does.

// ATA attribute ids this file knows how to read.
const (
	ATAReallocatedSectors   = 5
	ATAReportedUncorrect    = 187
	ATACurrentPending       = 197
	ATAOfflineUncorrectable = 198
	ATACRCErrors            = 199
)

// The synthetic ids the collector assigns to NVMe health-log fields.
const (
	NVMeCriticalWarning         = 1000
	NVMePercentageUsed          = 1001
	NVMeAvailableSpare          = 1002
	NVMeAvailableSpareThreshold = 1003
	NVMeMediaErrors             = 1004
)

// Wear thresholds, in one place, because both are a judgement.
//
// A drive's own estimate of consumed write endurance is allowed to pass 100: a
// drive at 105% is out of rated life and still writing, which is worth saying
// rather than clamping away.
const (
	// WearWarningPct is wear worth planning a replacement around.
	WearWarningPct = 80
	// WearCriticalPct is rated endurance consumed. Past here the drive is on
	// borrowed time.
	WearCriticalPct = 100
)

// DriveStaleAfter is how far a drive's last reading may trail its host's own
// last_seen before netra stops counting it as one of the host's drives.
//
// A week, where a mount gets three minutes, and the difference is not an
// inconsistency: the filesystem collector runs on every scrape tick, while
// AGENT_SMART_INTERVAL is the operator's to set and defaults to hourly. Ported
// from DRIVE_STALE_MS in smart.ts, and it exists because of a real haunting --
// a pulled disk kept its row and kept alarming, fixed once in da8e88e.
const DriveStaleAfter = 7 * 24 * time.Hour

// Urgency is the tie-break between two findings of the SAME severity, worst
// lowest.
//
// Not a fourth severity by another name: nothing renders it and a reader never
// sees these words. It exists because everything that escalates to the host is
// critical, so a severity comparison cannot decide which drive the host's one
// attention line names -- and "sda, 12 reallocated sectors (+1 more)" hides an
// unreadable sector behind a counter that is merely climbing.
type Urgency int

const (
	// UrgencyAcute is unreadable now, out of spares, out of rated life, or the
	// drive's own verdict on itself.
	UrgencyAcute Urgency = 0
	// UrgencyAccrued is a counter recording damage the drive has already
	// absorbed.
	UrgencyAccrued Urgency = 1
	// UrgencyWearing is below both, and only ever on a warning: wear short of
	// the limit, and a fault on the wire rather than on the platter.
	UrgencyWearing Urgency = 2
)

// DriveAttr is one SMART attribute's latest raw value.
//
// Raw is a POINTER because absent is not zero anywhere in this file. A drive
// that does not report reallocated sectors has not reported zero of them.
type DriveAttr struct {
	ID  int16
	Raw *int64
}

// DriveReading is one drive as the hub last saw it.
type DriveReading struct {
	Device string
	// LastSeen is when this drive's newest reading was TAKEN, from
	// devices.last_seen -- stamped from the reading's own ts, never from the
	// hub's clock.
	LastSeen   time.Time
	Attributes []DriveAttr
}

// DriveFinding is one thing wrong with a drive, in the words an operator would
// use.
type DriveFinding struct {
	Severity string
	Urgency  Urgency
	Text     string
}

func (d DriveReading) attr(id int16) *int64 {
	for _, a := range d.Attributes {
		if a.ID == id {
			return a.Raw
		}
	}
	return nil
}

// isNVMe reports which id space this drive's readings came from.
//
// Decided by whether any NVMe id is present rather than by the device name:
// /dev/nvme0n1 is the common case but not the only one, and a USB enclosure can
// put an NVMe drive behind an ATA passthrough. The ids are what the collector
// actually wrote, so they are what this reads.
func (d DriveReading) isNVMe() bool {
	for _, a := range d.Attributes {
		if a.ID >= NVMeCriticalWarning {
			return true
		}
	}
	return false
}

func plural(n int64, one string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %ss", n, one)
}

// DriveFindings is everything wrong with one drive, worst first.
//
// Reads only the attributes it knows. An unrecognised id is not a finding:
// every drive reports attributes this has never heard of, and guessing that a
// non-zero unknown counter is bad would flag every healthy disk in the fleet.
func DriveFindings(d DriveReading) []DriveFinding {
	var out []DriveFinding
	add := func(severity string, urgency Urgency, text string) {
		out = append(out, DriveFinding{Severity: severity, Urgency: urgency, Text: text})
	}

	if len(d.Attributes) == 0 {
		return nil
	}

	if d.isNVMe() {
		// A bitfield the drive sets when it is telling the host it is in
		// trouble. Any bit is a verdict rather than a reading, so it outranks
		// everything else here.
		if w := d.attr(NVMeCriticalWarning); w != nil && *w != 0 {
			add(SeverityCritical, UrgencyAcute, "drive reports a critical warning")
		}

		// Spare against the drive's OWN threshold. The percentage means nothing
		// without the line it is compared against, and that line varies per
		// model -- which is why the collector reports both.
		spare, floor := d.attr(NVMeAvailableSpare), d.attr(NVMeAvailableSpareThreshold)
		if spare != nil && floor != nil && *spare <= *floor {
			add(SeverityCritical, UrgencyAcute, fmt.Sprintf(
				"spare blocks at %d%%, at or below the drive's %d%% floor", *spare, *floor))
		}

		if used := d.attr(NVMePercentageUsed); used != nil {
			switch {
			case *used >= WearCriticalPct:
				add(SeverityCritical, UrgencyAcute, fmt.Sprintf("%d%% of rated write endurance used", *used))
			case *used >= WearWarningPct:
				add(SeverityWarning, UrgencyWearing, fmt.Sprintf("%d%% of rated write endurance used", *used))
			}
		}

		// Uncorrected data-integrity errors: the drive returned bad data or
		// could not return it at all.
		if media := d.attr(NVMeMediaErrors); media != nil && *media > 0 {
			add(SeverityCritical, UrgencyAccrued, plural(*media, "media error"))
		}
		return sortFindings(out)
	}

	// Sectors the drive has tried to read and could not, not yet reallocated.
	// The most urgent ATA counter there is: the data in them is currently
	// unreadable, and the count moves on the next write or the next failure.
	if p := d.attr(ATACurrentPending); p != nil && *p > 0 {
		add(SeverityCritical, UrgencyAcute, plural(*p, "pending sector"))
	}

	// Sectors that failed even offline verification -- unreadable and not
	// recoverable by rewriting.
	if o := d.attr(ATAOfflineUncorrectable); o != nil && *o > 0 {
		add(SeverityCritical, UrgencyAcute, plural(*o, "uncorrectable sector"))
	}

	// Already swapped for spares: the drive has started substituting for
	// damage, and it does not come back from that on its own -- which is what
	// DriveAlarms escalates on, so it is critical. It still ranks below a
	// sector that is unreadable RIGHT NOW; see Urgency.
	if r := d.attr(ATAReallocatedSectors); r != nil && *r > 0 {
		add(SeverityCritical, UrgencyAccrued, plural(*r, "reallocated sector"))
	}

	if u := d.attr(ATAReportedUncorrect); u != nil && *u > 0 {
		add(SeverityCritical, UrgencyAccrued, plural(*u, "uncorrectable error"))
	}

	// The cable, not the drive. UDMA CRC errors are corruption on the wire
	// between host and disk, so the fix is a cable or a port rather than a
	// replacement -- worth saying, because the alternative is somebody
	// replacing a healthy drive.
	if c := d.attr(ATACRCErrors); c != nil && *c > 0 {
		add(SeverityWarning, UrgencyWearing, plural(*c, "CRC error")+" — check the cable")
	}
	return sortFindings(out)
}

func severityRank(s string) int {
	if s == SeverityCritical {
		return 0
	}
	return 1
}

func sortFindings(in []DriveFinding) []DriveFinding {
	sort.SliceStable(in, func(i, j int) bool {
		if a, b := severityRank(in[i].Severity), severityRank(in[j].Severity); a != b {
			return a < b
		}
		return in[i].Urgency < in[j].Urgency
	})
	return in
}

// DriveAlarm is one finding worth raising a condition on its host for.
type DriveAlarm struct {
	Device   string
	Severity string
	Urgency  Urgency
	Text     string
}

// DriveIsCurrent reports whether a drive's reading still describes a drive the
// host has.
//
// Against the HOST'S OWN last_seen rather than the wall clock, so an agent with
// a skewed clock does not lose its whole inventory to a fact about its NTP
// config. A drive or a host with no timestamp is kept: with no reference point
// the honest answer is the reading netra holds, and failing open here means
// never silently dropping a possibly-dying disk.
func DriveIsCurrent(drive DriveReading, hostLastSeen *time.Time) bool {
	if hostLastSeen == nil || drive.LastSeen.IsZero() {
		return true
	}
	return hostLastSeen.Sub(drive.LastSeen) <= DriveStaleAfter
}

// DriveAlarms is what a drive is worth raising on its host, worst first.
//
// CRITICAL findings only. CRC errors and wear short of the limit are visible on
// the drive's own row and never escalate: they are counters that never reset,
// so a host would be parked in the attention list for ever with nothing anybody
// could do to clear it.
func DriveAlarms(drive DriveReading) []DriveAlarm {
	var out []DriveAlarm
	for _, f := range DriveFindings(drive) {
		if f.Severity != SeverityCritical {
			continue
		}
		out = append(out, DriveAlarm{
			Device:   drive.Device,
			Severity: f.Severity,
			Urgency:  f.Urgency,
			Text:     f.Text,
		})
	}
	return out
}

// SortAlarms orders a host's alarms across drives, worst first.
//
// Stable, so two alarms that tie on both keys keep the order their drives were
// read in -- which is device order, and is the only thing left to break the tie
// with.
func SortAlarms(in []DriveAlarm) []DriveAlarm {
	sort.SliceStable(in, func(i, j int) bool {
		if a, b := severityRank(in[i].Severity), severityRank(in[j].Severity); a != b {
			return a < b
		}
		return in[i].Urgency < in[j].Urgency
	})
	return in
}
