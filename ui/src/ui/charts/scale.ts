// How a value becomes a height. Pure, with no React import and no DOM
// access, for the same reason geometry.ts and plot.ts are: the property that
// matters here -- that a quiet host is still visible on a chart a burst has
// been drawn on -- is a property of arithmetic, and a fast unit test can pin
// it without rendering anything.
//
// A scale maps a value to a FRACTION of the drawable half-height: 0 at the
// midline, 1 at the ceiling. Values above the ceiling return more than 1 and
// are left to overflow, matching what geometry.ts already documents about
// never clamping -- the overflow is itself informative.

/** Value to fraction of the plot's half-height. 0 is the midline, 1 the ceiling. */
export type Scale = (value: number) => number;

/**
 * Proportional. What every chart in this app did before there was a seam
 * here, and still the right answer for anything with a real ceiling: a CPU
 * percentage, a filesystem's fullness, a memory stack under the host's total
 * RAM. Those are bounded quantities, and bending their axis would draw a host
 * idling at 2 % CPU a third of the way up its cell.
 */
export function linearScale(max: number): Scale {
  return (v) => (max === 0 ? 0 : v / max);
}

/**
 * Linear below `knee`, logarithmic above it.
 *
 * For a quantity with no ceiling and a heavy tail, where a linear axis cannot
 * show the typical value and the peak at once. Measured on ark.o11.net, 24 h
 * to 2026-08-21 06:35 UTC, 286 five-minute buckets: typical traffic 29 kB/s,
 * peak 37 MB/s in the bucket means the cell draws (101 MB/s in the peaks it
 * used to draw) -- a range of ~1300:1, or ~3500:1 as it was. Scaled linearly,
 * the typical bucket draws 0.011 px of the fleet cell's 14 px half-height,
 * which is the flat line the whole cell had become. Scaling to the 95th
 * percentile instead reaches 0.196 px, so percentile clipping does not rescue
 * it either; only a non-linear axis does. Through asinh at TRAFFIC_KNEE, and
 * against that same 37 MB/s ceiling, the same bucket draws 5.03 px.
 *
 * asinh and not log10, for one decisive reason: asinh(0) is 0. A traffic
 * series has genuine zeros -- ark's second interface reads 0 for all 286
 * buckets -- and log10 is undefined there, so a log axis needs an invented
 * floor and every zero becomes a lie about how small the value was. asinh
 * needs no floor: it passes through the origin, is very nearly linear below
 * the knee, and only turns logarithmic above it.
 *
 * `knee` is where that turn happens, and it is deliberately NOT derived from
 * the data. A knee taken from each host's own median would make two cells in
 * the same column mean different things, and would make one cell's shape
 * change as its traffic drifted -- the reader would have no fixed rule for
 * what a height means.
 */
export function asinhScale(knee: number, max: number): Scale {
  if (!(knee > 0) || !(max > 0)) return () => 0;
  const ceiling = Math.asinh(max / knee);
  if (ceiling === 0) return () => 0;
  return (v) => Math.asinh(v / knee) / ceiling;
}

/**
 * The knee for a throughput axis, in bytes per second.
 *
 * 1 kB/s: below the quietest thing worth calling traffic, so the compression
 * begins above every reading an operator cares to distinguish, and the whole
 * interesting range -- tens of kB/s to tens of MB/s -- sits in the
 * logarithmic part where it gets room. Raising it to 10 kB/s costs ark's
 * typical bucket nearly half its height (5.03 px to 2.52 px), because that
 * knee sits inside the range being read rather than below it.
 */
export const TRAFFIC_KNEE = 1_000;

/**
 * A scale that has chosen its shape but not yet its ceiling.
 *
 * What a chart CONTAINER is handed, rather than a finished Scale. The
 * enlarged view of a cell refetches its own range and derives its own
 * ceiling, which is not the cell's; handed a scale already bound to the
 * cell's ceiling it would draw the shape against a number its data no longer
 * has. A factory lets each container apply the same curve to the ceiling it
 * actually drew.
 */
export type ScaleFactory = (max: number) => Scale;

/** The throughput curve, at whatever ceiling the chart ended up with. */
export const trafficScale: ScaleFactory = (max) =>
  asinhScale(TRAFFIC_KNEE, max);
