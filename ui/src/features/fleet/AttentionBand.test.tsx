import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { AttentionBand, groupByHost, type Condition } from "./AttentionBand";

function condition(overrides: Partial<Condition> = {}): Condition {
  return {
    hostId: "h1",
    hostname: "host-1",
    severity: "warning",
    what: "disk 92% full",
    since: "2026-08-10T10:00:00Z",
    tab: null,
    ...overrides,
  };
}

function oneCritical(hostId: string): Condition[] {
  return [
    condition({
      hostId,
      hostname: hostId,
      severity: "critical",
      what: "unreachable",
    }),
  ];
}

function twoWarnings(hostId: string): Condition[] {
  return [
    condition({ hostId, hostname: hostId, severity: "warning", what: "a" }),
    condition({ hostId, hostname: hostId, severity: "warning", what: "b" }),
  ];
}

function fourOnOneHost(): Condition[] {
  return [
    condition({
      hostId: "h1",
      hostname: "host-1",
      severity: "critical",
      what: "disk full",
    }),
    condition({
      hostId: "h1",
      hostname: "host-1",
      severity: "serious",
      what: "service down",
    }),
    condition({
      hostId: "h1",
      hostname: "host-1",
      severity: "warning",
      what: "load high",
    }),
    condition({
      hostId: "h1",
      hostname: "host-1",
      severity: "warning",
      what: "container restarted",
    }),
  ];
}

describe("groupByHost", () => {
  it("orders hosts by their worst condition, not by how many they have", () => {
    const groups = groupByHost([...twoWarnings("a"), ...oneCritical("b")]);
    expect(groups[0].hostname).toBe("b");
    expect(groups[1].hostname).toBe("a");
  });

  it("keeps every condition in the data even when the display collapses them", () => {
    const groups = groupByHost(fourOnOneHost());
    expect(groups).toHaveLength(1);
    expect(groups[0].conditions).toHaveLength(4);
  });

  it("picks the worst condition of a host as its sort key, not the first one listed", () => {
    const conditions = [
      condition({ hostId: "a", hostname: "a", severity: "warning" }),
      condition({ hostId: "a", hostname: "a", severity: "critical" }),
    ];
    const groups = groupByHost(conditions);
    expect(groups[0].worst.severity).toBe("critical");
  });

  // Nothing is promoted out of the list any more, so the list itself has to
  // put the worst first -- otherwise a host's critical can be the condition
  // that falls behind the fold.
  it("orders a host's own conditions worst first", () => {
    const groups = groupByHost([
      condition({ hostId: "a", hostname: "a", severity: "warning", what: "w" }),
      condition({
        hostId: "a",
        hostname: "a",
        severity: "critical",
        what: "c",
      }),
      condition({ hostId: "a", hostname: "a", severity: "serious", what: "s" }),
    ]);
    expect(groups[0].conditions.map((c) => c.severity)).toEqual([
      "critical",
      "serious",
      "warning",
    ]);
  });

  // Equal severities keep the order hostConditions() wrote them in, which is
  // why reporting still leads: it qualifies every figure under it.
  it("keeps the written order inside one severity", () => {
    const groups = groupByHost([
      condition({
        hostId: "a",
        hostname: "a",
        severity: "warning",
        what: "first",
      }),
      condition({
        hostId: "a",
        hostname: "a",
        severity: "warning",
        what: "second",
      }),
    ]);
    expect(groups[0].conditions.map((c) => c.what)).toEqual([
      "first",
      "second",
    ]);
  });
});

