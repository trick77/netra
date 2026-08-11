import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { Meter } from "./Meter";

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
    expect(screen.getByText("—")).toBeInTheDocument();
    expect(container.querySelector(".meter")).not.toBeInTheDocument();

    rerender(<Meter value={50} max={null} />);
    expect(screen.getByText("—")).toBeInTheDocument();
    expect(container.querySelector(".meter")).not.toBeInTheDocument();
  });

  it("draws a filled bar sized to value/max", () => {
    const { container } = render(<Meter value={30} max={100} />);
    const fill = container.querySelector(".meter i") as HTMLElement;
    expect(fill).toBeInTheDocument();
    expect(fill.style.width).toBe("30%");
  });

  // Zero is a real, valid reading -- distinct from "not collected". A
  // value of 0 against a valid max must still draw a (zero-width) bar and
  // a real "0 %" text, never the absent marker. This is the property most
  // likely to be broken by a future "tidy-up" that treats 0 as falsy.
  it("renders a real zero, distinct from absent", () => {
    const { container } = render(<Meter value={0} max={100} />);
    expect(container.querySelector(".meter")).toBeInTheDocument();
    const fill = container.querySelector(".meter i") as HTMLElement;
    expect(fill.style.width).toBe("0%");
    expect(screen.getByText("0 %")).toBeInTheDocument();
    expect(screen.queryByText("—")).not.toBeInTheDocument();
  });

  // Pins that the value text comes from format.ts's `percent()` rather
  // than a hand-rolled `${n} %` string -- the brief requires reusing the
  // shared formatter, not re-implementing it.
  it("formats the value text with format.ts's percent()", () => {
    render(<Meter value={1} max={3} />);
    expect(screen.getByText("33 %")).toBeInTheDocument();
  });

  // The bar cannot be drawn wider than its track, but the number beside it
  // must tell the truth: a container 150% over its memory limit is a real
  // and interesting state, and clamping the *displayed* value would make
  // it read as merely full. Only the bar width is clamped; the text and
  // formatValue both receive the true, unclamped percentage.
  it("reports the true percentage past 100%, clamping only the bar width", () => {
    const { container } = render(<Meter value={12} max={8} />);
    const fill = container.querySelector(".meter i") as HTMLElement;
    expect(fill.style.width).toBe("100%");
    expect(screen.getByText("150 %")).toBeInTheDocument();
  });

  it("passes the unclamped percentage to formatValue", () => {
    const formatValue = (value: number, max: number, pct: number) =>
      `${value}/${max} (${pct}%)`;
    render(<Meter value={12} max={8} formatValue={formatValue} />);
    expect(screen.getByText("12/8 (150%)")).toBeInTheDocument();
  });

  // STATUS_VAR and SERIES_VAR never contain --accent by construction, so a
  // test that renders one configuration and asserts the absence of the
  // word "accent" cannot fail on any reachable input. This whitelists
  // every severity and every series slot instead, so it fails the moment
  // someone later wires the accent into a fill path.
  it("only ever fills with a status or series colour, never the accent", () => {
    const allowed = [
      "var(--st-ok)",
      "var(--st-warn)",
      "var(--st-serious)",
      "var(--st-crit)",
      "var(--s1)",
      "var(--s2)",
      "var(--s3)",
      "var(--s4)",
    ];

    const severities = ["ok", "warning", "serious", "critical"] as const;
    for (const severity of severities) {
      const { container, unmount } = render(
        <Meter value={30} max={100} severity={severity} />,
      );
      const fill = container.querySelector(".meter i") as HTMLElement;
      expect(allowed).toContain(fill.style.background);
      unmount();
    }

    const seriesSlots = [1, 2, 3, 4] as const;
    for (const series of seriesSlots) {
      const { container, unmount } = render(
        <Meter value={30} max={100} series={series} />,
      );
      const fill = container.querySelector(".meter i") as HTMLElement;
      expect(allowed).toContain(fill.style.background);
      unmount();
    }
  });

  it("derives severity from thresholds when none is given explicitly", () => {
    const { container, rerender } = render(<Meter value={10} max={100} />);
    let fill = container.querySelector(".meter i") as HTMLElement;
    expect(fill.style.background).toContain("--st-ok");

    rerender(<Meter value={98} max={100} />);
    fill = container.querySelector(".meter i") as HTMLElement;
    expect(fill.style.background).toContain("--st-crit");
  });

  it("accepts custom thresholds instead of hardcoding them", () => {
    const { container } = render(
      <Meter
        value={50}
        max={100}
        thresholds={{ warning: 40, serious: 60, critical: 80 }}
      />,
    );
    const fill = container.querySelector(".meter i") as HTMLElement;
    expect(fill.style.background).toContain("--st-warn");
  });

  it("lets an explicit severity override the threshold calculation", () => {
    const { container } = render(
      <Meter value={10} max={100} severity="critical" />,
    );
    const fill = container.querySelector(".meter i") as HTMLElement;
    expect(fill.style.background).toContain("--st-crit");
  });

  it("renders a label when given", () => {
    render(<Meter value={30} max={100} label="Memory" />);
    expect(screen.getByText("Memory")).toBeInTheDocument();
  });
});
