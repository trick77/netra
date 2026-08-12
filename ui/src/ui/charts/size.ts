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
export const REFERENCE_STROKE = "var(--ink-2)";
export const REFERENCE_DASH = "4 3";
export const REFERENCE_WIDTH = 1;
