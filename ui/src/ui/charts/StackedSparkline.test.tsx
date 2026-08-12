import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { stackBands } from "./geometry";
import { StackedSparkline } from "./StackedSparkline";

describe("StackedSparkline", () => {
  // 5 points, gap at index 2: two surviving runs of length >= 2 each
  // ([0,1] and [3,4]) -- geometry.stackBands() drops any run of length 1,
  // so a gap at the very edge would leave only one run and this test would
  // not actually exercise "every band breaks", just "every band shrinks".
  const bands = [
    { name: "A", color: "var(--s1)", values: [1, 2, 3, 4, 5] },
    { name: "B", color: "var(--s2)", values: [2, 3, null, 1, 2] },
  ];

  it("breaks every band at a gap in any one band, not just the null one", () => {
    const { container } = render(<StackedSparkline bands={bands} />);
    const paths = container.querySelectorAll("path[data-band]");
    expect(paths).toHaveLength(2);
    paths.forEach((p) => {
      const d = p.getAttribute("d") ?? "";
      expect(d.match(/M/g)?.length).toBe(2);
      expect(d.match(/Z/g)?.length).toBe(2);
    });
  });

  // A sparkline is a shape in a table cell, read at a glance beside four
  // other columns. A list of band names under it answers a question nobody
  // asks there, and at 32 cores it was taller than the row itself.
  it("names no bands: a sparkline carries a shape, not a key", () => {
    render(<StackedSparkline bands={bands} />);
    expect(screen.queryByText("A")).toBeNull();
    expect(screen.queryByText("B")).toBeNull();
  });

  it("computes the max as the largest running total across bands, not the largest single value", () => {
    const width = 100;
    const height = 20;
    const pad = 0;
    // running totals: i=0 -> 3, i=1 -> 5, i=2 -> gap (B is null), i=3 -> 5,
    // i=4 -> 7. The largest single value in either band is 5 (band A at
    // i=4); using that instead of 7 would overflow the plot box.
    const expectedMax = 7;
    const expected = stackBands(
      bands.map((b) => b.values),
      width,
      height,
      expectedMax,
      pad,
    );
    const { container } = render(
      <StackedSparkline
        bands={bands}
        width={width}
        height={height}
        pad={pad}
      />,
    );
    const paths = Array.from(container.querySelectorAll("path[data-band]"));
    expect(paths.map((p) => p.getAttribute("d"))).toEqual(expected);
  });

  it("takes colours from the caller", () => {
    const { container } = render(<StackedSparkline bands={bands} />);
    const paths = Array.from(container.querySelectorAll("path[data-band]"));
    expect(paths[0]?.getAttribute("fill")).toBe("var(--s1)");
    expect(paths[1]?.getAttribute("fill")).toBe("var(--s2)");
  });

  it("gives the chart an accessible name", () => {
    render(<StackedSparkline bands={bands} label="Disk usage by mount" />);
    expect(
      screen.getByRole("img", { name: "Disk usage by mount" }),
    ).toBeInTheDocument();
  });
});
