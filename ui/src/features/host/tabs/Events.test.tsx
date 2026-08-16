import { describe, expect, it } from "vitest";
import { render, screen, within } from "@testing-library/react";
import type { Event } from "../../../lib/api";
import { Events, eventSeverity } from "./Events";

function event(over: Partial<Event> = {}): Event {
  return {
    id: 1,
    host_id: 7,
    hostname: "kessel",
    ts: "2026-08-10T00:00:00Z",
    type: "package",
    subject: "openssl",
    detail: {},
    ...over,
  };
}

describe("eventSeverity", () => {
  it("has no opinion when the emitting collector expressed none", () => {
    // The events table has no severity column: internal/hub/store's schema
    // stores type, subject and the collector's own detail JSON, nothing
    // more. Inventing one from the type would colour a package upgrade.
    expect(eventSeverity(event())).toBeNull();
    expect(eventSeverity(event({ detail: null }))).toBeNull();
    expect(eventSeverity(event({ detail: { severity: "spicy" } }))).toBeNull();
  });

  it("passes through a severity the collector did state", () => {
    expect(eventSeverity(event({ detail: { severity: "critical" } }))).toBe(
      "critical",
    );
  });
});

describe("Events", () => {
  it("renders a package event as a neutral chip with no status tint", () => {
    render(<Events events={[event()]} />);
    const row = screen.getByRole("row", { name: /openssl/ });
    expect(within(row).getByText("package")).toBeInTheDocument();
    expect(row.querySelector(".badge.st-crit, .badge.st-warn")).toBeNull();
  });

  it("gives a stated severity both a tint and the word", () => {
    render(
      <Events
        events={[
          event({
            id: 2,
            type: "mdraid",
            subject: "md0",
            detail: { severity: "critical", state: "degraded" },
          }),
        ]}
      />,
    );
    const row = screen.getByRole("row", { name: /md0/ });
    expect(within(row).getByText("critical")).toBeInTheDocument();
    expect(row.querySelector(".badge.st-crit")).not.toBeNull();
  });

  it("shows the newest first", () => {
    render(
      <Events
        events={[
          event({ id: 1, subject: "older", ts: "2026-08-01T00:00:00Z" }),
          event({ id: 2, subject: "newer", ts: "2026-08-09T00:00:00Z" }),
        ]}
      />,
    );
    const rows = screen.getAllByRole("row");
    expect(rows[1]).toHaveTextContent("newer");
  });

  it("says the host has been quiet rather than showing an empty table", () => {
    render(<Events events={[]} />);
    expect(screen.queryByRole("table")).toBeNull();
    expect(
      screen.getByRole("heading", { name: /nothing/i }),
    ).toBeInTheDocument();
  });
});
