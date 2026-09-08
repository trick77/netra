// Events carry no severity on the wire -- the events table is (host_id, ts,
// type, subject, detail) and nothing more -- so the fixtures below are real
// emitter shapes (mdraid's detail JSON comes from
// agent/collector/mdraid.go, matching collector/testdata/mdraid/degraded) and
// severity is asserted as something this page derives from them.
import { describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Event } from "../../lib/api";
import { EVENT_LIMITS, RANGES } from "../../lib/range";
import {
  DEFAULT_FILTERS,
  EventsPage,
  EVENT_RANGES,
  applyFilters,
  filtersFromQuery,
  filtersToQuery,
  severityOf,
  type EventFilters,
} from "./EventsPage";
import { KNOWN_EVENT_TYPES } from "./message";
import { ABSENT } from "../../lib/format";

const NOW = new Date("2026-08-10T14:00:00Z");

function event(overrides: Partial<Event> = {}): Event {
  return {
    id: "e:1",
    host_id: 3,
    hostname: "web-01",
    ts: "2026-08-10T13:59:00Z",
    type: "package",
    subject: "nginx",
    detail: { from: "1.26", to: "1.27" },
    ...overrides,
  };
}

const DEGRADED = event({
  id: "e:2",
  ts: "2026-08-10T13:50:00Z",
  type: "mdraid",
  subject: "md0",
  // array_state is "clean" even here: the kernel reports consistency, not
  // how many disks are left. See mdraidSeverity in ./message.
  detail: {
    state: "clean",
    level: "raid10",
    raid_disks: 4,
    degraded: 1,
    sync_action: "idle",
  },
});

const RECOVERING = event({
  id: "e:3",
  ts: "2026-08-10T13:20:00Z",
  host_id: 4,
  hostname: "db-01",
  type: "mdraid",
  subject: "md0",
  detail: {
    state: "clean",
    level: "raid10",
    raid_disks: 4,
    degraded: 1,
    sync_action: "recover",
  },
});

/** The defaults with the severity floor lifted.
 *
 * The page opens at warning-and-above, which is a filter and is tested as one
 * below. Every OTHER test here is about something else -- rendering a chip,
 * ordering rows, handing a change back -- and its fixtures are ordinary info
 * events, so starting them behind the floor would have them assert against an
 * empty table for the wrong reason. */
const EVERY_SEVERITY: EventFilters = { ...DEFAULT_FILTERS, severity: "" };

const HOSTS = [
  { id: 3, hostname: "web-01" },
  { id: 4, hostname: "db-01" },
];

function renderPage(
  overrides: {
    events?: Event[];
    filters?: Partial<EventFilters>;
    truncated?: boolean;
  } = {},
) {
  const onFiltersChange = vi.fn();
  render(
    <EventsPage
      events={overrides.events ?? [event(), DEGRADED, RECOVERING]}
      hosts={HOSTS}
      truncated={overrides.truncated ?? false}
      filters={{ ...EVERY_SEVERITY, ...overrides.filters }}
      onFiltersChange={onFiltersChange}
      now={NOW}
    />,
  );
  return { onFiltersChange };
}

describe("severityOf", () => {
  it("reads a degraded array as critical", () => {
    expect(severityOf(DEGRADED)).toBe("critical");
  });

  it("reads a recovering array as a warning", () => {
    expect(severityOf(RECOVERING)).toBe("warning");
  });

  // A package upgrade is not a colour-coded emergency (spec 6).
  it("leaves a package upgrade as info", () => {
    expect(severityOf(event())).toBe("info");
  });

  it("does not trip over a detail that is not an object", () => {
    expect(severityOf(event({ detail: "degraded" }))).toBe("info");
    expect(severityOf(event({ detail: null }))).toBe("info");
  });
});

