package conditions

// The catalogue: every kind's name and the severity it enters at, as data the
// hub serves rather than as a table the browser keeps.
//
// It existed in TypeScript first, as CONDITION_KIND_INFO in
// ui/src/features/fleet/conditions.ts, and it exists for a real reason: a
// filter for a kind NOBODY IS CURRENTLY CARRYING must still be able to name
// itself. A label derived from the conditions actually present disappears the
// moment the last host carrying that kind recovers, and the page is then
// holding a filter it cannot name -- "Showing 0 of 100 hosts with", with a
// segment pressed for something no longer on screen. A reader who followed a
// link to a kind that has since cleared deserves to be told which kind cleared.
//
// Left in TypeScript it is one more copy of a hub-owned rule, which is exactly
// the class of duplication systemdstate/notable.go:9-13 warns about: "no
// compiler or linter in this repo can see across that boundary -- so a change
// here is only half a change." So the conditions API returns this alongside the
// rows, and ConditionKind stops being a hand-maintained union.

// Thresholds are the numbers a kind's judgement is made of, for the callers
// that must apply the same rule to subjects no condition covers.
//
// Only `disk` has any, and only because its thresholds do double duty. The
// fleet's Disk meter ranks a host's mounts by severity before percentage, and
// the host page's disk tile colours itself the same way -- both of which have
// to judge HEALTHY mounts, which the conditions API never mentions. Without
// this the browser would need its own copy of the four numbers to draw a green
// meter, and the copy this whole engine exists to delete would grow back.
type Thresholds struct {
	WarnPct  float64 `json:"warn_pct"`
	CritPct  float64 `json:"crit_pct"`
	WarnFree int64   `json:"warn_free"`
	CritFree int64   `json:"crit_free"`
}

// KindInfo is one kind, named and rated.
type KindInfo struct {
	Kind string `json:"kind"`
	// Label is the kind's own name, identical for every host carrying it.
	// Sentence case, because it heads a count rather than labelling a column.
	Label string `json:"label"`
	// Severity is the kind's ENTRY severity, used only when nothing is
	// carrying the kind any more. A kind that is present takes its severity
	// from the rows themselves, because one disk warns where another criticals
	// and a counts line must not understate that.
	Severity string `json:"severity"`
	// Thresholds is nil for every kind whose rule needs no numbers on the
	// other side of the wire.
	Thresholds *Thresholds `json:"thresholds,omitempty"`
}

// Catalogue is every kind, in the order a filter offers them.
//
// Every kind, present or not -- see the note at the top of this file. The order
// is the one the browser has always used and is deliberately not severity
// order: it groups the two reporting kinds, then the three about what is on the
// machine.
func Catalogue() []KindInfo {
	return []KindInfo{
		{Kind: KindSilent, Label: "Stopped reporting", Severity: SeverityCritical},
		{Kind: KindSporadic, Label: "Reporting sporadically", Severity: SeverityWarning},
		{Kind: KindFailedUnits, Label: "Failed units", Severity: SeverityWarning},
		{
			Kind:     KindDisk,
			Label:    "Filesystem nearly full",
			Severity: SeverityWarning,
			Thresholds: &Thresholds{
				WarnPct:  DiskWarnPct,
				CritPct:  DiskCritPct,
				WarnFree: DiskWarnFree,
				CritFree: DiskCritFree,
			},
		},
		// A measurement, not a diagnosis -- the rule every kind here follows.
		// "Drive failing" would be a verdict on hardware, and it would overstate
		// a host whose only alarm is a handful of reallocated sectors: that
		// drive has substituted for damage, which is worth acting on and is not
		// the same as failing.
		{Kind: KindDrive, Label: "Drive errors", Severity: SeverityCritical},
	}
}
