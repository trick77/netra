// The windows the host page OFFERS, in their own module.
//
// Not in HostPage.tsx, where they used to live: the Graphs tab needs them
// for its enlarged charts, and importing them from the page that imports the
// tab is a cycle. The type and the resolution are lib/range's -- the hub
// rejects relative times outright, and one module converting them means one
// place to be wrong.
import type { Range } from "../../lib/range";

export const RANGE_OPTIONS: { value: Range; label: string }[] = [
  { value: "1h", label: "1h" },
  { value: "6h", label: "6h" },
  { value: "24h", label: "24h" },
  { value: "7d", label: "7d" },
  // Past a week the step widens with the window (lib/range SPEC): 30d reads
  // the hourly aggregate (720 points), 3mo and 12mo the daily one (90, 365),
  // so a wider button never means a heavier chart than the 30d rail tile.
  { value: "30d", label: "30d" },
  { value: "3mo", label: "3mo" },
  { value: "12mo", label: "12mo" },
];

/** The same set as the bare values clampRange takes. */
export const RANGE_VALUES: readonly Range[] = RANGE_OPTIONS.map((o) => o.value);
