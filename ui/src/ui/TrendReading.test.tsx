import { describe, expect, it } from "vitest";
import { render } from "@testing-library/react";
import { Figure, TrendReading } from "./TrendReading";
import {
  READING_CARD_HEIGHT,
  READING_SPARK_HEIGHT,
  READING_SPARK_WIDTH,
} from "./charts/size";

describe("TrendReading", () => {
  it("draws a cell's trend at the fixed reading size, with no label", () => {
    const sizes: [number, number][] = [];
    const { container } = render(
      <TrendReading
        label="CPU"
        figure={<Figure pct={40} />}
        under="of 8 cores"
        trend={(w, h) => {
          sizes.push([w, h]);
          return <svg data-trend />;
        }}
      />,
    );

    expect(sizes.at(-1)).toEqual([READING_SPARK_WIDTH, READING_SPARK_HEIGHT]);
    expect(container.querySelector(".treading.cell")).toBeInTheDocument();
    // A cell has its column header; the label is for a card.
    expect(container.textContent).not.toContain("CPU");
    expect(container.textContent).toContain("of 8 cores");
  });

  it("names the quantity in a card and draws the trend at the card height", () => {
    const sizes: [number, number][] = [];
    const { container } = render(
      <TrendReading
        layout="card"
        label="Memory"
        figure={<Figure pct={30} />}
        trend={(w, h) => {
          sizes.push([w, h]);
          return null;
        }}
      />,
    );

    expect(container.querySelector(".treading-head .k")?.textContent).toBe(
      "Memory",
    );
    expect(sizes.at(-1)?.[1]).toBe(READING_CARD_HEIGHT);
  });

  it("never draws a fluid trend narrower than the fixed one", () => {
    const widths: number[] = [];
    render(
      <TrendReading
        fluid
        figure={null}
        trend={(w) => {
          widths.push(w);
          return null;
        }}
      />,
    );

    expect(Math.min(...widths)).toBeGreaterThanOrEqual(READING_SPARK_WIDTH);
  });

  it("prints nothing for a reading it does not have", () => {
    const { container } = render(
      <TrendReading figure={null} trend={() => null} />,
    );
    expect(container.textContent).toBe("");
  });
});

describe("Figure", () => {
  it("rounds, and carries the severity of the value", () => {
    const { container, rerender } = render(<Figure pct={95.6} />);
    expect(container.textContent).toBe("96%");
    expect(container.querySelector(".fig")?.className).toBe("fig st-crit");

    rerender(<Figure pct={71} />);
    expect(container.querySelector(".fig")?.className).toBe("fig st-warn");

    rerender(<Figure pct={12} />);
    expect(container.querySelector(".fig")?.className).toBe("fig st-ok");
  });

  it("names itself for a screen reader when given a label", () => {
    const { getByLabelText } = render(<Figure pct={40.2} label="CPU now" />);
    expect(getByLabelText("CPU now 40 %")).toBeInTheDocument();
  });
});
