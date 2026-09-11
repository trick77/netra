package conditions

import (
	"math"
	"time"
)

// Deviation: judging a reading against what that subject normally does.
//
// The five older kinds judge against constants, and they can because 90% full
// means the same thing everywhere. These three cannot. 300 processes is idle on
// one box and a fork bomb on another. A NAS drive that has held 44 C for two
// years is worth a look at 58; a busy NVMe at 58 is doing what its datasheet
// promises. One number per fleet is a number that is wrong for most of the
// fleet.
//
// The model is Observium's, which is worth stating because the ordering in it
// is the part that matters and the part that is easy to get backwards: ASK THE
// HARDWARE FIRST, and calibrate only underneath the answer. A threshold built
// purely from history cannot see a subject that has been too hot all week -- it
// calls that normal, by construction. A threshold built purely from a constant
// cannot tell a spinning disk from an NVMe. Each covers the other's blind spot,
// which is why both are here and neither alone would do.

// How wide a subject's normal range has to be exceeded before it is notable.
//
// Two margins, and the answer is the larger, because either one alone fails on
// a real sensor. The RANGE margin is the honest reading for a subject that
// swings: a CPU between 35 and 75 C says its own tolerance is wide, and half
// that span is a meaningful step beyond it. It collapses to nothing on a sensor
// that never moves -- a drive in a climate-controlled rack holding 38.0 to 38.4
// C for a week gets a margin of 0.2 C and alarms on the first warm afternoon.
// The MAGNITUDE margin exists for exactly that subject: five per cent of a
// reading is a step that scales with the unit, so it is 2 C on a drive and 0.3
// on a load average without either being written down separately.
const (
	MarginRangePct     = 0.50
	MarginMagnitudePct = 0.05
)

// BaselineWindow is how much history a baseline is measured over.
//
// Seven days, matching raw retention exactly -- so no continuous aggregate is
// needed and nothing is measured over buckets that have already been rolled up.
// It is also the widest window available for free, and width is what makes the
// percentile robust: at the 60 s scrape cadence a week is 10,080 samples, so
// p99 is the top hundred or so. A 24-hour window is 1,440 samples, where p99 is
// the fourteenth-highest reading -- about fourteen minutes of the day, which a
// single nightly backup sets on its own. A week also contains the weekly cycle,
// and weekday load is not weekend load on anything that serves people.
const BaselineWindow = 7 * 24 * time.Hour

// BaselineMinSamples is how much history a baseline needs before it may judge.
//
// ~34 h at the 60 s cadence. Below it the subject is UNJUDGED rather than
// healthy -- a new host must not be declared fine by a rule that has not
// watched it yet, and must not be declared broken by percentiles drawn from one
// afternoon.
const BaselineMinSamples = 2000

// OpenAfter is how many consecutive ticks must find a reading over the line
// before the condition opens.
//
// Observium calls this the alert delay, and it is the other half of the
// hysteresis clearAfter provides. clearAfter smooths only the CLOSING side: a
// condition still opens on the first observation, so a nightly cron burst that
// lifts load5 for ninety seconds writes a critical event every night. Three
// ticks is three minutes, which is shorter than any incident worth paging about
// and longer than every scheduled spike that is not one.
//
// Asymmetric against clearAfter = 2 on purpose, and in the opposite direction
// to it. For the five older kinds you want to know QUICKLY and can wait for the
// all-clear, because their predicates do not flap. A deviation predicate flaps
// by nature -- it is a continuous quantity sitting near a line -- so it pays
// the delay on the way in as well.
const OpenAfter = 3

// Delay holds the open-side hysteresis: how many consecutive passes have found
// each not-yet-open subject over its line.
//
// DELIBERATELY NOT INSIDE Diff, and the reason is that it is not a property of
// the diff. A finding that has been over the line once is not a condition the
// state machine should know about yet -- it is a reading. So the delay filters
// the scan BEFORE Diff sees it, Diff keeps the single meaning of Scan.Bad it
// has always had, and its whole state machine stays pure and untouched.
//
// In memory rather than in a column, because the count is worth exactly one
// hub process. A restart forgetting that load5 was high for two of the last
// three minutes costs three more minutes before the condition opens; persisting
// it would buy that back in exchange for a write per subject per tick, forever.
type Delay struct {
	seen map[Key]int
}

// NewDelay builds an empty delay tracker.
func NewDelay() *Delay { return &Delay{seen: make(map[Key]int)} }

