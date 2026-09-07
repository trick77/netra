package conditions

import "time"

// How full a filesystem has to be before it is worth someone's attention.
//
// Ported from DISK_WARN_PCT/DISK_CRIT_PCT in ui/src/features/fleet/conditions.ts,
// which the host page imported rather than restating -- a host that warned on
// its own page and read clean on the fleet page is the disagreement that whole
// module existed to end. Moving them here ends it one level up: the browser
// stops judging at all.
const (
	DiskWarnPct = 90.0
	DiskCritPct = 95.0
)

// How little room has to be LEFT before a percentage means anything.
//
// A percentage on its own is the wrong unit for a disk. Ten per cent of a
// 20 GB root is 2 GB and genuinely urgent; ten per cent of a 6.7 TB array is
// 674 GB and a week of headroom, and netra used to say "/mnt/ark is 90% full
// -- 674.4 GB free" in one breath and expect someone to act on it. What an
// operator runs out of is bytes.
//
// So both halves have to agree: a high proportion full AND little enough left
// that filling it is near.
const (
	DiskWarnFree = int64(100) * 1024 * 1024 * 1024
	DiskCritFree = int64(20) * 1024 * 1024 * 1024
)

// DiskSeverity is what a filesystem's fullness is worth, or "" for one nobody
// needs to look at.
//
// free is a POINTER because "not known" and "none left" are different facts. A
// reading that lost track of the bytes falls back to the percentage alone: a
// row that has forgotten how much room is left must not go silent about a disk
// at 97%.
func DiskSeverity(pct float64, free *int64) string {
	unknown := free == nil
	if pct >= DiskCritPct && (unknown || *free < DiskCritFree) {
		return SeverityCritical
	}
	if pct >= DiskWarnPct && (unknown || *free < DiskWarnFree) {
		return SeverityWarning
	}
	return ""
}

// UsePct is df's Use% for one filesystem, and whether it could be computed.
//
// used / (used + free), never used / total: total includes the root reserve,
// so dividing by it reports a disk as less full than df does -- the number an
// operator has already seen over SSH.
func UsePct(used, free *int64) (float64, bool) {
	if used == nil || free == nil {
		return 0, false
	}
	capacity := *used + *free
	if capacity <= 0 {
		return 0, false
	}
	return float64(*used) / float64(capacity) * 100, true
}

// scrapeInterval is the agent's fixed cadence, which the staleness rule is
// expressed in multiples of.
const scrapeInterval = 60 * time.Second

// StaleAfter is how long a host may be quiet before it has stopped reporting.
//
// Three scrapes, ported from STALE_THRESHOLD_MS in ui/src/lib/host.ts. One
// missed scrape is a lost packet; three is a machine that is not talking.
const StaleAfter = 3 * scrapeInterval

// Reporting says whether a host's data is current enough to conclude anything
// from.
//
// A host that has never been seen is NOT reporting, which is the honest
// reading: netra has no observation of it at all, so the absence of a subject
// on it says nothing either.
func Reporting(lastSeen *time.Time, now time.Time) bool {
	if lastSeen == nil {
		return false
	}
	return now.Sub(*lastSeen) <= StaleAfter
}

// SilentSeverity is what a host's silence is worth, or "" for one that is
// talking.
//
// Critical, matching what both pages already rated it: a host that has not
// spoken qualifies every other figure netra holds about it, so it is not a
// warning that sits alongside the others -- it is the reason to distrust them.
func SilentSeverity(lastSeen *time.Time, now time.Time) string {
	if Reporting(lastSeen, now) {
		return ""
	}
	return SeverityCritical
}
