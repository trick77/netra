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

// ScrapeInterval is the agent's fixed cadence, which the staleness rule is
// expressed in multiples of.
const ScrapeInterval = 60 * time.Second

// StaleAfter is how long a host may be quiet before it has stopped reporting.
//
// Three scrapes, ported from STALE_THRESHOLD_MS in ui/src/lib/host.ts. One
// missed scrape is a lost packet; three is a machine that is not talking.
const StaleAfter = 3 * ScrapeInterval

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

// The sporadic rule: a host that answers now but keeps dropping scrapes.
//
// Ported from reportsSporadically in ui/src/lib/host.ts. "Online" is exactly as
// wrong a summary of such a host as "offline" -- both say the thing is fine or
// gone, when the interesting state is neither.
const (
	// SporadicMissRatio is the share of a window's buckets a host may miss
	// before it is reporting badly rather than merely reporting.
	SporadicMissRatio = 0.2
	// SporadicMinSpan is the shortest run of buckets this will judge at all.
	// Two buckets cannot tell a gap from a host that started reporting
	// mid-window.
	SporadicMinSpan = 5
	// SporadicMinMisses is the smallest number of misses that can be a pattern
	// rather than an event. At the shortest span judged, ONE miss is exactly
	// the ratio -- so a host added twenty-five minutes ago that dropped a
	// single scrape while its agent settled would be called sporadic on the
	// strength of that one bucket.
	SporadicMinMisses = 2
)

// SporadicWindow is how far back the miss ratio is counted.
//
// FIXED, where the browser used whatever range the reader had picked -- and
// that difference is the whole reason this moved. A judgement made over the
// range picker's window is a fact about the READER rather than about the host:
// change the range and the condition appears or disappears. It is the same
// shape error that took `oom` and `dropped` out of conditions altogether.
//
// Three hours, matching the sentence the row prints -- "gaps in the last few
// hours" -- rather than a number chosen for how it queries.
const SporadicWindow = 3 * time.Hour

// SporadicSeverity rates a host's bucket coverage, or "" for one reporting
// cleanly.
//
// span is how many buckets lie between the first and the last one that carried
// a sample, and present is how many of those did. Both edges are trimmed by the
// caller before they get here, for the same reason: an empty bucket at either
// end is not a scrape the host failed to send. Trailing emptiness is every tier
// materialising behind now, so the newest buckets are empty for every host on
// the page, healthy or not. Leading emptiness is the time before the host was
// reporting at all -- counting it called every newly added agent sporadic on
// its first day, and the badge only cleared once four fifths of the window had
// elapsed since the host was added.
func SporadicSeverity(present, span int) string {
	if span < SporadicMinSpan {
		return ""
	}
	missed := span - present
	if missed < SporadicMinMisses {
		return ""
	}
	if float64(missed)/float64(span) < SporadicMissRatio {
		return ""
	}
	return SeverityWarning
}
