/**
 * The severity of a row, as a mark rather than a word.
 *
 * Badge is still the component for a status that has something to SAY -- a
 * container state, a systemd unit, the host page header. This one is for the
 * fleet list, where the word was the same four letters repeated down the
 * leftmost column and the reader is scanning for which rows have a mark at
 * all, not reading them.
 *
 * TWO GLYPHS, not one glyph in two colours. Badge's header sets out the rule
 * this has to keep (spec §3.3): netra's amber and its critical red measure
 * ΔE 2.2 under deuteranopia, so a triangle painted in each is one mark to a
 * reader who cannot separate the hues -- exactly the bare coloured dot
 * Badge's required `children` exists to prevent. Badge answers it with a
 * word; this answers it with SHAPE. A triangle and an octagon are different
 * marks in greyscale, at 14px, and at a glance, and the hue then reinforces
 * a distinction that no longer depends on it.
 *
 * The octagon is the road sign, and it is deliberate: triangle warns,
 * octagon stops. A reader who has never seen this table knows which of the
 * two outranks the other without a legend.
 *
 * role="img" with an aria-label, NOT aria-hidden: OsIcon may hide itself
 * because the OS name is spelled out beside it, and here nothing is. The
 * word is what a screen reader gets, so the column still says "critical" to
 * anyone not reading it by eye.
 */
export type MarkSeverity = "warning" | "critical";

const SEVERITY_CLASS: Record<MarkSeverity, string> = {
  warning: "st-warn",
  critical: "st-crit",
};

// A 16-box for both, so the two marks occupy the same area and a column of
// them lines up regardless of which severity a row is at. The triangle is
// inset a little at the top and full-width at the base -- an isoceles drawn
// corner to corner in the box reads smaller than the octagon beside it,
// because a triangle covers half the area a near-circle does.
const PATHS: Record<MarkSeverity, string> = {
  // Apex at the top, base on the floor, inset 0.8 from each edge so the
  // corners are not clipped by the viewBox at 13px.
  warning: "M8 1.5 15.2 14.2H0.8Z",
  // A regular octagon: the corner cut of a side-16 square is 16/(2+√2).
  critical: "M4.69 0H11.31L16 4.69V11.31L11.31 16H4.69L0 11.31V4.69Z",
};

export function SeverityMark({ severity }: { severity: MarkSeverity }) {
  return (
    <svg
      className={`smark ${SEVERITY_CLASS[severity]}`}
      viewBox="0 0 16 16"
      width="13"
      height="13"
      fill="currentColor"
      role="img"
      aria-label={severity}
      focusable="false"
    >
      <path d={PATHS[severity]} />
    </svg>
  );
}
