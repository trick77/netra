import { describe, expect, it } from "vitest";
import { render } from "@testing-library/react";
import { ChartFigure } from "./ChartFigure";
import type { ChartSeries } from "./Chart";

const series: ChartSeries[] = [
  { name: "in", color: "var(--in-1)", values: [10, 40, 20, 60] },
];

const answered = {
  from: "2026-08-28T00:00:00Z",
  to: "2026-08-29T00:00:00Z",
};

function draw() {
  return render(
    <ChartFigure
      series={series}
      width={600}
      height={200}
      min={0}
      max={100}
      format={(v) => `${v}`}
      window={answered}
      label="traffic"
    />,
  ).container;
}

describe("ChartFigure", () => {
  // The column used to be mounted on hover and unmounted on leave, so every
  // statistic in the table shifted sideways the moment the pointer crossed
  // the chart and shifted back when it left. The reading moved under the eye
  // about to read it.
  describe("the at-cursor column", () => {
    it("is drawn with no pointer over the plot", () => {
      const c = draw();
      const heads = [...c.querySelectorAll("thead th")].map(
        (th) => th.textContent,
      );
      expect(heads).toEqual(["Series", "At –", "Latest", "Min", "Max", "Mean"]);
    });

    it("reads absent in every row until a bucket is hovered", () => {
      const c = draw();
      const cells = [...c.querySelectorAll("tbody td.cursor")];
      expect(cells).toHaveLength(series.length);
      for (const cell of cells) expect(cell.textContent).toBe("–");
    });

    it("holds the same column count as the hovered table", () => {
      // The point of the change: the table's shape does not depend on where
      // the pointer is.
      const c = draw();
      expect(c.querySelectorAll("thead th")).toHaveLength(6);
      expect(c.querySelectorAll("tbody tr:first-child > *")).toHaveLength(6);
    });
  });
});
