/**
 * The figure a list cell prints beside its sparkline, and the line under it.
 *
 * One primitive for both lists. The fleet's CPU and Memory cells and the
 * container list's CPU cell all answer the same question -- "what does that
 * chart end on" -- and each had grown its own type: 14px ink with a unit line
 * here, 14px muted in a 44px column there, a meter's own value elsewhere. A
 * reader switching between the two lists should not have to re-learn what a
 * reading looks like.
 *
 * Fixed width and right-aligned, so the figures form a column down the page:
 * sized to their text, "8 %" and "100 %" start at different x and the eye has
 * to find each number instead of reading down them.
 *
 * The unit is set a step smaller and quieter after the figure -- "34 %", not
 * "34%" -- because at the same size the percent sign reads as one more digit
 * in a column of tabular figures.
 *
 * Callers decide when NOT to render one: the rule across both lists is that a
 * cell with nothing to report prints nothing at all, never a dash (a dash
 * asserts netra looked and found a value it could not print).
 */
export function Reading({
  value,
  unit,
  under,
}: {
  /** The figure itself, already rounded and formatted. */
  value: string;
  /** Its unit, if the figure has one: "%", "MB/s". */
  unit?: string;
  /** What the figure is measured against: "of 8 cores", "of 14.9 GiB". */
  under?: string;
}) {
  return (
    <div className="metric-read">
      <span className="v">
        {value}
        {unit !== undefined && <small>{unit}</small>}
      </span>
      {under !== undefined && <span className="u">{under}</span>}
    </div>
  );
}
