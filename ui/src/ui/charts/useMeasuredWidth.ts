// The width a panel's chart is actually allowed to be.
//
// Charts carry explicit width/height attributes and a viewBox, and
// `svg.spark { max-width: 100% }` in index.css lets them shrink to fit. What
// it cannot do is GROW them: a chart drawn at 260 in a 500px card left 240px
// of empty surface -- the wasted right third of every panel on the host page
// -- because a card stretches to its column while the drawing width stayed a
// constant.
//
// Scaling the SVG up instead (width: 100%) would work and would take the axis
// text with it, since the labels are drawn INSIDE the image: a panel in a wide
// card would carry 15px axis labels beside a 13px card title. So the width is
// MEASURED and the chart is drawn at it, which leaves every font, stroke and
// the plot.ts label-margin table at the size they were tuned for and simply
// gives the plot more room.
import { useCallback, useEffect, useState } from "react";

/**
 * The content width of the element the returned ref is put on, or `fallback`
 * until it is known.
 *
 * `fallback` covers three real cases and they all want the same answer -- the
 * width the chart was drawn at before this existed: the first paint, a
 * detached or zero-width container, and jsdom, which implements no layout at
 * all and reports 0 for everything.
 */
export function useMeasuredWidth<T extends HTMLElement>(
  fallback: number,
): {
  ref: (node: T | null) => void;
  width: number;
} {
  // The measured element is STATE, not a useRef, and the observer is set up
  // when it arrives rather than once on mount. A panel does not necessarily
  // render its chart on the first pass: ChartPanel returns the "not
  // collected" card while a family is still being fetched, and that card has
  // no plot in it at all. An effect with an empty dependency list runs
  // against a ref that is still null there, never runs again, and the panel
  // draws at the fallback width for the rest of its life -- which is exactly
  // what the Traffic, Processor and Memory cards did while CPU time
  // breakdown, whose data was already in hand, measured correctly.
  const [node, setNode] = useState<T | null>(null);
  const [measured, setMeasured] = useState<number | null>(null);
  const ref = useCallback((next: T | null) => setNode(next), []);

  useEffect(() => {
    // No ResizeObserver is not a crash: a test environment or an old browser
    // keeps the fallback width, which is a smaller chart and not a broken
    // one.
    if (node === null || typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver((entries) => {
      const box = entries[0]?.contentRect;
      if (box === undefined) return;
      // Rounded, and only when it CHANGES: a fractional width from a
      // percentage track would otherwise rewrite the viewBox on every
      // sub-pixel reflow, and each write redraws every path in the panel.
      const next = Math.round(box.width);
      setMeasured((prev) => (prev === next ? prev : next));
    });
    observer.observe(node);
    return () => observer.disconnect();
  }, [node]);

  return {
    ref,
    width: measured !== null && measured > 0 ? measured : fallback,
  };
}
