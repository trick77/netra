// Where the numbers on an axis come from.
//
// Pure, with no React import and no DOM access, for the same reason
// geometry.ts gives: the behaviour that matters most here -- a tick landing
// on a round number rather than on whatever fraction of the data's peak
// happened to fall there -- can be pinned by a fast unit test instead of
// discovered later through a rendered chart.
//
// Every tick carries `major`. A major tick is labelled and carries the
// reading; the minor ticks between are unlabelled helpers, and they are what
// let a reader judge "a bit over 400M" without a label at 450M.

/** A position on an axis, as a fraction of the plot box, and its value. */
export interface Tick {
  /** 0 at the bottom of the plot box, 1 at the top. */
  fraction: number;
  value: number;
  /** Labelled, and drawn with the heavier grid ink. */
  major: boolean;
}

/** A position on a time axis. Minor ticks carry no label. */
export interface TimeTick {
  /** 0 at the left edge of the plot box, 1 at the right. */
  fraction: number;
  major: boolean;
  label: string | null;
}

/**
 * How many minor divisions sit between two labelled ticks.
 *
 * Four, matching what RRDtool draws. Two is too coarse to help the eye
 * subdivide, and eight turns the lattice into a texture that competes with
 * the series drawn over it.
 */
const MINOR_PER_MAJOR = 4;

/**
 * The largest "nice" step no bigger than `rough`.
 *
 * Rounds DOWN, and that is the whole point. Rounding up lets the step exceed
 * the range it is meant to divide: a 5.4 GiB memory stack asking for a
 * ~2.7 GiB step got 5 GiB -- larger than any tick that fits under the
 * ceiling -- so the axis came out with NO labelled tick at all and the panel
 * silently lost its ceiling label. Rounding down guarantees at least `count`
 * intervals fit.
 *
 * `base` selects the ladder. 1000 steps 1/2/5 x 10^n, which is right for
 * every decimal quantity. 1024 steps a POWER OF TWO within a power of 1024,
 * which is what a binaryBytes panel needs: ticked decimally, a 16 GiB host
 * reads 1.9 / 3.7 / 5.6 / 7.5 GiB, and every label on the axis is a ragged
 * number. In base 1024 the same axis reads 2 / 4 / 6 / 8 / 12 / 16 GiB.
 */
export function niceStep(rough: number, base: 1000 | 1024 = 1000): number {
  if (!Number.isFinite(rough) || rough <= 0) return 1;

  if (base === 1024) {
    // The largest power of 1024 at or below `rough`, then a power-of-two
    // mantissa within it. Powers of two throughout, so a tick is always a
    // whole number of KiB/MiB/GiB and the formatter prints it without a
    // fraction.
    const mag = Math.pow(1024, Math.floor(Math.log(rough) / Math.log(1024)));
    const norm = rough / mag;
    // The largest power of two at or below `norm`, which runs the whole way
    // to 512 -- `norm` lives in [1, 1024), not [1, 10). A ladder stopping at
    // 8 collapsed any step 16x or more above a power of 1024 down to 8, up
    // to 128 times too small: a 100 GiB filesystem asking for ~33 GiB steps
    // got 8 GiB, which is twelve labels and fifty ticks on a 260px panel.
    const mantissa = Math.pow(2, Math.floor(Math.log2(norm)));
    return mantissa * mag;
  }

  const mag = Math.pow(10, Math.floor(Math.log10(rough)));
  const norm = rough / mag;
  const mantissa = norm >= 5 ? 5 : norm >= 2 ? 2 : 1;
  return mantissa * mag;
}

/**
 * Ticks covering [min, max], stepping from `min` on round values.
 *
 * `count` is how many MAJOR intervals are wanted, not how many ticks come
 * back: the step is chosen from it and then walked, so a caller asking for 3
 * over a range that divides into 4 round steps gets 4. Asking for an exact
 * number of labels would mean labelling un-round values, which is the thing
 * this module exists to avoid.
 *
 * A degenerate range (max <= min) returns the single value at mid-height,
 * matching scaleY()'s own degenerate branch in geometry.ts: a series that
 * never moved is drawn as one line down the middle of the box, and the axis
 * has to say that one value at that one height rather than stepping a range
 * the shape does not have.
 */
