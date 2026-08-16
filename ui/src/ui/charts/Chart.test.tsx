import { describe, expect, it } from "vitest";
import { render } from "@testing-library/react";
import { Chart, type ChartSeries } from "./Chart";
import { niceTicks, timeTicks } from "./ticks";

const series: ChartSeries[] = [
  { name: "busy", color: "var(--s1)", values: [10, 40, 20, 60] },
];
const pair: ChartSeries[] = [
  { name: "in", color: "var(--s2)", values: [10, 40, 20, 60] },
  { name: "out", color: "var(--s5)", values: [5, 20, 10, 30] },
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
        />,
      );
      expect(c.querySelector("[data-zero]")).not.toBeNull();
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
  });
});