describe("filters in the URL", () => {
  const busy: EventFilters = {
    search: "nginx",
    host: "3",
    type: "package",
    severity: "warning",
    range: "7d",
  };

  it("round-trips through a query string", () => {
    expect(filtersFromQuery(filtersToQuery(busy))).toEqual(busy);
  });

  // A default carries no information, and a URL that spells out every
  // default is unshareable noise. The RANGE is the exception: it is a
  // remembered preference now, so an absent range means "whatever the
  // reader's browser remembers" rather than 24h -- and a sent link exists
  // to override exactly that.
  it("writes nothing but the range and the severity for the defaults", () => {
    expect(filtersToQuery(DEFAULT_FILTERS)).toBe("range=24h&severity=warning");
    expect(filtersFromQuery("")).toEqual(DEFAULT_FILTERS);
  });

  // "" is every severity, and it is a real choice rather than the absence of
  // one. Since the DEFAULT became `warning`, omitting the key would resolve
  // back to warning -- so a reader looking at everything could not send anyone
  // a link to what they were looking at.
  it("round-trips a reader who cleared the severity floor", () => {
    const all: EventFilters = { ...DEFAULT_FILTERS, severity: "" };
    expect(filtersToQuery(all)).toContain("severity=");
    expect(filtersFromQuery(filtersToQuery(all))).toEqual(all);
  });

  // A link written before `info` left the dropdown must not open a control
  // rendered blank: a <select> whose value matches no <option> shows nothing
  // while behaving as "all severities".
  it("folds the lowest severity onto the option that means the same thing", () => {
    expect(filtersFromQuery("severity=info").severity).toBe("");
  });

  it("ignores a range the page does not have", () => {
    expect(filtersFromQuery("range=99y").range).toBe(DEFAULT_FILTERS.range);
  });

  // The fallback is what a URL carrying no range resolves to -- the screen
  // passes the remembered preference, already clamped to this page's set.
  it("takes the given fallback when the URL names no range", () => {
    expect(filtersFromQuery("", "7d").range).toBe("7d");
    expect(filtersFromQuery("range=1h", "7d").range).toBe("1h");
    expect(filtersFromQuery("range=99y", "7d").range).toBe("7d");
  });
});

describe("applyFilters", () => {
  const events = [event(), DEGRADED, RECOVERING];

  it("matches the search against subject, type and host", () => {
    expect(
      applyFilters(events, { ...EVERY_SEVERITY, search: "db-01" }),
    ).toEqual([RECOVERING]);
    expect(
      applyFilters(events, { ...EVERY_SEVERITY, search: "NGINX" }),
    ).toHaveLength(1);
  });

  it("filters by host, type and derived severity", () => {
    expect(applyFilters(events, { ...EVERY_SEVERITY, host: "4" })).toEqual([
      RECOVERING,
    ]);
    expect(
      applyFilters(events, { ...EVERY_SEVERITY, type: "mdraid" }),
    ).toHaveLength(2);
    expect(
      applyFilters(events, { ...EVERY_SEVERITY, severity: "critical" }),
    ).toEqual([DEGRADED]);
  });

  // The severity filter is a THRESHOLD, not an equality, and this is the test
  // that makes the default safe: selecting `warning` while a critical event is
  // in the list has to keep the critical one. As an equality it would hide
  // exactly the rows the filter looks like it is for.
  // `info` is a valid value -- an old link may carry it -- and as a threshold
  // it selects everything, which is why it is not offered in the dropdown.
  it("treats the lowest severity as no filter at all", () => {
    expect(
      applyFilters(events, { ...EVERY_SEVERITY, severity: "info" }),
    ).toHaveLength(3);
  });

  it("treats severity as a floor, not an exact match", () => {
    expect(
      applyFilters(events, { ...EVERY_SEVERITY, severity: "warning" }),
    ).toEqual([DEGRADED, RECOVERING]);
  });

  // What the page does on open, with nothing selected.
  it("hides info by default and keeps everything worse", () => {
    expect(applyFilters(events, DEFAULT_FILTERS)).toEqual([
      DEGRADED,
      RECOVERING,
    ]);
    expect(applyFilters(events, EVERY_SEVERITY)).toHaveLength(3);
  });

  // The range is a server-side window (api.ts's since/until), not a
  // predicate over rows already fetched.
  it("leaves the range alone", () => {
    expect(
      applyFilters(events, { ...EVERY_SEVERITY, range: "1h" }),
    ).toHaveLength(3);
  });
});

