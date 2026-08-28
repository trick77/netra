import { describe, expect, it } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Event } from "../../../lib/api";
import { Events, eventSeverity } from "./Events";

function event(over: Partial<Event> = {}): Event {
  return {
    id: "e:1",
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
            id: "e:2",
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
          event({ id: "e:1", subject: "older", ts: "2026-08-01T00:00:00Z" }),
          event({ id: "e:2", subject: "newer", ts: "2026-08-09T00:00:00Z" }),
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

/**
 * Clicks every column header of the rendered table, asserting each one is a
 * control and that no click loses a row.
 *
 * Every header, not a sample: the point of these tables is that all of their
 * columns sort now, and a per-column accessor that throws on a null or reads
 * the wrong field only shows itself when that column is the one clicked.
 */
async function sortByEveryColumn() {
  const before = screen.getAllByRole("row").length;

  for (const cell of screen.getAllByRole("columnheader")) {
    const control = within(cell).getByRole("button");
    await userEvent.click(control);
    expect(screen.getAllByRole("row")).toHaveLength(before);
  }
}

// The events log shipped with no sortable column at all -- five columns of
// timestamps, categories and words, and the only order available was the
// newest-first this tab imposes on arrival.
describe("Events sorting", () => {
  const rows = [
    event({ id: "e:1", ts: "2026-08-10T09:00:00Z", type: "package" }),
    event({
      id: "e:2",
      ts: "2026-08-10T10:00:00Z",
      type: "drive",
      subject: "sda",
      detail: { severity: "critical" },
    }),
    event({
      id: "e:3",
      ts: "2026-08-10T08:00:00Z",
      type: "unit",
      subject: "cron.service",
      detail: { severity: "warning" },
    }),
  ];

  /** The Subject cell of every body row, in order. */
  function subjects(): string[] {
    const [, ...body] = screen.getAllByRole("row");
    return body.map((row) => within(row).getAllByRole("cell")[3]!.textContent!);
  }

  function header(name: RegExp) {
    return within(screen.getByRole("columnheader", { name })).getByRole(
      "button",
    );
  }

  it("sorts on every column", async () => {
    render(<Events events={rows} />);

    await sortByEveryColumn();
  });

  // The tab lands newest-first; one click on When gives oldest-first, which
  // is what a reader chasing a cause is after.
  it("orders When oldest-first on the first click", async () => {
    render(<Events events={rows} />);
    await userEvent.click(header(/when/i));

    expect(subjects()).toEqual(["cron.service", "openssl", "sda"]);
  });

  // By RANK, not alphabetically: "critical" collates above "warning", so a
  // string sort is right in one direction and backwards in the other -- and
  // an event the collector said nothing about is not the calmest on the page,
  // so it sorts last either way.
  it("orders Severity by rank and leaves the unstated ones last", async () => {
    render(<Events events={rows} />);
    await userEvent.click(header(/severity/i));

    expect(subjects()).toEqual(["cron.service", "sda", "openssl"]);
  });
});
