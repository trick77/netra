import { describe, expect, it } from "vitest";
import { render, screen, within } from "@testing-library/react";
import { AttentionBand } from "./AttentionBand";

const NOW = new Date("2026-08-10T14:00:00Z");

describe("AttentionBand", () => {
  // A permanently visible "all clear" box in the best position on the page is
  // a box people stop reading.
  it("renders nothing when nothing is wrong", () => {
    const { container } = render(<AttentionBand rows={[]} now={NOW} />);
    expect(container.querySelector(".attn")).toBeNull();
  });

  // The severity is a heading over its rows, said once. Three critical rows do
  // not need the word "critical" three times.
  it("heads each severity once, with its count", () => {
    render(
      <AttentionBand
        now={NOW}
        rows={[
          { severity: "critical", what: "one" },
          { severity: "critical", what: "two" },
          { severity: "warning", what: "three" },
        ]}
      />,
    );

    expect(screen.getByRole("heading", { name: "Critical 2" })).toBeVisible();
    expect(screen.getByRole("heading", { name: "Warning 1" })).toBeVisible();
  });

  // Worst first, whatever order the caller wrote them in.
  it("puts critical above warning", () => {
    render(
      <AttentionBand
        now={NOW}
        rows={[
          { severity: "warning", what: "later" },
          { severity: "critical", what: "first" },
        ]}
      />,
    );

    const headings = screen.getAllByRole("heading");
    expect(headings[0]!.textContent).toContain("Critical");
  });

  // A neutral row is not a thing to act on -- a paused container, a host
  // nobody can currently see -- and a band that listed it would be answering a
  // question nobody asked.
  it("draws nothing for a neutral row", () => {
    const { container } = render(
      <AttentionBand
        now={NOW}
        rows={[{ severity: "neutral", what: "paused" }]}
      />,
    );
    expect(container.querySelector(".attn")).toBeNull();
  });

  describe("the onset clause", () => {
    it("appends how long it has been true", () => {
      render(
        <AttentionBand
          now={NOW}
          rows={[
            {
              severity: "warning",
              what: "quiet",
              since: "2026-08-10T13:00:00Z",
            },
          ]}
        />,
      );
      expect(screen.getByText(/· since 1 h ago/)).toBeInTheDocument();
    });

    // A floor is a span, not a moment: the walk could not see past it.
    it("says over rather than since when the onset is only a floor", () => {
      render(
        <AttentionBand
          now={NOW}
          rows={[
            {
              severity: "warning",
              what: "quiet",
              since: "2026-08-03T14:00:00Z",
              sinceAtLeast: true,
            },
          ]}
        />,
      );
      expect(screen.getByText(/· over 7 d$/)).toBeInTheDocument();
    });

    // Several kinds have no memory of when they became true, and a plausible
    // instant would be worse than none.
    it("prints no clause at all without an onset", () => {
      const { container } = render(
        <AttentionBand now={NOW} rows={[{ severity: "warning", what: "x" }]} />,
      );
      expect(container.querySelector(".since")).toBeNull();
    });

    // The class the stylesheet was actually written for. It rendered as
    // `muted`, which has no rule anywhere, so the clause lost both its ink and
    // its tabular figures.
    it("marks the clause with the class index.css styles", () => {
      const { container } = render(
        <AttentionBand
          now={NOW}
          rows={[
            { severity: "warning", what: "x", since: "2026-08-10T13:00:00Z" },
          ]}
        />,
      );
      expect(container.querySelector(".attn-row .since")).not.toBeNull();
    });
  });

  describe("the cap", () => {
    const many = Array.from({ length: 9 }, (_, i) => ({
      severity: "warning" as const,
      what: `row ${i}`,
    }));

    it("draws every row when no cap is given", () => {
      render(<AttentionBand now={NOW} rows={many} />);
      expect(screen.getAllByRole("listitem")).toHaveLength(9);
    });

    // Unbounded, a fleet's band is the per-item wall that was deleted for
    // hosts: one lossy host can put a row here for every container it runs.
    it("stops at the cap and says how many were left out", () => {
      render(
        <AttentionBand
          now={NOW}
          rows={many}
          cap={5}
          overflow={(severity, hidden) => (
            <a href={`/?attn=${severity}`}>+ {hidden} more</a>
          )}
        />,
      );

      // Five rows plus the overflow line.
      expect(screen.getAllByRole("listitem")).toHaveLength(6);
      // The heading still counts them ALL: capping what is drawn must not
      // change what is reported.
      expect(screen.getByRole("heading", { name: "Warning 9" })).toBeVisible();
      // And the way out is a link, which is exactly what the band this
      // replaced was faulted for lacking.
      expect(
        within(screen.getByRole("list")).getByRole("link", {
          name: "+ 4 more",
        }),
      ).toHaveAttribute("href", "/?attn=warning");
    });

    it("draws no overflow line when nothing was left out", () => {
      render(
        <AttentionBand
          now={NOW}
          rows={many.slice(0, 3)}
          cap={5}
          overflow={() => <a href="/">more</a>}
        />,
      );
      expect(screen.queryByRole("link")).toBeNull();
    });
  });
});