describe("EventsPage", () => {
  it("renders the type as a chip that takes no status tint", () => {
    renderPage({ events: [event()] });

    // Scoped to the row: "package" is also a filter option.
    const chip = within(screen.getByRole("listitem")).getByText("package");
    expect(chip).toHaveClass("badge");
    expect(chip.className).not.toMatch(/st-/);
  });

  it("gives critical and warning a tinted chip, and info the word alone", () => {
    renderPage();

    const rows = screen.getAllByRole("listitem").map((row) => within(row));

    const critical = rows[1]!.getByText("critical");
    expect(critical.closest(".badge")).toHaveClass("st-crit");
    expect(rows[2]!.getByText("warning").closest(".badge")).toHaveClass(
      "st-warn",
    );

    const info = rows[0]!.getByText("info");
    expect(info.closest(".badge")).toBeNull();
  });

  it("renders an event with nothing to say as the absent marker, not a blank", () => {
    // Subjectless AND detailless: an event about the host as a whole that
    // carried no payload. A subjectless event that DOES have detail is not
    // blank any more -- see the message tests -- so this is the only case
    // left where the marker is the honest rendering.
    renderPage({ events: [event({ subject: null, detail: {} })] });

    expect(screen.getByText(ABSENT)).toBeInTheDocument();
  });

  it("says what happened rather than naming the subject and stopping", () => {
    // The whole point of the wider log: detail was always fetched and never
    // shown, so a package row read "package · web-01 · nginx" and the
    // version change it existed to report was invisible.
    renderPage({
      events: [
        event({
          type: "package",
          subject: "curl",
          detail: {
            action: "upgrade",
            from_version: "8.5.0",
            to_version: "8.5.0-2",
          },
        }),
      ],
    });

    expect(
      screen.getByText("curl upgraded 8.5.0 → 8.5.0-2"),
    ).toBeInTheDocument();
  });

  it("folds the rest of a big apt run into a link to the host's packages", () => {
    // The hub keeps three rows of a run and counts the rest, so one
    // dist-upgrade cannot fill the page. The row still says what ITS package
    // did; the fold is beside it.
    renderPage({
      events: [
        event({
          type: "package",
          subject: "curl",
          detail: {
            action: "upgrade",
            from_version: "8.5.0",
            to_version: "8.5.0-2",
            more: 397,
            run_size: 400,
          },
        }),
      ],
    });

    expect(
      screen.getByText("curl upgraded 8.5.0 → 8.5.0-2"),
    ).toBeInTheDocument();
    const fold = screen.getByRole("link", {
      name: "+397 more packages changed",
    });
    expect(fold).toHaveAttribute("href", "/hosts/3/packages");
    expect(fold).toHaveAttribute("title", "400 packages changed in this run");
  });

  it("shows no fold on a run the hub sent in full", () => {
    renderPage({
      events: [
        event({
          type: "package",
          subject: "curl",
          detail: { action: "upgrade", to_version: "8.5.0-2" },
        }),
      ],
    });

    expect(screen.queryByText(/more packages changed/)).toBeNull();
  });

  it("links each row to its host", () => {
    renderPage({ events: [event()] });

    expect(screen.getByRole("link", { name: "web-01" })).toHaveAttribute(
      "href",
      "/hosts/3",
    );
  });

  it("hands every filter change back whole, never as a partial", async () => {
    const { onFiltersChange } = renderPage({ filters: { host: "3" } });

    await userEvent.type(screen.getByLabelText("Filter events"), "n");

    expect(onFiltersChange).toHaveBeenCalledWith({
      ...EVERY_SEVERITY,
      host: "3",
      search: "n",
    });
  });

  it("hands back a host, a type, a severity and a range", async () => {
    const { onFiltersChange } = renderPage();

    await userEvent.selectOptions(screen.getByLabelText("Host"), "4");
    await userEvent.selectOptions(screen.getByLabelText("Type"), "mdraid");
    await userEvent.selectOptions(screen.getByLabelText("Severity"), "warning");
    await userEvent.click(screen.getByRole("button", { name: "7d" }));

    expect(onFiltersChange.mock.calls.map(([f]) => f)).toEqual([
      { ...EVERY_SEVERITY, host: "4" },
      { ...EVERY_SEVERITY, type: "mdraid" },
      { ...EVERY_SEVERITY, severity: "warning" },
      { ...EVERY_SEVERITY, range: "7d" },
    ]);
  });

  // Every filter but the range runs over rows already fetched, so a window
  // the server truncated can hide a critical event behind a severity floor
  // that never saw it. Saying "nothing matches these filters" would be a
  // false statement about what happened.
  it("says the window was truncated rather than blaming the filters", () => {
    renderPage({
      events: [],
      filters: { severity: "critical" },
      truncated: true,
    });

    expect(screen.getByText(/cut off before any filter ran/i)).toBeTruthy();
  });

  it("blames the filters when nothing was truncated", () => {
    renderPage({ events: [], filters: { severity: "critical" } });

    expect(screen.getByText(/matches these filters/i)).toBeTruthy();
  });

  // Two options that select identical rows is one option too many: as a
  // threshold, "info and worse" IS "all severities".
  it("offers no severity that means the same as no filter", () => {
    renderPage();

    const options = [
      ...screen.getByLabelText("Severity").querySelectorAll("option"),
    ];
    expect(options.map((o) => o.value)).toEqual(["", "critical", "warning"]);
  });

  // The known types are always on offer, because filtering by type narrows
  // the response: a list built from the response alone would empty itself
  // down to the one type already selected, with no way back.
  it("offers every type the hub emits, not only the ones in this response", () => {
    renderPage();

    const options = [
      ...screen.getByLabelText("Type").querySelectorAll("option"),
    ];
    expect(options.map((o) => o.value)).toEqual([
      "",
      ...[...KNOWN_EVENT_TYPES].sort(),
    ]);
  });

  it("keeps the other types on offer once one is selected", () => {
    // The regression this guards: with the response filtered to packages,
    // deriving the list from it alone leaves "package" as the only option.
    renderPage({
      events: [event({ type: "package" })],
      filters: { type: "package" },
    });

    const options = [
      ...screen.getByLabelText("Type").querySelectorAll("option"),
    ];
    expect(options.map((o) => o.value)).toContain("mdraid");
    expect(options.map((o) => o.value)).toContain("unit");
  });

  // internal/hub/read/events.go orders by ts DESC, id DESC, so the newest
  // row is already first: this page must pass that order through, not
  // impose one of its own.
  it("keeps the log newest-first, as the hub returned it", () => {
    renderPage();

    const times = screen
      .getAllByRole("listitem")
      .map((row) => row.querySelector("time")!.getAttribute("dateTime"));

    expect(times).toEqual([
      "2026-08-10T13:59:00Z",
      "2026-08-10T13:50:00Z",
      "2026-08-10T13:20:00Z",
    ]);
  });

  it("shows an empty state when the filters match nothing", () => {
    renderPage({ filters: { search: "nothing matches this" } });

    expect(screen.queryByRole("list")).toBeNull();
    expect(
      screen.getByRole("heading", { name: /no events/i }),
    ).toBeInTheDocument();
  });
});

describe("EVENT_LIMITS", () => {
  // A flat limit makes the wide buttons a lie: events accumulate with the
  // window, so the same 500 rows over 30 days is the newest few days and a
  // silent cut everywhere else.
  it("asks for more rows as the window widens", () => {
    const offered = EVENT_RANGES.map((r) => EVENT_LIMITS[r.value]);
    for (let i = 1; i < offered.length; i++) {
      expect(offered[i]!).toBeGreaterThanOrEqual(offered[i - 1]!);
    }
    expect(EVENT_LIMITS["30d"]).toBeGreaterThan(EVENT_LIMITS["24h"]);
  });

  // maxEventLimit in internal/hub/read/events.go. The hub clamps rather than
  // rejects, so asking for more is not an error -- it is a lie about what
  // came back.
  it("never asks for more than the hub will give", () => {
    for (const limit of Object.values(EVENT_LIMITS)) {
      expect(limit).toBeLessThanOrEqual(5000);
    }
  });

  it("covers every range, not only the ones this page offers", () => {
    for (const range of RANGES) {
      expect(EVENT_LIMITS[range]).toBeGreaterThan(0);
    }
  });
});
