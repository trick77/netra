import { describe, expect, it } from "vitest";
import { render } from "@testing-library/react";
import { Chart, type ChartSeries } from "./Chart";
import { mirroredTicks, niceTicks, timeTicks } from "./ticks";

const series: ChartSeries[] = [
  { name: "busy", color: "var(--s1)", values: [10, 40, 20, 60] },
];
const pair: ChartSeries[] = [
  { name: "in", color: "var(--s2)", values: [10, 40, 20, 60] },
  { name: "out", color: "var(--s5)", values: [5, 20, 10, 30] },
];
// Two interfaces, as the mirrored stack takes them: consecutive in/out
// pairs, even up and odd down.
const twoPairs: ChartSeries[] = [
  { name: "eth0 in", color: "var(--s2)", values: [10, 12] },
  { name: "eth0 out", color: "var(--s5)", values: [20, 22] },
  { name: "eth1 in", color: "var(--in-2)", values: [30, 32] },
  { name: "eth1 out", color: "var(--out-2)", values: [40, 42] },
];

function draw(ui: React.ReactElement) {
  return render(ui).container;
}

describe("Chart", () => {
  // The property the whole library rests on: a caller that asks for no
  // furniture gets exactly the mark it drew before. That is what lets every
  // sparkline in the app move onto this renderer without changing.
  describe("furniture is opt-in", () => {
    it("draws no grid, spine, labels or crosshair by default", () => {
      const c = draw(
        <Chart series={series} width={170} height={32} max={100} />,
      );
      expect(c.querySelector("[data-grid]")).toBeNull();
      expect(c.querySelector("[data-spine]")).toBeNull();
      expect(c.querySelector("[data-axis-labels] text")).toBeNull();
      expect(c.querySelector("[data-crosshair]")).toBeNull();
    });

    // No furniture means no margins, so there is nothing to clip against and
    // nothing to translate by. The markup a sparkline emits is then exactly
    // what it emitted before it moved onto this renderer -- which is what
    // lets every fleet cell migrate without being re-checked by eye.
    it("emits no wrapper group or clip path when there is no furniture", () => {
      const c = draw(
        <Chart series={series} width={170} height={32} max={100} />,
      );
      expect(c.querySelector("g[transform]")).toBeNull();
      expect(c.querySelector("clipPath")).toBeNull();
    });

    // With labels the plot has to shrink to make room for them, and the mark
    // moves with it -- otherwise the series would be drawn over the axis.
    it("insets the mark once labels are asked for", () => {
      const c = draw(
        <Chart
          series={series}
          width={260}
          height={112}
          max={100}
          labels
          widestYLabel="500 MB/s"
          y={niceTicks(0, 100)}
        />,
      );
      const g = c.querySelector("g[clip-path]");
      expect(g?.getAttribute("transform")).not.toBe("translate(0,0)");
    });
  });

  describe("marks", () => {
    it("draws a line by default", () => {
      const c = draw(
        <Chart series={series} width={170} height={32} max={100} />,
      );
      expect(c.querySelectorAll("[data-line]").length).toBeGreaterThan(0);
      expect(c.querySelector("[data-area]")).toBeNull();
    });

    it("fills under the line when asked for an area", () => {
      const c = draw(
        <Chart series={series} width={170} height={32} max={100} mark="area" />,
      );
      expect(c.querySelector("[data-area]")).not.toBeNull();
      // The stroke stays: the fill reads as volume, the edge is the value.
      expect(c.querySelector("[data-line]")).not.toBeNull();
    });

    it("stacks bands cumulatively", () => {
      const c = draw(
        <Chart series={pair} width={170} height={32} max={100} mark="stack" />,
      );
      expect(c.querySelectorAll("[data-band]").length).toBe(2);
    });

    it("mirrors a pair about a midline", () => {
      const c = draw(
        <Chart series={pair} width={170} height={32} max={100} mark="mirror" />,
      );
      expect(c.querySelector("[data-up]")).not.toBeNull();
      expect(c.querySelector("[data-down]")).not.toBeNull();
      expect(c.querySelector("[data-mid]")).not.toBeNull();
    });

    // Two interfaces as in/out pairs: even series stack up, odd stack down.
    it("stacks mirrored pairs on either side of one midline", () => {
      const c = draw(
        <Chart
          series={twoPairs}
          width={170}
          height={32}
          max={100}
          mark="mirrorStack"
        />,
      );
      expect(c.querySelectorAll("[data-band][data-up]").length).toBe(2);
      expect(c.querySelectorAll("[data-band][data-down]").length).toBe(2);
      // ONE midline for the whole mark. The plain mirror draws its own
      // inside each pair's group, and four of them at the same y is a
      // heavier line than any other chart draws.
      expect(c.querySelectorAll("[data-mid]").length).toBe(1);
    });

    // A null is a host that reported nothing. It must leave a hole, not a
    // line drawn across it -- geometry.ts's rule, inherited here.
    it("breaks the mark at a null rather than bridging it", () => {
      const gapped: ChartSeries[] = [
        { name: "busy", color: "var(--s1)", values: [10, 20, null, 40, 50] },
      ];
      const c = draw(
        <Chart series={gapped} width={170} height={32} max={100} />,
      );
      expect(c.querySelectorAll("path[data-line]").length).toBe(2);
    });
  });

  describe("the peak envelope", () => {
    // The rollups carry avg AND max per bucket. Drawing both is what lets a
    // reader see the burst and the typical level at once.
    it("draws a band beneath the mean when the tier carries a peak", () => {
      const withBand: ChartSeries[] = [
        { ...pair[0]!, band: [20, 80, 40, 90] },
        { ...pair[1]!, band: [10, 40, 20, 50] },
      ];
      const c = draw(
        <Chart
          series={withBand}
          width={170}
          height={32}
          max={100}
          mark="mirror"
        />,
      );
      expect(c.querySelector("[data-band-up]")).not.toBeNull();
      expect(c.querySelector("[data-band-down]")).not.toBeNull();
    });

    // At the raw tier the sample IS its own peak and there is no _max column
    // to ask for, so the envelope must simply be absent rather than assumed.
    it("omits the band when the tier has no peak column", () => {
      const c = draw(
        <Chart series={pair} width={170} height={32} max={100} mark="mirror" />,
      );
      expect(c.querySelector("[data-band-up]")).toBeNull();
    });
  });

  describe("clipping", () => {
    // Clipping is not clamping. mirrorPaths deliberately lets a value escape
    // its box so an overflow stays visible; the clip only stops it reaching
    // the axis margins and painting over the labels.
    it("clips the mark to the plot rect", () => {
      const c = draw(
        <Chart
          series={series}
          width={260}
          height={112}
          max={100}
          labels
          widestYLabel="100%"
          y={niceTicks(0, 100)}
        />,
      );
      const clip = c.querySelector("clipPath");
      const marks = c.querySelector("g[clip-path]");
      expect(clip).not.toBeNull();
      expect(marks?.getAttribute("clip-path")).toBe(`url(#${clip?.id})`);
    });

    // jsdom does not clip, so this asserts the COORDINATE SPACE instead --
    // which is where the bug was. The clip rect is resolved in the user
    // space of the element referencing it, and MarkGroup already carries a
    // translate, so image-space coordinates on the rect are applied twice:
    // on a 260x112 panel the window landed at x >= 92 while the plot starts
    // at 48, and the oldest fifth of every axis-bearing chart plus the band
    // nearest the ceiling was drawn and then hidden.
    it("declares the clip rect in the group's space, not the image's", () => {
      const c = draw(
        <Chart
          series={series}
          width={260}
          height={112}
          max={100}
          labels
          widestYLabel="100%"
          y={niceTicks(0, 100)}
        />,
      );
      const rect = c.querySelector("clipPath rect")!;
      const group = c.querySelector("g[clip-path]")!;
      // The group is offset...
      expect(group.getAttribute("transform")).not.toBe("translate(0,0)");
      // ...so the rect must not be offset again.
      expect(rect.getAttribute("x")).toBe("0");
      expect(rect.getAttribute("y")).toBe("0");
    });

    it("gives two charts on one page different clip ids", () => {
      const withAxis = (
        <Chart
          series={series}
          width={260}
          height={112}
          max={100}
          labels
          widestYLabel="100%"
          y={niceTicks(0, 100)}
        />
      );
      const c = draw(
        <>
          {withAxis}
          {withAxis}
        </>,
      );
      const ids = [...c.querySelectorAll("clipPath")].map((n) => n.id);
      expect(ids).toHaveLength(2);
      expect(ids[0]).not.toBe(ids[1]);
    });
  });

  // The guarantee the whole migration rests on, tested at the one place it
  // was actually broken: ZeroRule was drawn for every mirrored chart, so a
  // fleet traffic cell gained a second, heavier midline on top of its own.
  describe("a mirrored sparkline is not given axis furniture", () => {
    it("draws no zero rule without a spine, grid or labels", () => {
      const c = draw(
        <Chart series={pair} width={170} height={32} max={100} mark="mirror" />,
      );
      expect(c.querySelector("[data-zero]")).toBeNull();
      // Its own midline, at the sparkline ink, is still there.
      expect(c.querySelector("[data-mid]")).not.toBeNull();
    });

    it("draws the zero rule once there is furniture to outrank", () => {
      const c = draw(
        <Chart
          series={pair}
          width={900}
          height={320}
          max={100}
          mark="mirror"
          spine
          y={mirroredTicks(100, 3)}
        />,
      );
      expect(c.querySelector("[data-zero]")).not.toBeNull();
    });

    it("rules zero where the data puts it, not across the middle", () => {
      // The mirror puts zero wherever the two halves' ranges land -- four
      // fifths of the way down on a host that pulls far more than it pushes.
      // The rule used to be pinned to 0.5 regardless, so it drew a grey line
      // across the middle of the box, over the marks and through the spikes.
      const c = draw(
        <Chart
          series={pair}
          width={900}
          height={320}
          max={100}
          mark="mirror"
          spine
        />,
      );
      const zero = c.querySelector("[data-zero]");
      const mid = c.querySelector("[data-mid]");
      expect(zero).not.toBeNull();
      expect(mid).not.toBeNull();
      // Both name the same height, which is the point: the axis furniture and
      // the marks measure from one line. Within a pixel rather than exactly:
      // the marks snap their midline to a whole row so the bars measured from
      // it fill rows rather than straddling them, and this rule is drawn at
      // the exact fraction.
      expect(
        Math.abs(
          Number(zero!.getAttribute("y1")) - Number(mid!.getAttribute("y1")),
        ),
      ).toBeLessThanOrEqual(1);
      // And it is not the midline of the box: `pair` is lopsided.
      expect(Number(zero!.getAttribute("y1"))).not.toBeCloseTo(320 / 2, 0);
    });
  });

  describe("crosshair", () => {
    const FROM = new Date(2026, 7, 15, 13, 18).getTime();
    const x = timeTicks(FROM, FROM + 3600_000, 4);

    it("is absent when nothing is hovered", () => {
      const c = draw(
        <Chart series={series} width={260} height={112} max={100} x={x} />,
      );
      expect(c.querySelector("[data-cursor]")).toBeNull();
    });

    it("marks the hovered bucket with a rule and a dot per series", () => {
      const c = draw(
        <Chart series={pair} width={260} height={112} max={100} cursor={1} />,
      );
      expect(c.querySelector("[data-cursor]")).not.toBeNull();
      expect(c.querySelectorAll("[data-cursor-dot]").length).toBe(2);
    });

    // The rule says WHERE, the dot says HOW MUCH. At a hole there is no value
    // to mark, and a dot at zero would state a reading nobody reported.
    it("rules a null bucket but puts no dot on it", () => {
      const gapped: ChartSeries[] = [
        { name: "busy", color: "var(--s1)", values: [10, null, 30] },
      ];
      const c = draw(
        <Chart series={gapped} width={260} height={112} max={100} cursor={1} />,
      );
      expect(c.querySelector("[data-cursor]")).not.toBeNull();
      expect(c.querySelector("[data-cursor-dot]")).toBeNull();
    });
  });

  // A stack draws band k at the RUNNING TOTAL through k. Dots placed at the
  // raw value bunched near the baseline while their own bands sat above.
  describe("crosshair on a mirrored stack", () => {
    it("reads each dot up its own half, skipping the other direction", () => {
      const c = draw(
        <Chart
          series={twoPairs}
          width={200}
          height={100}
          max={100}
          min={0}
          mark="mirrorStack"
          cursor={0}
        />,
      );
      const ys = [...c.querySelectorAll("[data-cursor-dot]")].map((d) =>
        Number(d.getAttribute("cy")),
      );
      // The crosshair reads the INSET rect, as the marks do, so on a
      // 100-tall box the midline is 50 and a half-height is 48. twoPairs at
      // index 0 is eth0 in 10 / out 20, eth1 in 30 / out 40, so the up half
      // stacks to 10 then 40 and the down half to 20 then 60. Summing ALL
      // four in order -- what the plain stack does -- would put eth1's
      // inbound dot at 60, on top of an outbound reading it is not drawn
      // over.
      const at = (total: number, dir: 1 | -1) => 50 + dir * (total / 100) * 48;
      expect(ys[0]!).toBeCloseTo(at(10, -1), 6);
      expect(ys[1]!).toBeCloseTo(at(20, 1), 6);
      expect(ys[2]!).toBeCloseTo(at(40, -1), 6);
      expect(ys[3]!).toBeCloseTo(at(60, 1), 6);
    });
  });

  describe("crosshair over a hole in a stack", () => {
    // The docstring's rule -- the rule says WHERE, the dot says HOW MUCH,
    // and at a hole there is no value to mark -- applies to the STACK a band
    // belongs to, not only to the band itself. Both stack geometries break
    // every band at an index where any of their series is null, so a dot
    // drawn at a raw value there floats over a hole nothing was drawn in.
    it("draws no dot for a half whose stack is broken there", () => {
      const holed: ChartSeries[] = [
        { name: "eth0 in", color: "var(--s2)", values: [10, 12] },
        { name: "eth0 out", color: "var(--s5)", values: [20, 22] },
        { name: "eth1 in", color: "var(--in-2)", values: [null, 32] },
        { name: "eth1 out", color: "var(--out-2)", values: [40, 42] },
      ];
      const c = draw(
        <Chart
          series={holed}
          width={200}
          height={100}
          max={100}
          min={0}
          mark="mirrorStack"
          cursor={0}
        />,
      );
      // eth1's inbound is the hole, and it takes eth0's inbound dot with it:
      // the up half draws no band at that index at all. The down half is
      // whole and keeps both of its dots.
      expect(c.querySelectorAll("[data-cursor-dot]").length).toBe(2);
      // And the rule itself is still drawn -- it says WHERE.
      expect(c.querySelector("[data-cursor]")).not.toBeNull();
    });
  });

  describe("crosshair on a stack", () => {
    it("puts each dot on its own band, not on its raw value", () => {
      const c = draw(
        <Chart
          series={pair}
          width={200}
          height={100}
          max={100}
          min={0}
          mark="stack"
          cursor={1}
        />,
      );
      const ys = [...c.querySelectorAll("[data-cursor-dot]")].map((d) =>
        Number(d.getAttribute("cy")),
      );
      // pair at index 1 is 40 and 20, so the bands top out at 40 and 60.
      // SVG y grows downward, so the second dot sits ABOVE the first.
      expect(ys).toHaveLength(2);
      expect(ys[1]!).toBeLessThan(ys[0]!);
    });
  });

  // geometry.ts insets every mark by `pad` inside the box it is given, so
  // furniture mapped across the full rect named heights the series was never
  // drawn at.
  describe("furniture lines up with the marks", () => {
    it("insets the grid by the same pad the marks use", () => {
      const pad = 6;
      const c = draw(
        <Chart
          series={series}
          width={200}
          height={100}
          max={100}
          min={0}
          pad={pad}
          grid
          y={niceTicks(0, 100, 1)}
        />,
      );
      const top = [...c.querySelectorAll("[data-grid][data-major]")]
        .map((l) => Number(l.getAttribute("y1")))
        .sort((a, b) => a - b)[0]!;
      // The topmost gridline names the ceiling, and the ceiling is drawn at
      // y = pad -- not at y = 0.
      expect(top).toBeCloseTo(pad, 6);
    });
  });

  describe("reference rule", () => {
    it("marks a ceiling with a dashed rule", () => {
      const c = draw(
        <Chart
          series={series}
          width={170}
          height={32}
          max={100}
          min={0}
          reference={80}
        />,
      );
      expect(c.querySelector("[data-reference]")).not.toBeNull();
    });

    // A threshold is only worth drawing where the series has reached it,
    // which is exactly where an opaque band covers it. It is not furniture
    // like the grid: the grid says where the values are, this says which
    // value matters.
    it("draws over the marks, not under them", () => {
      const c = draw(
        <Chart
          series={pair}
          width={170}
          height={32}
          max={100}
          min={0}
          mark="stack"
          reference={80}
        />,
      );

      const nodes = [...c.querySelectorAll("[data-band], [data-reference]")];
      expect(nodes.at(-1)!.hasAttribute("data-reference")).toBe(true);
    });
  });

  // The grid is furniture and belongs behind what it measures. It IS painted
  // first, but a translucent band let it show straight through the data --
  // and where a gridline crossed a band's own edge stroke, that edge read as
  // broken. Being behind is only half of it; the data has to be able to hide
  // it.
  describe("the grid stays behind the data", () => {
    it("fills stacked bands opaquely", () => {
      const c = draw(
        <Chart series={pair} width={170} height={32} max={100} mark="stack" />,
      );

      for (const band of c.querySelectorAll("[data-band]")) {
        expect(band.getAttribute("fill-opacity")).toBe("1");
      }
    });

    it("paints the grid before any mark", () => {
      const c = draw(
        <Chart
          series={pair}
          width={260}
          height={112}
          max={100}
          mark="stack"
          grid
          y={niceTicks(0, 100)}
        />,
      );

      const svg = c.querySelector("svg")!;
      // A layer is the element itself when MarkGroup renders no wrapper.
      const holds = (l: Element, sel: string) =>
        l.matches(sel) || l.querySelector(sel) !== null;
      const layers = [...svg.children];
      const grid = layers.findIndex((l) => holds(l, "[data-grid]"));
      const marks = layers.findIndex((l) => holds(l, "[data-band]"));
      expect(grid).toBeGreaterThanOrEqual(0);
      expect(grid).toBeLessThan(marks);
    });
  });
});
