import { afterEach, describe, expect, it, vi } from "vitest";
import { render } from "@testing-library/react";
import { useMeasuredWidth } from "./useMeasuredWidth";

function Probe({ onWidth }: { onWidth: (w: number) => void }) {
  const { ref, width } = useMeasuredWidth<HTMLDivElement>(260);
  onWidth(width);
  return <div ref={ref} style={{ paddingLeft: "8px", paddingRight: "8px" }} />;
}

describe("useMeasuredWidth", () => {
  afterEach(() => vi.restoreAllMocks());

  // The observer reports a frame late; the first picture used to be drawn at
  // the fallback and redrawn at the real width, a jump on every tab switch.
  it("has the content width before the first paint, padding excluded", () => {
    vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockReturnValue({
      width: 516.4,
    } as DOMRect);
    const widths: number[] = [];
    render(<Probe onWidth={(w) => widths.push(w)} />);
    // Render passes before the layout effect may use the fallback; what
    // reaches the screen is the last one, committed before paint.
    expect(widths[widths.length - 1]).toBe(500);
  });

  it("keeps the fallback where nothing can be measured", () => {
    const widths: number[] = [];
    render(<Probe onWidth={(w) => widths.push(w)} />);
    expect(widths[widths.length - 1]).toBe(260);
  });
});
