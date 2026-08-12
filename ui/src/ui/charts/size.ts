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
