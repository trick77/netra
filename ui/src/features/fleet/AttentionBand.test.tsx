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
});

describe("AttentionBand", () => {
  it("renders nothing when there is nothing wrong", () => {
    const { container } = render(<AttentionBand conditions={[]} />);
    expect(container).toBeEmptyDOMElement();
  });

  it("collapses a host past two conditions but still counts them all", () => {
    render(<AttentionBand conditions={fourOnOneHost()} />);
    expect(screen.getByText(/\+3 more/)).toBeInTheDocument();
    expect(screen.getByText(/4 on 1 host/)).toBeInTheDocument();
  });

  it("counts both conditions and hosts across multiple hosts", () => {
    render(
      <AttentionBand conditions={[...twoWarnings("a"), ...oneCritical("b")]} />,
    );
    expect(screen.getByText(/3 on 2 hosts/)).toBeInTheDocument();
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

    // Only the worst condition shows at the top level. The other three sit
    // inside the closed <details>: present in the DOM, since grouping is
    // presentation and never suppression, but not visible and not reachable
    // by a screen reader until the disclosure is opened.
    expect(screen.getByText("disk full")).toBeVisible();
    expect(screen.getByText("service down")).not.toBeVisible();
  });

  // The rows must be inside the element the summary controls. Rendered as
  // siblings of it, the summary announced itself expanded while the thing it
  // disclosed was empty, and the revealed rows had no programmatic
  // relationship to the control that revealed them.
  it("discloses the hidden conditions from inside the details element", () => {
    render(<AttentionBand conditions={fourOnOneHost()} />);

    const details = document.querySelector("details")!;
    expect(details.querySelectorAll(".attn-sub")).toHaveLength(3);

    details.setAttribute("open", "");
    expect(screen.getByText("service down")).toBeVisible();
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
