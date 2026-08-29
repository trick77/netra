// The one test that exists to fail when the two mirrored charts drift apart.
//
// UpDownSparkline (a fleet row's traffic cell) and Overlay's mirrored branch
// (the host page's Traffic panel) draw the same reading -- rx above the
// midline, tx below it -- through the same mirrorPaths() geometry, and an
// operator scans one and then the other. They HAVE drifted before: the
// sparkline sat at a fully opaque fill with no edge while the panel had a
// dimmed fill and a solid one, so the same fact about the same host was two
// different pictures.
//
// What this pins has changed, because the weights are no longer a constant.
// A mirrored chart carries an edge only where the edge is narrower than one
// data column (mirrorEdge in size.ts), so the fleet cell at one point per
// pixel legitimately has none while a dialog at three and a half legitimately
// does. Freezing one pair of numbers would now forbid the correct answer.
//
// So the invariant is the RULE: two mirrored charts drawn at the same density
// agree, and both cross the threshold at the same place. That still fails on
// the drift it was written for -- one side retuned by hand differs at every
// density -- while allowing the difference that is a reading rather than an
// accident.
//
// It reads the rendered attributes of both rather than asserting that both
// files import the same constant: an import is not what the operator sees,
// and a component can always go back to hardcoding one.
import { describe, expect, it } from "vitest";
import { render } from "@testing-library/react";
import { Overlay } from "./Overlay";
import { UpDownSparkline } from "./UpDownSparkline";
import { SPARK_HEIGHT, SPARK_WIDTH } from "./size";

const rx = [1, 4, 2, 5];
const tx = [2, 1, 3, 1];

// Every attribute that decides how the mark looks. `d` is deliberately absent
// -- the two are drawn at different sizes, so their coordinates differ; it is
// the weights that must not.
const MARK_ATTRS = ["fill-opacity", "stroke-width"];

function marks(container: HTMLElement) {
  const read = (el: Element | null) =>
    el === null
      ? null
      : Object.fromEntries(MARK_ATTRS.map((a) => [a, el.getAttribute(a)]));
  const axis = container.querySelector("line[data-mid]");
  return {
    up: read(container.querySelector("path[data-up]")),
    down: read(container.querySelector("path[data-down]")),
    axis: {
      stroke: axis?.getAttribute("stroke"),
      "stroke-width": axis?.getAttribute("stroke-width"),
    },
  };
}

describe("mirrored charts", () => {
  // 150px against 260px with four points is 37 and 65 pixels per point: both
  // have room for an edge, so both must draw the same one.
  it("draws the traffic cell and the throughput panel as the same mark", () => {
    // Given the same rx/tx pair drawn by both mirrored charts, at the sizes
    // their real call sites use (hostColumns.tsx's 150x45 fleet cell and
    // ChartPanel.tsx's 260x64 panel)
    const sparkline = render(
      <UpDownSparkline
        up={rx}
        down={tx}
        max={5}
        upColor="var(--s1)"
        downColor="var(--s2)"
      />,
    );
    const overlay = render(
      <Overlay
        series={[
          { name: "rx", color: "var(--s1)", values: rx },
          { name: "tx", color: "var(--s2)", values: tx },
        ]}
        max={5}
        mirrored
        width={260}
        height={64}
        legend={false}
      />,
    );

    // When the weights of both marks are read
    // Then they are the same, down to the axis rule
    expect(marks(sparkline.container)).toEqual(marks(overlay.container));
  });

  // And the threshold itself is shared: hand both the density the fleet cell
  // actually has and neither draws an edge. A component that hardcoded its
  // own weights would keep drawing one here.
  it("crosses the no-edge threshold in the same place", () => {
    const dense = Array.from({ length: SPARK_WIDTH }, (_, i) =>
      i === 80 ? 100 : 1,
    );
    const sparkline = render(
      <UpDownSparkline
        up={dense}
        down={dense}
        max={100}
        upColor="var(--s1)"
        downColor="var(--s2)"
      />,
    );
    const overlay = render(
      <Overlay
        series={[
          { name: "rx", color: "var(--s1)", values: dense },
          { name: "tx", color: "var(--s2)", values: dense },
        ]}
        max={100}
        mirrored
        width={SPARK_WIDTH}
        height={SPARK_HEIGHT}
        legend={false}
      />,
    );

    expect(marks(sparkline.container)).toEqual(marks(overlay.container));
    expect(
      sparkline.container
        .querySelector("path[data-up]")
        ?.getAttribute("stroke-width"),
    ).toBe("0");
  });
});