// Apply removes findings that have not yet been over the line often enough,
// and forgets subjects that have recovered.
//
// ONLY FOR SUBJECTS WITH NO OPEN CONDITION. Withholding a finding whose
// condition is already open would read to Diff as a MISS, and two misses close
// it -- so a drive genuinely over its limit would have its condition opened,
// closed, reopened and closed again forever, writing a transition pair every
// few minutes and losing its onset each time. The delay decides when to start
// looking, never whether to keep looking.
//
// The other five kinds pass through untouched: a host that has stopped
// reporting or a drive with a reallocated sector should be raised on the first
// observation, and those predicates do not flap the way a continuous
// measurement near a threshold does.
func (d *Delay) Apply(scan Scan, open []Open) {
	isOpen := make(map[Key]bool, len(open))
	for _, o := range open {
		isOpen[o.Key] = true
	}

	// Forget any subject that is no longer bad, so the count means CONSECUTIVE
	// passes. Without this a host that crosses the line for one tick a day
	// would accumulate a crossing every day and open on the third, three days
	// later, describing nothing that happened at the time.
	for key := range d.seen {
		if _, bad := scan.Bad[key]; !bad {
			delete(d.seen, key)
		}
	}

	for key := range scan.Bad {
		if !DeviationKinds[key.Kind] || isOpen[key] {
			// Not delayed, and not counted either: a kind that opens
			// immediately must not leave entries behind in this map.
			delete(d.seen, key)
			continue
		}
		d.seen[key]++
		if d.seen[key] < OpenAfter {
			delete(scan.Bad, key)
		}
	}
}

// Limits are what the hardware says about itself, where it says anything.
//
// Both nil for a chip that publishes nothing, for a host running an agent
// predating the field, and for a read that failed. Those are one fact to a
// reader -- no limit from the hardware -- and all three fall back to Ceilings.
type Limits struct {
	High     *float64
	HighCrit *float64
}

// Ceilings is the fixed threshold used only when the hardware supplies none.
//
// The fallback, never an override: a chip that publishes its own limits is a
// better authority on itself than a table written here could be. The numbers
// differ per chip family because the failure that produced them does: a fixed
// 60 C critical picked for spinning disks makes every NVMe permanently red,
// since composite temperatures of 60-70 C under load are ordinary and vendor
// thresholds sit around 80-85.
//
// CRITICAL ONLY, AND THERE IS NO WARN FIELD. A fallback that also capped the
// warning would reintroduce the fleet-wide-constant failure one severity down.
// drivetemp registers tempN_max only when the drive reports SCT limits, so a
// perfectly healthy 7200 rpm disk that has held 56-58 C all week publishes
// nothing and would be clamped to a 55 C warning it can never get under --
// permanently at warning, for being what it has always been.
//
// The ceiling exists to answer one question: is this subject somewhere no
// subject of its kind should ever be. That is a critical-severity question. The
// EARLY warning is the baseline's job, and the baseline is the half that knows
// what this particular drive runs at.
type Ceilings struct {
	Crit float64
}

// Baseline is what a subject's own history measured.
//
// P01 and P99 rather than min and max: a single reboot, one backup window or
// one bad reading would otherwise set the threshold for the week, which is the
// whole reason percentiles are used at all.
type Baseline struct {
	P01     float64
	P99     float64
	Samples int
}

// Ready reports whether this baseline rests on enough history to judge with.
func (b Baseline) Ready() bool { return b.Samples >= BaselineMinSamples }

// Margin is how far past p99 a reading has to be to count as a departure.
func (b Baseline) Margin(floor float64) float64 {
	byRange := MarginRangePct * (b.P99 - b.P01)
	byMagnitude := MarginMagnitudePct * math.Abs(b.P99)
	return math.Max(math.Max(byRange, byMagnitude), floor)
}

// Bounds is the pair a reading is actually compared against, and where
// each half came from.
type Bounds struct {
	Warn float64
	Crit float64
	// Source is "device" when the hardware's own limit decided the critical
	// threshold, "baseline" when the calibrated one did.
	//
	// Carried into the event detail so the log can say which authority spoke.
	// "61 C, warn 52, crit 58" and "61 C, the drive's own limit is 60" are
	// different sentences, and an operator deciding whether to act reads them
	// differently.
	Source string
}

// Sources, as they appear in a condition's detail JSON.
const (
	SourceDevice   = "device"
	SourceBaseline = "baseline"
)

