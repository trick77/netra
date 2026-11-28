import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { ChartCursorProvider, indexAt, timeAt } from "./cursor";
import { ChartPanel, type Band } from "./ChartPanel";

const window1h = {
  from: "2026-08-10T00:00:00Z",
  to: "2026-08-10T01:00:00Z",
};
const from = Date.parse(window1h.from);
const MIN = 60_000;

describe("timeAt and indexAt", () => {
  it("spread the buckets evenly across the window and back", () => {
    // 61 buckets over an hour: one a minute, both ends included
    expect(timeAt(0, window1h, 61)).toBe(from);
    expect(timeAt(60, window1h, 61)).toBe(from + 60 * MIN);
    expect(indexAt(from + 30 * MIN, window1h, 61)).toBe(30);
    // The nearest bucket, not the one before
    expect(indexAt(from + 30 * MIN + 40_000, window1h, 61)).toBe(31);
  });

  it("map one moment to each chart's own bucket", () => {
    // The same instant in a chart with half the resolution
    expect(indexAt(from + 30 * MIN, window1h, 31)).toBe(15);
  });

  it("place nothing outside the window or without one", () => {
    expect(indexAt(from - MIN, window1h, 61)).toBeNull();
    expect(indexAt(from + 61 * MIN, window1h, 61)).toBeNull();
    expect(indexAt(null, window1h, 61)).toBeNull();
    expect(indexAt(from, null, 61)).toBeNull();
    expect(indexAt(from, window1h, 1)).toBeNull();
    expect(timeAt(0, null, 61)).toBeNull();
    expect(timeAt(0, { from: "x", to: "y" }, 61)).toBeNull();
  });
});

// jsdom does no layout, so every element measures 0x0 and Chart ignores the
// pointer. Give every chart a 260x112 box at the origin.
let rect: ReturnType<typeof vi.spyOn>;
beforeEach(() => {
  rect = vi.spyOn(Element.prototype, "getBoundingClientRect").mockReturnValue({
    x: 0,
    y: 0,
    left: 0,
    top: 0,
    right: 260,
    bottom: 112,
    width: 260,
    height: 112,
    toJSON: () => ({}),
  } as DOMRect);
});
afterEach(() => rect.mockRestore());

const line = (name: string, color: string, values: number[]): Band => ({
  name,
  color,
  values,
});

function panels() {
  return render(
    <ChartCursorProvider>
      <ChartPanel
        title="CPU"
        series={[line("busy", "var(--s1)", [10, 20, 30, 40, 50])]}
        fmt={(n) => (n === null ? "-" : `${n}%`)}
        window={window1h}
      />
      <ChartPanel
        title="Disk"
        series={[
          {
            ...line("read", "var(--s1)", [1, 2, 3, 4, 5]),
            band: [9, 9, 9, 9, 9],
          },
          line("write", "var(--s2)", [5, 4, 3, 2, 1]),
        ]}
        stacked
        fmt={(n) => (n === null ? "-" : String(n))}
        window={window1h}
      />
    </ChartCursorProvider>,
  );
}

function svgOf(title: string) {
  return within(screen.getByRole("region", { name: `${title} chart` }))
    .getAllByRole("img")
    .find((el) => el.tagName.toLowerCase() === "svg")!;
}