export function niceTicks(
  min: number,
  max: number,
  count = 3,
  base: 1000 | 1024 = 1000,
): Tick[] {
  if (!(max > min)) return [{ fraction: 0.5, value: min, major: true }];

  const span = max - min;
  const step = niceStep(span / count, base);
  const minor = step / MINOR_PER_MAJOR;

  // Start at the first minor multiple at or above `min`, so ticks sit on
  // round values rather than at a round offset from a ragged floor.
  const first = Math.ceil(min / minor) * minor;
  const ticks: Tick[] = [];
  // Walk by index rather than accumulating `v += minor`: repeated addition
  // drifts, and a drifted value fails the major test below by a hair and
  // renders a labelled tick as an unlabelled one.
  for (let i = 0; ; i++) {
    const value = first + i * minor;
    // A hair of slack, or the tick exactly at `max` is dropped by floating
    // point and the axis loses its top label.
    if (value > max + span * 1e-9) break;
    ticks.push({
      fraction: (value - min) / span,
      value,
      major: isMultiple(value, step),
    });
  }
  return ticks;
}

/**
 * Ticks for an axis mirrored about zero -- ingress above the midline, egress
 * below -- where both edges are a magnitude of `ceiling` away from a zero in
 * the middle.
 *
 * Its own function rather than niceTicks(-ceiling, ceiling): the two halves
 * must be symmetric about the midline, and both label a MAGNITUDE. Running
 * a signed range through niceTicks would put "-200 M" below the line, which
 * states a negative rate -- traffic has a direction, not a sign.
 */
export function mirroredTicks(
  ceiling: number,
  count = 3,
  base: 1000 | 1024 = 1000,
): Tick[] {
  if (!(ceiling > 0)) return [{ fraction: 0.5, value: 0, major: true }];

  const step = niceStep(ceiling / count, base);
  const minor = step / MINOR_PER_MAJOR;
  const ticks: Tick[] = [{ fraction: 0.5, value: 0, major: true }];

  for (let i = 1; ; i++) {
    const value = i * minor;
    if (value > ceiling + ceiling * 1e-9) break;
    const half = (value / ceiling) * 0.5;
    const major = isMultiple(value, step);
    ticks.push({ fraction: 0.5 + half, value, major });
    ticks.push({ fraction: 0.5 - half, value, major });
  }
  return ticks;
}

/** Whether `value` sits on a multiple of `step`, tolerant of float drift. */
function isMultiple(value: number, step: number): boolean {
  const ratio = value / step;
  return Math.abs(ratio - Math.round(ratio)) < 1e-6;
}

// The time steps a reader recognises, in milliseconds. A chart's x axis must
// break on boundaries a human keeps time by -- the hour, the quarter hour,
// midnight -- never on the window's own length divided by seven, which puts
// a label at 14:23 and asks the reader to do arithmetic.
const SECOND = 1000;
const MINUTE = 60 * SECOND;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;
const TIME_STEPS = [
  MINUTE,
  5 * MINUTE,
  15 * MINUTE,
  30 * MINUTE,
  HOUR,
  3 * HOUR,
  6 * HOUR,
  12 * HOUR,
  DAY,
  7 * DAY,
];

const DAYS = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];
const MONTHS = [
  "Jan",
  "Feb",
  "Mar",
  "Apr",
  "May",
  "Jun",
  "Jul",
  "Aug",
  "Sep",
  "Oct",
  "Nov",
  "Dec",
];

function pad2(n: number): string {
  return String(n).padStart(2, "0");
}

/**
 * How a moment is written on an axis spanning `span` milliseconds.
 *
 * The granularity follows the window, because the ambiguity does. Inside a
 * day the date is understood and only the clock matters. Across days a bare
 * "18:00" appears twice on the same axis, so the weekday disambiguates it.
 * Past a week the weekday stops being enough and the date takes over.
 */
