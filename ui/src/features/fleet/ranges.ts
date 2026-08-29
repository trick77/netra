// The window the fleet list is drawn over.
//
// It used to also carry FLEET_RANGE_VALUES, the narrower set an enlarged
// fleet chart offered. That set is gone: the enlarged view's rail draws one
// fixed ladder wherever it is opened from (RAIL_RANGES in lib/range), because
// every tile is a separate read of ONE host and the fan-out this set existed
// to prevent never happens there.
//
// Not in FleetPage.tsx, where it used to live: the container list and the
// host columns need it, and importing it from the page that imports those
// components is a cycle. Exactly the reason and exactly the shape of
// features/host/ranges.ts.
import type { Range } from "../../lib/range";

/**
 * The one window every row in the fleet is drawn over.
 *
 * Fixed rather than chosen. A range picker over a list of sparklines asks
 * the reader to set up the question before they can ask it, and the answer
 * they wanted -- is anything unusual right now -- has one sensible window.
 *
 * A day, matching what the same hosts are graphed over elsewhere, so the two
 * pictures can be read against each other. It costs a little resolution in
 * the fold: 288 five-minute buckets into 150 pixels averages roughly 1.9 of
 * them per column, where 12h is about one.
 */
export const FLEET_RANGE: Range = "24h";
