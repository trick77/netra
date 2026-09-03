/**
 * The width every sparkline in a list uses.
 *
 * One constant rather than a default repeated in three components: the CPU,
 * memory and traffic cells sit side by side in a fleet row, and a row whose
 * charts are different widths reads as three unrelated pictures rather than
 * one host.
 *
 * It went 120 -> 170 on the argument that a stacked chart with thirty-two
 * bands needs horizontal room to show a shape at all, and it is now 150. The
 * twenty pixels are not withdrawn from that argument, they are spent on the
 * one axis a band actually occupies: SPARK_HEIGHT below went 32 -> 45 in the
 * same change. A thirty-two-core stack has more shape to show for a third
 * more height than for the last twenty pixels of width, where each band is a
 * ribbon a pixel tall whatever the width, and the row still has the length
 * back for the cells that are not charts.
 */
export const SPARK_WIDTH = 150;

/**
 * The height every sparkline in a list uses.
 *
 * Here for the reason the width is: it was a `32` written out separately in
 * Sparkline, StackedSparkline and UpDownSparkline, and a fourth time at the
 * fleet's filesystem cell. Four copies of one number are four numbers, and a
 * row whose four cells are different heights reads as four unrelated
 * pictures of four different hosts rather than one picture of one.
 *
 * 45 rather than 32 because at cell density the height IS the signal: a CPU
 * column whose hosts idle in single-digit percent was drawing its whole
 * reading inside about three pixels, and a mirrored traffic cell was
 * splitting what was left between rx and tx.
 */
export const SPARK_HEIGHT = 45;
/** The height of a sparkline that sits OVER a now-bar (ui/NowReading) in a
 * fleet cell rather than beside a reading: the bar and its unit line take
 * the rest of the row's height, and 26 keeps a CPU or Memory cell the same
 * height as the 45px traffic chart beside it. */
export const SPARK_STRIP_HEIGHT = 26;

/**
 * The width of the chart inside an enlarged view.
 *
 * Here rather than in ChartDetail because it is no longer only that
 * component's layout: a caller that folds its data to the pixel column it
 * will be drawn in -- see reduceToColumns() in lib/metrics -- has to know how
 * wide the dialog is before it fetches, and reading the number off the
 * component that draws it is what keeps the fold and the plot agreeing.
 */
export const DETAIL_WIDTH = 1000;

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
 * Both of those inks are TEXT inks, tuned to clear a reading floor -- --ink
 * measures 9.9:1 on the chart surface. Reaching for one put a bright rule
 * across every memory sparkline, which made the annotation the loudest mark
 * on the row rather than the quietest.
 *
 * --reference is tuned for the opposite job: dim enough to read as a quiet
 * annotation over the bands it crosses, legible enough to find, at 3.3:1.
 * A hairline with open dashes, for the same reason -- it annotates the
 * chart, it is not a series in it, which is also why a threshold rule drawn
 * with it never wears a status hue.
 */
export const REFERENCE_STROKE = "var(--reference)";
export const REFERENCE_DASH = "4 3";
export const REFERENCE_WIDTH = 1;

/**
 * The weight of the area under a line, and how it thins as a panel draws
 * more of them.
 *
 * 0.15 is what a lone filled series has always been drawn at. The divisor is
 * about what OVERLAPPING fills say: these panels draw INDEPENDENT lines, and
 * translucent areas from a common baseline compound where they overlap. Left
 * at 0.15 apiece, the six bases an IP fragmentation panel declares darken the
 * lower band toward 0.6, so it reads as a cumulative stack -- the one thing a
 * non-stacked panel must not look like -- and the lowest series ends up
 * buried under every other.
 *
 * Dividing by sqrt(count) thins it fast enough to stay readable without
 * conceding the mark: six series at 0.061 compound to about 0.31 where all
 * six cover each other, against 0.60 undivided, and the twelve a six-mount
 * Filesystem space panel draws reach 0.41 against 0.86. Not 1/count, which
 * over-corrects -- six at 0.025 is a fill nobody can see, and the point is to
 * keep the mark, not to give it up. Not the exact 1-(1-0.15)^(1/n) either,
 * which pins the worst case at 0.15 by making the common case, where two or
 * three series overlap, fainter than it needs to be.
 *
 * The ceiling is what makes the promise hold at counts nobody sized the
 * curve for. sqrt alone keeps climbing: it crosses half at about twenty
 * areas and reaches 0.53 at the twenty-four a twelve-mount Filesystem space
 * panel draws, which is back inside the cumulative-stack reading this exists
 * to prevent. The keyed panels are UNCAPPED -- bandsFor emits one pass per
 * key, so the count is however many mounts or disks a host happens to have,
 * and no formula tuned against six can be trusted there. Inverting the
 * composite gives the weight at which n areas land exactly on the ceiling,
 * and taking whichever is lower means the ground under a panel never goes
 * darker than AREA_OVERLAP_CEILING however many series share it. It only
 * binds past ~16 areas, so every count the paragraph above reasons about is
 * still the sqrt curve.
 *
 * Series count rather than plot width because compounding is a fact about
 * how many areas share a baseline, not about how wide they are drawn. A
 * panel and its enlarged view therefore weight the fill identically, which
 * is what keeps a chart the same chart on click.
 *
 * The count is what the panel DRAWS, not what its spec declares: the keyed
 * panels multiply bases by entities, so Filesystem space on six mounts
 * arrives here as twelve.
 */