export function timeLabel(at: Date, span: number): string {
  const clock = `${pad2(at.getHours())}:${pad2(at.getMinutes())}`;
  if (span <= 26 * HOUR) return clock;
  if (span <= 8 * DAY) return `${DAYS[at.getDay()]} ${clock}`;
  return `${at.getDate()} ${MONTHS[at.getMonth()]}`;
}

/**
 * Ticks along a time axis, on boundaries rather than at even fractions.
 *
 * `targetMajors` is how many labels the axis has room for -- the caller
 * knows its own width, and a 260px panel fits three where a full page fits
 * eight. The coarsest step that yields at least that many is chosen, so the
 * labels never collide.
 *
 * Boundaries are computed in LOCAL time via Date, not by rounding the epoch
 * value: `Math.ceil(t / DAY) * DAY` lands on midnight UTC, which is not
 * midnight anywhere the reader lives, and every daily tick on the axis would
 * sit at an odd hour.
 */
export function timeTicks(
  from: number,
  to: number,
  targetMajors = 5,
): TimeTick[] {
  const span = to - from;
  if (!(span > 0)) return [];

  const step =
    TIME_STEPS.find((candidate) => span / candidate <= targetMajors) ??
    TIME_STEPS[TIME_STEPS.length - 1]!;
  const minor = minorStepFor(step);

  const ticks: TimeTick[] = [];
  for (let at = ceilTo(from, minor); at <= to; at = advance(at, minor)) {
    const major = isTimeBoundary(at, step);
    ticks.push({
      fraction: (at - from) / span,
      major,
      label: major ? timeLabel(new Date(at), span) : null,
    });
  }
  return ticks;
}

/**
 * The minor division of a time step.
 *
 * Not step/4 as on a value axis: time does not divide into four evenly at
 * every scale. A 3-hour major divides into hours (three minors, not four),
 * and a day divides into six-hour marks. Each entry is the largest step that
 * divides the major a small whole number of times.
 */
function minorStepFor(step: number): number {
  if (step >= 7 * DAY) return DAY;
  if (step >= DAY) return 6 * HOUR;
  if (step >= 12 * HOUR) return 3 * HOUR;
  if (step >= 6 * HOUR) return HOUR;
  if (step >= 3 * HOUR) return HOUR;
  if (step >= HOUR) return 15 * MINUTE;
  if (step >= 30 * MINUTE) return 10 * MINUTE;
  if (step >= 15 * MINUTE) return 5 * MINUTE;
  if (step >= 5 * MINUTE) return MINUTE;
  return 30 * SECOND;
}

/** The first local-time boundary of `step` at or after `at`. */
function ceilTo(at: number, step: number): number {
  const d = new Date(at);
  if (step >= DAY) {
    d.setHours(0, 0, 0, 0);
    // setHours already floored to local midnight; step forward until we
    // reach or pass `at`.
    while (d.getTime() < at) d.setDate(d.getDate() + 1);
    return d.getTime();
  }
  // Below a day, boundaries are regular offsets from local midnight, so
  // flooring within the day is exact even across a DST shift -- the shift
  // moves midnight, and every mark moves with it.
  d.setHours(0, 0, 0, 0);
  const midnight = d.getTime();
  const since = at - midnight;
  return midnight + Math.ceil(since / step) * step;
}

/** One `step` on from `at`, in local time. */
function advance(at: number, step: number): number {
  if (step < DAY) return at + step;
  const d = new Date(at);
  d.setDate(d.getDate() + Math.round(step / DAY));
  return d.getTime();
}

/** Whether `at` lands on a local boundary of `step`. */
function isTimeBoundary(at: number, step: number): boolean {
  const d = new Date(at);
  if (step >= 7 * DAY) return d.getDay() === 1 && isMidnight(d);
  if (step >= DAY) return isMidnight(d);
  const midnight = new Date(at);
  midnight.setHours(0, 0, 0, 0);
  const since = at - midnight.getTime();
  return Math.abs(since % step) < 1;
}

function isMidnight(d: Date): boolean {
  return (
    d.getHours() === 0 &&
    d.getMinutes() === 0 &&
    d.getSeconds() === 0 &&
    d.getMilliseconds() === 0
  );
}
