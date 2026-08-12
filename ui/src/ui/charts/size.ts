/**
 * The width every sparkline in a list uses.
 *
 * One constant rather than a default repeated in three components: the CPU,
 * memory and traffic cells sit side by side in a fleet row, and a row whose
 * charts are different widths reads as three unrelated pictures rather than
 * one host. Widened from 120 -- a stacked chart with thirty-two bands needs
 * the horizontal room to show a shape at all, and the row had space going
 * spare.
 */
export const SPARK_WIDTH = 170;

/**
 * How a reference rule is drawn -- the dashed line marking a ceiling, such
 * as a host's total memory.
 *
 * One definition, imported by both StackedSparkline and Overlay. They had
 * drifted to different dash patterns, different inks and different
 * opacities, so the same fact about the same host looked like three
 * different things depending on which chart a reader happened to be
 * looking at.
 */
/* --ink, not --ink-2. The rule marks the host's total RAM, which is the one
   thing on a memory chart that says whether a stack is nearly full or barely
   touched -- at secondary ink over a five-band stack it read as a chart
   gridline rather than as the ceiling, and on the fleet row it disappeared
   into the bands entirely. It is a stated fact about the host, so it is
   drawn in the same ink as text. */
export const REFERENCE_STROKE = "var(--ink)";
/* Denser dashes and a heavier stroke than a hairline. --ink is already the
   darkest token there is, so the rule reading faint was never about the
   colour: at 1px with 3px gaps there is barely any ink on screen, and over a
   five-band stack on a 32px-tall sparkline it vanished into the bands. More
   ink per unit length is the only lever left, and this is a stated ceiling
   rather than a gridline, so it should carry the weight. */
export const REFERENCE_DASH = "5 2";
export const REFERENCE_WIDTH = 1.75;
