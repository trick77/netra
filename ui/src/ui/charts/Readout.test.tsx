import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { Readout } from "./Readout";

const window1h = { from: "2026-08-10T00:00:00Z", to: "2026-08-10T01:00:00Z" };
const at = Date.parse("2026-08-10T00:30:00Z");

describe("Readout", () => {
  it("says a missing bucket has no reading rather than printing zero", () => {
    render(
      <Readout
        series={[{ name: "busy", color: "var(--s1)", values: [1, null, 3] }]}
        index={1}
        at={at}
        window={window1h}
        fraction={0.5}
      />,
    );
    expect(screen.getByRole("status")).toHaveTextContent("busyno reading");
  });

  it("names how many series it left to the enlarged view", () => {
    const series = Array.from({ length: 11 }, (_, i) => ({
      name: `core ${i}`,
      color: "var(--s1)",
      values: [i],
    }));
    render(
      <Readout
        series={series}
        index={0}
        at={at}
        window={window1h}
        total
        fraction={0}
      />,
    );
    const readout = screen.getByRole("status");
    expect(readout).toHaveTextContent("core 7");
    expect(readout).not.toHaveTextContent("core 8");
    expect(readout).toHaveTextContent("3 more in the enlarged view");
    // The total still counts every series, shown or not: 0 + 1 + ... + 10
    expect(readout).toHaveTextContent("total55");
  });

  it("leaves the total out when no series has a reading there", () => {
    render(
      <Readout
        series={[{ name: "a", color: "var(--s1)", values: [null] }]}
        index={0}
        at={at}
        window={window1h}
        total
        fraction={0}
      />,
    );
    expect(screen.getByRole("status")).not.toHaveTextContent("total");
  });
});
