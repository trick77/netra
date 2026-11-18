import { describe, expect, it } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { HeatStrip } from "./HeatStrip";
import { ChartCursorProvider, useSharedCursor } from "./cursor";

describe("HeatStrip", () => {
  it("draws a row per series in one ink, brighter where busier", () => {
    const { container } = render(
      <HeatStrip
        label="per core"
        rows={[
          { name: "core 0", values: [0, 100] },
          { name: "core 1", values: [50, null] },
        ]}
      />,
    );
    const cells = [...container.querySelectorAll("rect[data-cell]")];
    // A null cell is not drawn: no reading is not idle
    expect(cells).toHaveLength(3);
    for (const c of cells) expect(c.getAttribute("fill")).toBe("var(--ink)");
    const opacity = cells.map((c) => Number(c.getAttribute("fill-opacity")));
    // Idle is faint but visible, busy is full
    expect(opacity[0]).toBeGreaterThan(0);
    expect(opacity[1]).toBe(1);
    expect(opacity[2]).toBeGreaterThan(opacity[0]!);
    expect(opacity[2]).toBeLessThan(1);
    expect(screen.getByRole("img", { name: "per core" })).toBeInTheDocument();
  });

  it("folds more buckets than fit into columns, keeping each column's peak", () => {
    // jsdom measures nothing, so the strip draws at its 600px fallback:
    // 200 columns of 3px for 1000 buckets
    const values = Array.from({ length: 1000 }, (_, i) => (i === 7 ? 100 : 0));
    const { container } = render(
      <HeatStrip label="x" rows={[{ name: "core 0", values }]} />,
    );
    const cells = [...container.querySelectorAll("rect[data-cell]")];
    expect(cells).toHaveLength(200);
    // The burst survives the fold
    expect(
      cells.filter((c) => c.getAttribute("fill-opacity") === "1"),
    ).toHaveLength(1);
  });

  it("draws no cursor without a section's one", () => {
    const { container } = render(
      <ChartCursorProvider>
        <HeatStrip
          label="x"
          rows={[{ name: "core 0", values: [1, 2] }]}
          window={{ from: "2026-08-10T00:00:00Z", to: "2026-08-10T01:00:00Z" }}
        />
      </ChartCursorProvider>,
    );
    expect(container.querySelector("[data-cursor]")).toBeNull();
  });

  // The crosshair sits on the centre of the column its bucket was folded
  // into, so it lines up with the cell rather than drifting off it.
  it("draws the section's cursor over the hovered bucket's column", () => {
    function Hover() {
      const shared = useSharedCursor()!;
      return (
        <button
          type="button"
          onClick={() =>
            shared.set(Date.parse("2026-08-10T01:00:00Z"), "other")
          }
        >
          hover
        </button>
      );
    }
    const { container } = render(
      <ChartCursorProvider>
        <Hover />
        <HeatStrip
          label="x"
          rows={[{ name: "core 0", values: [1, 2, 3, 4] }]}
          window={{ from: "2026-08-10T00:00:00Z", to: "2026-08-10T01:00:00Z" }}
        />
      </ChartCursorProvider>,
    );
    fireEvent.click(screen.getByRole("button", { name: "hover" }));
    // The newest of four buckets, four 150px columns: the last one's centre
    expect(container.querySelector("[data-cursor]")!.getAttribute("x1")).toBe(
      "525",
    );
  });
});
