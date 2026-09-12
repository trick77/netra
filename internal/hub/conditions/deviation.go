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

// DeviationThresholds caps a subject's own band by whatever hard limit applies.
//
// The band comes from the moving average (EWMA.Band); this decides how far it is
// allowed to drift. The calibrated pair sits underneath whichever hard limit applies, so the
// EARLY warning survives -- a drive that normally runs at 44 C should be
// noticed at 52, not held silent until its 80 C vendor limit. The hard limit is
// a ceiling on how far calibration may drift, which is what stops a subject
// that has been too hot all week from quietly calibrating its way past the
// point the hardware calls critical.
func DeviationThresholds(warn, crit float64, lim Limits, ceil Ceilings) Bounds {

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
		// A share of the band's own width, so the step is in the metric's unit
		// rather than a constant that means one thing for a temperature and
		// another for a load average. The 1 is the floor for a band so narrow
		// that a proportional step would round the two together.
		step := math.Max((crit-warn)/2, 1)
		warn = math.Min(warn, crit-step)
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
	// MinSpan is how much history this kind needs before it may judge.
	//
	// A DURATION, NOT A SAMPLE COUNT, and that distinction is the whole reason
	// this field was rewritten. It used to be 2000 for temperature and 8000 for
	// the host kinds: a scrape cadence multiplied by a calendar requirement,
	// readable as neither. Nobody looks at 8000 and thinks "five and a half
	// days", and the number silently meant something else if the cadence ever
	// changed. It also had to sit below a perfect week's worth of samples, or a
	// host that ever missed a scrape could never be judged at all -- a coupling
	// to packet loss that says nothing about whether the history is any good.
	// A span has none of that: it tolerates gaps completely, and it says what
	// it means.
	//
	// PER KIND BECAUSE THE CYCLE THEY MUST HAVE SEEN IS DIFFERENT, and getting
	// this wrong is visible on every fresh install. `processes` and `load` are
	// driven by what people ask of the machine, so they have a weekly shape: a
	// netra installed on a Saturday, judging after a day, has calibrated
	// entirely against a quiet weekend. Monday morning is then a departure from
	// normal on every host at once.
	//
	// Temperature keeps the short span. A drive has no weekday, its load-driven
	// swing is what the variance already measures, and it has the second tier
	// -- the chip's own published limit -- which owes nothing to history.
	//
	// The cost is honest and worth paying: the host kinds say nothing for the
	// first week a host reports. Nobody can know a machine's weekly rhythm in a
	// day, and a rule that pretends otherwise spends its first Monday crying
	// wolf, which is how a fleet learns to ignore its own attention list.
	MinSpan time.Duration
}

// Chip families, named by the hwmon driver that registers them.
const (
	ChipNVMe      = "nvme"
	ChipDriveTemp = "drivetemp"
)

// temperatureRules are the per-chip scale floors and fallback ceilings.
//
// Only consulted when the chip published no limit of its own, which on modern
// hardware is mostly k10temp and acpitz. The two storage entries are here for
// the older drives that predate SCT limit reporting, not as a second opinion on
// the ones that have them.
var temperatureRules = map[string]FamilyRule{
	ChipNVMe:      {Floor: 4, Ceilings: Ceilings{Crit: 80}, Unit: "C", MinSpan: 24 * time.Hour},
	ChipDriveTemp: {Floor: 4, Ceilings: Ceilings{Crit: 60}, Unit: "C", MinSpan: 24 * time.Hour},
}

// defaultTemperatureRule covers every chip that is not a disk: packages, cores,
// the board. 85/95 C is where a CPU throttles and where it shuts down, which is
// the same pair the kernel itself acts on.
var defaultTemperatureRule = FamilyRule{
	Floor: 6, Ceilings: Ceilings{Crit: 95}, Unit: "C", MinSpan: 24 * time.Hour,
}

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
		return FamilyRule{Floor: 50, MinSpan: TauSlow}
	case KindLoad:
		return FamilyRule{Floor: 0.5, MinSpan: TauSlow}
	}
	return FamilyRule{}
}
