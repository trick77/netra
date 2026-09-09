import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { Meter } from "./Meter";
import { ABSENT } from "../lib/format";

// The bar a Meter draws is the fleet's SegmentBar, so what these assert on is
// the row of cells and how many of them are lit -- there is no fill width to
// read any more. The severity is a class on the bar rather than an inline
// colour, for the same reason: ten cells cannot be painted from a style
// attribute.
const bar = (container: HTMLElement) =>
  container.querySelector(".segbar") as HTMLElement | null;
const lit = (container: HTMLElement) =>
  container.querySelectorAll(".segbar i.on").length;

describe("Meter", () => {
  // A Docker container with no memory limit has nothing to be a
  // percentage of; drawing it against the host total would invent a
  // denominator that was never configured.
  it("renders 'no limit' rather than a bar against the host total", () => {
    render(<Meter noLimit />);
    expect(screen.getByText(/no limit/i)).toBeInTheDocument();
  });

  it("renders the absent marker rather than a bar when value or max is unknown", () => {
    const { container, rerender } = render(<Meter value={null} max={100} />);
    expect(screen.getByText(ABSENT)).toBeInTheDocument();
    expect(bar(container)).not.toBeInTheDocument();

    rerender(<Meter value={50} max={null} />);
    expect(screen.getByText(ABSENT)).toBeInTheDocument();
    expect(bar(container)).not.toBeInTheDocument();
  });

  it("lights cells to the nearest tenth of value/max", () => {
    const { container } = render(<Meter value={30} max={100} />);
    expect(bar(container)).toBeInTheDocument();
    expect(lit(container)).toBe(3);
  });

  // The floor SegmentBar's litCells() carries: a reading well under a tenth
  // still lights one cell, because an unlit row is what "nothing was
  // measured" looks like everywhere else in this app.
  it("lights one cell for a reading below a tenth", () => {
    const { container } = render(<Meter value={3} max={100} />);
    expect(lit(container)).toBe(1);
  });

  // Zero is a real, valid reading -- distinct from "not collected". A
  // value of 0 against a valid max must still draw a bar (an unlit one) and
  // a real "0 %" text, never the absent marker. This is the property most
  // likely to be broken by a future "tidy-up" that treats 0 as falsy.
  it("renders a real zero, distinct from absent", () => {
    const { container } = render(<Meter value={0} max={100} />);
    expect(bar(container)).toBeInTheDocument();
    expect(lit(container)).toBe(0);
    expect(screen.getByText("0%")).toBeInTheDocument();
    expect(screen.queryByText(ABSENT)).not.toBeInTheDocument();
  });

  // Pins that the value text comes from format.ts's `percent()` rather
  // than a hand-rolled `${n} %` string -- the brief requires reusing the
  // shared formatter, not re-implementing it.
  it("formats the value text with format.ts's percent()", () => {
    render(<Meter value={1} max={3} />);
    expect(screen.getByText("33%")).toBeInTheDocument();
  });

  // The bar cannot light more cells than it has, but the number beside it
  // must tell the truth: a container 150% over its memory limit is a real
  // and interesting state, and clamping the *displayed* value would make
  // it read as merely full. Only the bar is clamped; the text and
  // formatValue both receive the true, unclamped percentage.
  it("reports the true percentage past 100%, clamping only the bar", () => {
    const { container } = render(<Meter value={12} max={8} />);
    expect(lit(container)).toBe(10);
    expect(screen.getByText("150%")).toBeInTheDocument();
  });

  it("passes the unclamped percentage to formatValue", () => {
    const formatValue = (value: number, max: number, pct: number) =>
      `${value}/${max} (${pct}%)`;
    render(<Meter value={12} max={8} formatValue={formatValue} />);
    expect(screen.getByText("12/8 (150%)")).toBeInTheDocument();
  });

  // The status and series palettes are reached by class now, so what this
  // pins is that a Meter only ever asks for one of those seven -- never the
  // accent, which is chrome and not a fill. It whitelists every severity and
  // every series slot rather than asserting the absence of a word, so it
  // fails the moment someone wires the accent into a fill path.
  it("only ever fills with a status or series colour, never the accent", () => {
    const allowed = ["st-ok", "st-warn", "st-crit", "s1", "s2", "s3", "s4"];

    const severities = ["ok", "warning", "critical"] as const;
    for (const severity of severities) {
      const { container, unmount } = render(
        <Meter value={30} max={100} severity={severity} />,
      );
      const fill = bar(container)!;
      expect(allowed).toContain(fill.className.replace("segbar ", ""));
      unmount();
    }

    const seriesSlots = [1, 2, 3, 4] as const;
    for (const series of seriesSlots) {
      const { container, unmount } = render(
        <Meter value={30} max={100} series={series} />,
      );
      const fill = bar(container)!;
      expect(allowed).toContain(fill.className.replace("segbar ", ""));
      unmount();
    }
  });

  it("derives severity from thresholds when none is given explicitly", () => {
    const { container, rerender } = render(<Meter value={10} max={100} />);
    expect(bar(container)!.className).toContain("st-ok");

    rerender(<Meter value={98} max={100} />);
    expect(bar(container)!.className).toContain("st-crit");
  });

  it("accepts custom thresholds instead of hardcoding them", () => {
    const { container } = render(
      <Meter value={50} max={100} thresholds={{ warning: 40, critical: 80 }} />,
    );
    expect(bar(container)!.className).toContain("st-warn");
  });

  it("lets an explicit severity override the threshold calculation", () => {
    const { container } = render(
      <Meter value={10} max={100} severity="critical" />,
    );
    expect(bar(container)!.className).toContain("st-crit");
  });

  it("renders a label when given", () => {
    render(<Meter value={30} max={100} label="Memory" />);
    expect(screen.getByText("Memory")).toBeInTheDocument();
  });
});