describe("AttentionBand", () => {
  it("renders nothing when there is nothing wrong", () => {
    const { container } = render(<AttentionBand conditions={[]} />);
    expect(container).toBeEmptyDOMElement();
  });

  it("collapses a host past three conditions but still counts them all", () => {
    render(<AttentionBand conditions={fourOnOneHost()} />);
    expect(screen.getByText(/\+1 more/)).toBeInTheDocument();
    expect(screen.getByText("4 problems")).toBeInTheDocument();
  });

  it("counts one host's problems in its own header", () => {
    render(
      <AttentionBand conditions={[...twoWarnings("a"), ...oneCritical("b")]} />,
    );
    expect(screen.getByText("2 problems")).toBeInTheDocument();
    expect(screen.getByText("1 problem")).toBeInTheDocument();
  });

  // The band's own "{n} on {m} hosts" header is gone: it restated the line
  // FleetPage prints directly above it. Nothing inside the card may grow a
  // second copy of that sentence.
  it("has no header of its own", () => {
    const { container } = render(
      <AttentionBand conditions={[...twoWarnings("a"), ...oneCritical("b")]} />,
    );
    expect(container.querySelector(".attn > header")).toBeNull();
  });

  // That heading was also the landmark a screen reader navigated to, so the
  // band has to name itself now that it is gone.
  it("names itself as a landmark", () => {
    render(<AttentionBand conditions={oneCritical("a")} />);
    expect(
      screen.getByRole("region", { name: /needs attention/i }),
    ).toBeInTheDocument();
  });

  it("orders rows by worst severity, not by condition count", () => {
    render(
      <AttentionBand conditions={[...twoWarnings("a"), ...oneCritical("b")]} />,
    );
    const whos = screen.getAllByText(/^(a|b)$/);
    expect(whos[0]).toHaveTextContent("b");
  });

  it("keeps a host's other conditions behind the disclosure rather than flat in the band", () => {
    render(<AttentionBand conditions={fourOnOneHost()} />);

    // The worst three show at the top level. Anything past them sits inside
    // the closed <details>: present in the DOM, since grouping is
    // presentation and never suppression, but not visible and not reachable
    // by a screen reader until the disclosure is opened.
    expect(screen.getByText("disk full")).toBeVisible();
    expect(screen.getByText("service down")).toBeVisible();
    expect(screen.getByText("container restarted")).not.toBeVisible();
  });

  // Every condition is a row of equal weight under its host now. The old band
  // promoted the worst one into the host's own row and drew the rest as
  // indented sub-rows, which lined up with no column above them.
  it("draws every condition as a row of the same kind", () => {
    const { container } = render(
      <AttentionBand conditions={twoWarnings("a")} />,
    );
    expect(container.querySelectorAll(".attn-cond")).toHaveLength(2);
    expect(container.querySelector(".attn-sub")).toBeNull();
  });

  it("links a condition to the tab that answers it", () => {
    render(
      <AttentionBand
        conditions={[
          condition({
            hostId: "h9",
            hostname: "h9",
            what: "1 failed unit — docker.service",
            tab: "units",
          }),
        ]}
      />,
    );
    expect(screen.getByRole("link", { name: /units/ })).toHaveAttribute(
      "href",
      "/hosts/h9/units",
    );
  });

  // A link that lands somewhere unhelpful teaches people to stop following
  // links, so a condition with no page to open carries no link at all.
  it("leaves a condition with no answering tab unlinked", () => {
    render(
      <AttentionBand
        conditions={[
          condition({
            hostId: "h9",
            hostname: "h9",
            what: "stopped reporting",
            tab: null,
          }),
        ]}
      />,
    );
    expect(screen.getAllByRole("link")).toHaveLength(1); // the hostname only
  });

  // The rows must be inside the element the summary controls. Rendered as
  // siblings of it, the summary announced itself expanded while the thing it
  // disclosed was empty, and the revealed rows had no programmatic
  // relationship to the control that revealed them.
  it("discloses the hidden conditions from inside the details element", () => {
    render(<AttentionBand conditions={fourOnOneHost()} />);

    const details = document.querySelector("details")!;
    expect(details.querySelectorAll(".attn-cond")).toHaveLength(1);

    details.setAttribute("open", "");
    expect(screen.getByText("container restarted")).toBeVisible();
  });

  it("has no dismiss or acknowledge control", () => {
    render(<AttentionBand conditions={oneCritical("a")} />);
    expect(
      screen.queryByRole("button", { name: /dismiss|acknowledge/i }),
    ).toBeNull();
  });

  it("links each row to its host", () => {
    render(<AttentionBand conditions={oneCritical("h1")} />);
    const link = screen.getByRole("link", { name: /h1/i });
    expect(link).toHaveAttribute("href", expect.stringContaining("h1"));
  });

  it("renders an action slot per row when provided", () => {
    render(
      <AttentionBand
        conditions={oneCritical("h1")}
        renderAction={(group) => <button>Explain {group.hostname}</button>}
      />,
    );
    expect(
      screen.getByRole("button", { name: "Explain h1" }),
    ).toBeInTheDocument();
  });
});