export const AREA_FILL_OPACITY = 0.15;

/** How dark the deepest overlap on a panel is ever allowed to composite to. */
export const AREA_OVERLAP_CEILING = 0.45;

export function areaFillOpacity(count: number): number {
  if (count <= 1) return AREA_FILL_OPACITY;
  // 1 - (1 - a)^count = ceiling, solved for a.
  const capped = 1 - (1 - AREA_OVERLAP_CEILING) ** (1 / count);
  return Math.min(AREA_FILL_OPACITY / Math.sqrt(count), capped);
}

/**
 * How a mirrored up/down area is drawn -- a dimmed fill with a solid edge of
 * the same series colour.
 *
 * One definition, imported by both UpDownSparkline and Overlay's mirrored
 * branch, for the same reason the reference rule above has one: the two draw
 * the SAME reading. A fleet row's traffic cell and the host page's Traffic
 * panel are both rx above the midline and tx below it, through the same
 * mirrorPaths() geometry, and an operator scans one and then the other.
 *
 * Deliberately NOT shared with the stacked branches, which draw their bands
 * opaque. That used to read "a stack's 0.55 answers a different question:
 * bands are layered over each other and the fill has to stay readable
 * through the one above it" -- which is not what stackBands builds. Band k
 * is the ribbon between running total k-1 and running total k, so the bands
 * are disjoint and nothing sits behind one except the grid. The translucency
 * only let the gridline show through the data. Tying the two together would
 * still be wrong: a future tune of one would silently move the other.
 *
 * These are the SPARSE weights -- see mirrorEdge() below for when an edge is
 * drawn at all.
 */
export const MIRROR_FILL_OPACITY = 0.45;

/**
 * The edge on a filled band, whatever kind of band it is.
 *
 * One constant, because it is one mark: a CPU stack's ribbon, a memory
 * stack's ribbon and a mirrored traffic half are the same object drawn in
 * three panels an operator reads on one screen. Chart.tsx carried its own
 * `bandStroke = 1.25` default for the stacked marks while the mirrored ones
 * read this, so the two agreed by coincidence and a tune of either would
 * have silently moved only half the charts.
 */
export const BAND_STROKE_WIDTH = 1.25;

/**
 * The vertical headroom a stacked band spends, against `pad` for a line.
 *
 * Half its own edge, and nothing more. `pad` is two pixels because that is
 * what a LINE's stroke needs to clear the box edge; a filled band with a
 * BAND_STROKE_WIDTH outline needs half of that stroke, and the difference is
 * box the data can never reach. Spent at both ends of a 45px fleet cell it
 * was an eighth of the chart -- on a CPU column whose hosts idle in
 * single-digit percent, an eighth of what little signal there is.
 *
 * The same reasoning mirrorPaths already applies by spending the whole
 * height: its mark carries no stroke at cell density, so it spends
 * everything. Horizontal padding is untouched; the time axis is unmoved.
 *
 * Here rather than in Chart.tsx because four things have to agree on it: the
 * marks (stackBands' `yPad`), the axis furniture, the reference rule -- which
 * on the memory cell is the host's total RAM and has to land on the band edge
 * that reaches it -- and the tests that recompute stackBands to prove a panel
 * and its enlarged view cannot diverge.
 */
export const BAND_Y_PAD = BAND_STROKE_WIDTH / 2;

