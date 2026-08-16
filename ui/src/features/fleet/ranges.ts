// The windows the fleet page OFFERS, in their own module.
//
// Not in FleetPage.tsx, where they used to live: the container list and the
// host columns need them for their enlarged charts, and importing them from
// the page that imports those components is a cycle. Exactly the reason and
// exactly the shape of features/host/ranges.ts.
import type { Range } from "../../lib/range";

/**
 * Exported so the screen that fetches for this page can clamp a remembered
 * range to this set BEFORE asking the hub: clamping inside the component
 * would leave the fetch on 7d while the toolbar showed 24h. A 30-day fan-out
 * across every host is a rollup nobody asked for, which is why this stops at
 * 24h.
 */
export const FLEET_RANGES: { value: Range; label: string }[] = [
  { value: "1h", label: "1h" },
  { value: "6h", label: "6h" },
  { value: "24h", label: "24h" },
];

/** The same set as the bare values clampRange takes. */
export const FLEET_RANGE_VALUES: readonly Range[] = FLEET_RANGES.map(
  (o) => o.value,
);
