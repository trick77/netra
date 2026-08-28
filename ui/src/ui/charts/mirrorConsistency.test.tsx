// The one test that exists to fail when the two mirrored charts drift apart.
//
// UpDownSparkline (a fleet row's traffic cell) and Overlay's mirrored branch
// (the host page's Traffic panel) draw the same reading -- rx above the
// midline, tx below it -- through the same mirrorPaths() geometry,
// and an operator scans one and then the other. They HAVE drifted before: the
// sparkline sat at a fully opaque fill with no edge while the panel had a
// dimmed fill and a solid one, so the same fact about the same host was two
// different pictures. Reconciling them put the weights in size.ts, and this
// pins the property that motivated it -- not the specific numbers, which
// UpDownSparkline.test.tsx already pins, but the fact that the two agree.
//
// It reads the rendered attributes of both rather than asserting that both
// files import the same constant: an import is not what the operator sees,
// and a component can always go back to hardcoding one.
import { describe, expect, it } from "vitest";
import { render } from "@testing-library/react";
import { Overlay } from "./Overlay";
import { UpDownSparkline } from "./UpDownSparkline";

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
  it("draws the traffic cell and the throughput panel as the same mark", () => {
    // Given the same rx/tx pair drawn by both mirrored charts, at the sizes
    // their real call sites use (hostColumns.tsx's 170x32 fleet cell and
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
});
