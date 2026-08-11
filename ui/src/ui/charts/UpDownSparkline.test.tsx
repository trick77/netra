import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { mirrorPaths } from "./geometry";
import { UpDownSparkline } from "./UpDownSparkline";

describe("UpDownSparkline", () => {
  // 5 points, gap at index 2: two surviving runs of length >= 2 each
  // ([0,1] and [3,4]) -- mirrorPaths() drops any run of length 1, so a gap
  // too close to an edge would leave only one run and not actually prove
  // "the up side broke into two fills".
  it("breaks the up fill at a gap while the down fill stays whole", () => {
    const up = [1, 2, null, 4, 3];
    const down = [1, 1, 1, 1, 1];
    const { container } = render(
      <UpDownSparkline up={up} down={down} max={4} />,
    );
    const upPath = container.querySelector("path[data-up]");
    const downPath = container.querySelector("path[data-down]");
    expect(upPath?.getAttribute("d")?.match(/M/g)?.length).toBe(2);
    expect(downPath?.getAttribute("d")?.match(/M/g)?.length).toBe(1);
  });

  it("renders the exact geometry output with no post-processing", () => {
    const up = [1, 2, null, 4, 3];
    const down = [1, 1, 1, 1, 1];
    const width = 100;
    const height = 20;
    const pad = 2;
    const max = 4;
    const expected = mirrorPaths(up, down, width, height, max, pad);
    const { container } = render(
      <UpDownSparkline
        up={up}
        down={down}
        max={max}
        width={width}
        height={height}
        pad={pad}
      />,
    );
    expect(container.querySelector("path[data-up]")?.getAttribute("d")).toBe(
      expected.up,
    );
    expect(container.querySelector("path[data-down]")?.getAttribute("d")).toBe(
      expected.down,
    );
  });

  it("takes colours from the caller, defaulting to series tokens not hex", () => {
    const { container } = render(
      <UpDownSparkline
        up={[1, 2]}
        down={[1, 2]}
        max={4}
        upColor="var(--s3)"
        downColor="var(--s4)"
      />,
    );
    expect(container.querySelector("path[data-up]")?.getAttribute("fill")).toBe(
      "var(--s3)",
    );
    expect(
      container.querySelector("path[data-down]")?.getAttribute("fill"),
    ).toBe("var(--s4)");
  });

  it("gives the chart an accessible name", () => {
    render(
      <UpDownSparkline
        up={[1, 2]}
        down={[1, 2]}
        max={4}
        label="Inbound/outbound traffic"
      />,
    );
    expect(
      screen.getByRole("img", { name: "Inbound/outbound traffic" }),
    ).toBeInTheDocument();
  });
});