describe("a section's shared cursor", () => {
  it("draws the crosshair in every chart and the readout in the hovered one", () => {
    // Given two panels in one section
    const { container } = panels();

    // When the pointer moves over the middle of the CPU chart
    fireEvent.pointerMove(svgOf("CPU"), { clientX: 154, clientY: 50 });

    // Then both charts draw a crosshair at that moment
    expect(container.querySelectorAll("[data-cursor]")).toHaveLength(2);
    // and only the hovered panel reads it out
    const readouts = screen.getAllByRole("status");
    expect(readouts).toHaveLength(1);
    expect(
      within(screen.getByRole("region", { name: "CPU chart" })).getByRole(
        "status",
      ),
    ).toHaveTextContent(/busy\s*30%/);
  });

  it("reads out every series, its peak and the stack's total", () => {
    panels();
    fireEvent.pointerMove(svgOf("Disk"), { clientX: 154, clientY: 50 });

    const readout = within(
      screen.getByRole("region", { name: "Disk chart" }),
    ).getByRole("status");
    expect(readout).toHaveTextContent("read3peak 9");
    expect(readout).toHaveTextContent("write3");
    expect(readout).toHaveTextContent("total6");
  });

  it("clears in every chart when the pointer leaves", () => {
    const { container } = panels();
    fireEvent.pointerMove(svgOf("CPU"), { clientX: 154, clientY: 50 });
    fireEvent.pointerLeave(svgOf("CPU"));

    expect(container.querySelectorAll("[data-cursor]")).toHaveLength(0);
    expect(screen.queryByRole("status")).toBeNull();
  });

  it("keeps the new owner's cursor when the old panel's leave arrives late", () => {
    const { container } = panels();
    fireEvent.pointerMove(svgOf("CPU"), { clientX: 154, clientY: 50 });
    // The pointer is already over Disk, at another bucket...
    fireEvent.pointerMove(svgOf("Disk"), { clientX: 250, clientY: 50 });
    // ...when CPU's leave is delivered
    fireEvent.pointerLeave(svgOf("CPU"));

    expect(container.querySelectorAll("[data-cursor]")).toHaveLength(2);
    expect(
      within(screen.getByRole("region", { name: "Disk chart" })).getByRole(
        "status",
      ),
    ).toBeInTheDocument();
  });

  it("works on its own outside a section", () => {
    const { container } = render(
      <ChartPanel
        title="CPU"
        series={[line("busy", "var(--s1)", [10, 20, 30, 40, 50])]}
        window={window1h}
      />,
    );
    fireEvent.pointerMove(svgOf("CPU"), { clientX: 154, clientY: 50 });

    expect(container.querySelectorAll("[data-cursor]")).toHaveLength(1);
    expect(screen.getByRole("status")).toHaveTextContent("busy30");
  });
});

describe("event markers on a panel", () => {
  const events = [
    {
      at: from + 30 * MIN,
      severity: "warning" as const,
      label: "disk filling",
    },
    { at: from + 45 * MIN, severity: "critical" as const, label: "host down" },
    { at: from + 15 * MIN, severity: "info" as const, label: "unit restarted" },
    // Outside the window: dropped, not pinned to an edge
    { at: from + 90 * MIN, severity: "info" as const, label: "later" },
  ];

  function marked() {
    return render(
      <ChartPanel
        title="CPU"
        series={[line("busy", "var(--s1)", [10, 20, 30, 40, 50])]}
        window={window1h}
        events={events}
      />,
    );
  }

  // Left to right along the axis, the events being far enough apart that no
  // two share pixels.
  it("draws one marker per event in the window, its severity in its shape", () => {
    const { container } = marked();
    const marks = container.querySelectorAll("[data-marker]");
    expect([...marks].map((m) => m.getAttribute("data-marker"))).toEqual([
      "info",
      "warning",
      "critical",
    ]);
    expect(marks[0]!.querySelector("rect")).not.toBeNull();
    expect(marks[1]!.querySelector("path")).not.toBeNull();
    expect(marks[2]!.querySelector("circle")).not.toBeNull();
  });

  it("lists the events near the cursor in the readout", () => {
    marked();
    // The middle bucket of five is 00:30, where the warning landed
    fireEvent.pointerMove(svgOf("CPU"), { clientX: 154, clientY: 50 });

    const readout = screen.getByRole("status");
    expect(readout).toHaveTextContent("disk filling");
    // Two buckets of fifteen minutes either side reach both neighbours
    expect(readout).toHaveTextContent("host down");
    expect(readout).toHaveTextContent("unit restarted");
    expect(readout).not.toHaveTextContent("later");
  });
});
