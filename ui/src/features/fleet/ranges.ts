// The windows an ENLARGED fleet chart offers.
//
// The fleet list itself no longer chooses a range: every row is drawn over
// FLEET_RANGE, fixed. These are the windows a reader can still switch to
// after opening one of those charts, where the question has changed from
// "scan the fleet" to "look at this one thing", and a wider or narrower
// window is part of looking.
//
// Not in FleetPage.tsx, where they used to live: the container list and the
// host columns need them for their enlarged charts, and importing them from
// the page that imports those components is a cycle. Exactly the reason and
// exactly the shape of features/host/ranges.ts.
import type { Range } from "../../lib/range";

/**
 * The one window every row in the fleet is drawn over.
 *
 * Fixed rather than chosen. A range picker over a list of sparklines asks
 * the reader to set up the question before they can ask it, and the answer
 * they wanted -- is anything unusual right now -- has one sensible window.
 * 12h also keeps the cell's fold honest: 144 five-minute buckets into 170
 * pixels is roughly one bucket per column, so almost nothing is averaged
 * away before it is drawn.
 */
export const FLEET_RANGE: Range = "12h";

/**
 * Exported so the screen that fetches for an enlarged chart can clamp a
 * remembered range to this set BEFORE asking the hub. A 30-day fan-out
 * across every host is a rollup nobody asked for, which is why this stops
 * at 24h.
 */
export const FLEET_RANGE_VALUES: readonly Range[] = ["1h", "6h", "12h", "24h"];