// DeviationThresholds resolves the two numbers a reading is judged against.
//
// The calibrated pair sits underneath whichever hard limit applies, so the
// EARLY warning survives -- a drive that normally runs at 44 C should be
// noticed at 52, not held silent until its 80 C vendor limit. The hard limit is
// a ceiling on how far calibration may drift, which is what stops a subject
// that has been too hot all week from quietly calibrating its way past the
// point the hardware calls critical.
func DeviationThresholds(b Baseline, floor float64, lim Limits, ceil Ceilings) Bounds {
	margin := b.Margin(floor)
	warn, crit := b.P99+margin/2, b.P99+margin

	// The DEVICE's own pair caps both halves, because the chip publishes one
	// value for each: tempN_max is "warn here" and tempN_crit is "stop here".
	if lim.High != nil && *lim.High > 0 {
		warn = math.Min(warn, *lim.High)
	}

	// SOURCE TRACKS THE CRITICAL THRESHOLD ALONE, and that is not an
	// approximation -- it is what the renderers print. Both of them write "past
	// its <crit> limit" when the source is `device`, so a source set by the
	// WARNING cap would put the word "limit" next to a number the hardware
	// never stated. A chip publishing max 70 and crit 85, on a sensor whose
	// calibrated pair is 71/74, binds only the warning: crit stays 74, which is
	// pure baseline, and the row would otherwise have claimed 74 C was the
	// drive's own limit and that a 70.5 C reading was past it.
	source := SourceBaseline
	switch {
	case lim.HighCrit != nil && *lim.HighCrit > 0:
		// The device spoke, so the family's guess is not consulted at all.
		if *lim.HighCrit < crit {
			crit = *lim.HighCrit
			source = SourceDevice
		}
	case ceil.Crit > 0:
		// A ceiling of zero or less is "this family has none" -- processes and
		// load have no absolute number at which they are wrong, only a history
		// to depart from. The source stays `baseline`: a number written in
		// this file is not something the hardware said.
		crit = math.Min(crit, ceil.Crit)
	}

	// A chip whose published warning limit sits at or above its critical one
	// -- coretemp reports temp1_max == temp1_crit on some parts -- would
	// otherwise produce a warning that can never fire, because any reading
	// over it is already critical. Stepping warn back below crit keeps the two
	// severities distinguishable.
	if warn >= crit {
		warn = math.Min(warn, crit-math.Max(margin/2, 1))
	}

	return Bounds{Warn: warn, Crit: crit, Source: source}
}

// DeviationSeverity is what a reading is worth, or "" for one nobody needs to
// look at.
//
// Mirrors DiskSeverity: the caller passes a measurement, gets back a severity
// or an empty string, and no judgement lives anywhere else.
func DeviationSeverity(value float64, t Bounds) string {
	switch {
	case value >= t.Crit:
		return SeverityCritical
	case value >= t.Warn:
		return SeverityWarning
	default:
		return ""
	}
}

// FamilyRule is the per-family half of the rule: the margin floor and the
// ceiling that applies when the hardware is silent.
type FamilyRule struct {
	Floor    float64
	Ceilings Ceilings
	// Unit is what the number is in, for the sentence the UI writes.
	Unit string
}

// Chip families, named by the hwmon driver that registers them.
const (
	ChipNVMe      = "nvme"
	ChipDriveTemp = "drivetemp"
)

// temperatureRules are the per-chip margin floors and fallback ceilings.
//
// Only consulted when the chip published no limit of its own, which on modern
// hardware is mostly k10temp and acpitz. The two storage entries are here for
// the older drives that predate SCT limit reporting, not as a second opinion on
// the ones that have them.
var temperatureRules = map[string]FamilyRule{
	ChipNVMe:      {Floor: 4, Ceilings: Ceilings{Crit: 80}, Unit: "C"},
	ChipDriveTemp: {Floor: 4, Ceilings: Ceilings{Crit: 60}, Unit: "C"},
}

// defaultTemperatureRule covers every chip that is not a disk: packages, cores,
// the board. 85/95 C is where a CPU throttles and where it shuts down, which is
// the same pair the kernel itself acts on.
var defaultTemperatureRule = FamilyRule{Floor: 6, Ceilings: Ceilings{Crit: 95}, Unit: "C"}

// RuleFor returns the family rule for one kind and, for temperatures, one chip.
//
// processes and load carry no ceiling at all -- there is no count of processes
// that is wrong on every machine, which is precisely why they are judged by
// deviation and not by a constant.
func RuleFor(kind, chip string) FamilyRule {
	switch kind {
	case KindTemperature:
		if r, ok := temperatureRules[chip]; ok {
			return r
		}
		return defaultTemperatureRule
	case KindProcesses:
		return FamilyRule{Floor: 50, Unit: ""}
	case KindLoad:
		return FamilyRule{Floor: 0.5, Unit: ""}
	}
	return FamilyRule{}
}