/**
 * Whether a mirrored chart at this density gets an edge, and how its fill is
 * weighted.
 *
 * An edge is only an edge while it is narrower than the thing it outlines.
 * The fleet's traffic cell draws one point per pixel, so a lone burst is a
 * triangle two pixels wide at its base -- and a 1.25px stroke at full opacity
 * runs down BOTH of those sides and along the midline, then has its apex
 * chopped square by the miter limit. What reaches the eye is a three-pixel
 * block whose edges are the stroke rather than the data: the pixel tower an
 * operator reported after comparing the cell against the same host in
 * Observium. The fill underneath, at 0.45, is the quieter mark.
 *
 * Antialiasing cannot rescue it. It softens the edges of what is drawn, and a
 * stroke is opaque in its middle by definition, so the mark stops fading
 * exactly where it should be finest. An un-stroked area has nothing but its
 * antialiased edge and therefore tapers to its apex, which is the shape a
 * spike is supposed to have.
 *
 * This is RRDtool's rule rather than an invention: a mirrored port graph in
 * Observium/LibreNMS is `AREA:in` plus `AREA:out_neg` with no `LINE`
 * directive on either half, and no alpha on the default inverted graph.
 *
 * Stated as a threshold rather than as two hard-coded sets of weights,
 * because the property that matters is a relation between the ink and the
 * data -- a chart with room for an edge keeps one, a chart without room does
 * not, and neither has to be listed here by name.
 *
 * It was collapsed to a single answer once, on the reasoning that rrdtool
 * fills an AREA solid and draws no LINE at any size, so a threshold was an
 * invention. That is true of rrdtool and it made the charts worse, for a
 * reason the fidelity argument cannot see: a translucent fill with nothing
 * behind it is not translucent to look at, it is just a dimmer flat block.
 * Measured through the band of a rendered panel, every row reads the same
 * #5d5e75 -- uniform, which is to say solid. The EDGE is the only thing that
 * makes an area read as an outline with something inside it, and an operator
 * asking for the band not to be "a solid fill" is asking for that edge.
 *
 * So the threshold stands, and it is doing two jobs rather than one: the
 * panel gets the outline that makes it read as a shape, and the cell -- where
 * a 1.25px stroke on a column of about the same width runs down both sides of
 * a spike and has its apex chopped square by the miter limit -- does not.
 */
export function mirrorEdge(
  plotWidth: number,
  points: number,
  pad = 0,
): { fillOpacity: number; strokeWidth: number } {
  // Spaced exactly as scaleX() spaces them -- inset by `pad` at both ends and
  // divided by the GAPS rather than the points. Measuring plotWidth/points
  // instead is close enough almost everywhere and wrong at the boundary: a
  // 170px plot with 134 points reads 1.269 that way and 1.248 the real way,
  // so the rule would keep a 1.25 edge on a column narrower than it -- the
  // one case it exists to catch. 170 because that is a width where the two
  // spacings land either side of the edge, not because anything is drawn at
  // it any more; the fleet cell is SPARK_WIDTH and the arithmetic is the
  // same wherever the boundary happens to fall.
  const column = points > 1 ? (plotWidth - 2 * pad) / (points - 1) : Infinity;
  return BAND_STROKE_WIDTH < column
    ? { fillOpacity: MIRROR_FILL_OPACITY, strokeWidth: BAND_STROKE_WIDTH }
    : { fillOpacity: 1, strokeWidth: 0 };
}

/**
 * The rule marking a mirrored chart's midline -- where zero is.
 *
 * --border rather than --reference: a reference rule annotates the data (a
 * host's total RAM), while this one is the axis the data is drawn against,
 * and it is the same structural hairline as a card edge or a table rule.
 * Solid, not dashed, for that reason too.
 */
/* The spine and the tick marks only. The midline of a mirrored chart is
   ZERO_STROKE below, on every size of that chart -- see the note there. */
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
 * more helper line, and --border-strong was only one step better.
 *
 * --axis, the ink this file's own note measures at 3.08:1, and on EVERY size
 * of the chart. A sparkline's midline was held at AXIS_STROKE on the argument
 * that a cell with no axis furniture wants a structural hairline rather than
 * an axis; but the traffic cell is a mirrored chart whose whole reading is
 * "how far above or below zero", and at --border that line was the one thing
 * on the cell you could not see. One zero line, one ink, at 150px and at
 * 1000px.
 */
export const ZERO_STROKE = "var(--axis)";
export const ZERO_WIDTH = 1;
