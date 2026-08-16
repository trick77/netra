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
/*
 * Its own token, and NOT --ink or --ink-2.
 *
 * Both of those inks inverture with the theme -- --ink is #1f1e1a in light and
 * #faf9f5 in dark -- so "make the rule less prominent" and "make it darker"
 * are the same request in light and opposite requests in dark. Reaching for
 * --ink put a near-white rule across every memory sparkline for anyone
 * reading in dark, which is the loudest mark on the row rather than the
 * quietest.
 *
 * --reference is defined per theme to sit at the SAME low prominence in
 * both: dim enough to read as a quiet annotation over the bands it crosses,
 * legible enough to find. A hairline with open dashes, for the same reason
 * -- it annotates the chart, it is not a series in it.
 */
export const REFERENCE_STROKE = "var(--reference)";
export const REFERENCE_DASH = "4 3";
export const REFERENCE_WIDTH = 1;

/**
 * How a mirrored up/down area is drawn -- a dimmed fill with a solid edge of
 * the same series colour.
 *
 * One definition, imported by both UpDownSparkline and Overlay's mirrored
 * branch, for the same reason the reference rule above has one: the two draw
 * the SAME reading. A fleet row's traffic cell and the host page's Interface
 * throughput panel are both rx above the midline and tx below it, through the
 * same mirrorPaths() geometry, and an operator scans one and then the other.
 * They were last reconciled by hand -- the sparkline had been left at a fully
 * opaque fill with no edge, so the same fact about the same host was two
 * different pictures -- and a comment saying "do not retune this on one side
 * alone" is not something a test can enforce. These constants are.
 *
 * Deliberately NOT shared with the stacked branches. A stack's 0.55 looks
 * like the same kind of number, but it answers a different question: bands
 * are layered over each other and the fill has to stay readable through the
 * one above it, where a mirrored pair has nothing behind it and only needs
 * to sit below its own edge. Tying them together would mean a future tune of
 * one silently moving the other.
 */
export const MIRROR_FILL_OPACITY = 0.45;
export const MIRROR_STROKE_WIDTH = 1.25;

/**
 * The rule marking a mirrored chart's midline -- where zero is.
 *
 * --border rather than --reference: a reference rule annotates the data (a
 * host's total RAM), while this one is the axis the data is drawn against,
 * and it is the same structural hairline as a card edge or a table rule.
 * Solid, not dashed, for that reason too.
 */
/* Stays --border, NOT the retuned --axis. This constant is the midline of a
   SPARKLINE -- the fleet row's traffic cell draws it -- and a sparkline
   carries no axis furniture at all, so its midline is a structural hairline
   like a table rule rather than an axis. Pointing it at --axis darkened
   every traffic cell in the fleet table, which UpDownSparkline's own test
   caught. A chart WITH furniture uses ZERO_STROKE below, which is a
   different mark answering a different question. */
export const AXIS_STROKE = "var(--border)";
export const AXIS_WIDTH = 1;

/**
 * The gridlines behind a chart, at two densities.
 *
 * MAJOR lines sit under the labelled ticks and carry the reading. MINOR
 * lines are the unlabelled helpers between them, and they are most of why an
 * RRDtool graph reads: they let the eye judge "a bit over 400M" without a
 * label at 450M.
 *
 * The inks live in index.css (--grid, --grid-minor) and are derived from a
 * measured contrast target rather than picked -- see the comment there
 * before changing either. The DASH is as load-bearing as the ink: the first
 * attempt used "1 4", a 20% duty cycle, which reads as nothing however dark
 * the line. Both levels are drawn with the same pattern so the lattice reads
 * as one grid at two weights, not as two different kinds of mark.
 */
export const GRID_MAJOR_STROKE = "var(--grid)";
export const GRID_MINOR_STROKE = "var(--grid-minor)";
export const GRID_DASH = "1 2";
export const GRID_WIDTH = 1;

/**
 * The midline of a mirrored chart -- where zero is.
 *
 * Stronger than a gridline and stronger than the spine, because on a
 * mirrored chart it is the line every reading is measured FROM. --border was
 * too quiet for that once a real grid sat behind it: zero looked like one
 * more helper line. Only for charts that draw furniture -- a sparkline's
 * midline is AXIS_STROKE above and does not change.
 */
export const ZERO_STROKE = "var(--border-strong)";
export const ZERO_WIDTH = 1;
