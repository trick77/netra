import { describe, expect, it } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { MetricsResponse } from "../../../lib/api";
import {
  AgentGraphs,
  NetworkGraphs,
  StorageGraphs,
  SystemGraphs,
  timedEvents,
  type GraphsProps,
} from "./Graphs";
import {
  ALL_SPECS,
  groupedSlugs,
  NETWORK_GROUPS,
  UNGROUPED_SLUGS,
} from "../chartSpecs";

/**
 * Every tab that draws panels, at once.
 *
 * The Graphs tab is gone -- see GROUP_SLUGS in ../chartSpecs -- but these
 * tests are about how a PANEL behaves (gaps, ceilings, counters, absent
 * columns), not about which tab it landed on, and asserting that against
 * every panel in one render is what they have always done. The split itself
 * is asserted separately, below.
 */
function Graphs(props: GraphsProps) {
  return (
    <>
      <SystemGraphs {...props} />
      <AgentGraphs {...props} />
      <NetworkGraphs {...props} />
      <StorageGraphs {...props} />
    </>
  );
}

function response(
  over: Partial<MetricsResponse> & { family: string },
): MetricsResponse {
  return {
    tier: "raw",
    step_s: 60,
    window: { from: "2026-08-10T00:00:00Z", to: "2026-08-10T01:00:00Z" },
    requested_window: {
      from: "2026-08-10T00:00:00Z",
      to: "2026-08-10T01:00:00Z",
    },
    warnings: [],
    key_columns: [],
    columns: [],
    series: [],
    truncated: false,
    ...over,
  };
}

// The host_snmp family, carrying exactly the columns the three IP/ICMP panels
// draw. It is a SECOND host-level family, not more columns on "host": the
// counters live in their own table so host_samples' continuous aggregates
// never had to be dropped and recreated.
const fullHostSnmp = response({
  family: "host_snmp",
  columns: [
    "ip_in_receives_per_s",
    "ip_out_requests_per_s",
    "ip6_in_receives_per_s",
    "ip6_out_requests_per_s",
    "icmp_in_errors_per_s",
    "icmp_out_errors_per_s",
    "icmp_in_dest_unreachs_per_s",
    "icmp6_in_errors_per_s",
    "icmp_in_echos_per_s",
    "icmp_out_echo_reps_per_s",
    "icmp6_in_echos_per_s",
    "icmp6_out_echo_replies_per_s",
  ],
  series: [
    {
      key: {},
      points: [
        [
          1_786_320_000_000, 800, 700, 220, 190, 0.2, 0.1, 0.3, 0.1, 1.2, 1.1,
          0.5, 0.4,
        ],
        [
          1_786_320_060_000, 820, 710, 225, 195, 0.3, 0.2, 0.4, 0.2, 1.3, 1.2,
          0.6, 0.5,
        ],
      ],
    },
  ],
});

// A tier that carries uptime but none of the load columns -- the case that
// makes lib/metrics.ts's column() throw mid-render if it is called blind.
const sparseHost = response({
  family: "host",
  columns: ["uptime_s"],
  series: [
    {
      key: {},
      points: [
        [1_754_784_000_000, 3600],
        [1_754_784_060_000, 3660],
      ],
    },
  ],
});

// The tier carries load1/5/15 and the host reported nothing for them in this
// window: a real chart with a hole, not a not-collected panel.
const silentHost = response({
  family: "host",
  columns: ["uptime_s", "load1", "load5", "load15"],
  series: [
    {
      key: {},
      points: [
        [1_754_784_000_000, 3600, null, null, null],
        [1_754_784_060_000, 3660, null, null, null],
      ],
    },
  ],
});

const fullHost = response({
  family: "host",
  columns: ["uptime_s", "load1", "load5", "load15", "ctxt_per_s"],
  series: [
    {
      key: {},
      points: [
        [1_754_784_000_000, 3600, 0.5, 0.4, 0.3, 1200],
        [1_754_784_060_000, 3660, null, 0.4, 0.3, 1300],
      ],
    },
  ],
});

describe("Graphs", () => {
  it("groups the small multiples under named headings", () => {
    const { container } = render(<Graphs host={fullHost} />);
    // The ledger groups are heading rows inside one table, not h3s
    const ledger = container.querySelector(".ledger") as HTMLElement;
    expect(within(ledger).getByText("Kernel")).toBeInTheDocument();
    // The group's heading row, and here its uncollected spec's row too
    expect(
      within(ledger).getAllByText("Memory pressure").length,
    ).toBeGreaterThan(0);
    for (const group of [
      "Resources",
      "Hub",
      "Traffic",
      "TCP",
      "Protocol counters",
      "Filesystems",
      "Drives",
    ]) {
      // level 3, because a group heading and a panel title can be the same
      // word: "Traffic" is both the group and the panel inside it. The
      // groups are h3 (.grouphead) and the panels h4.
      expect(
        screen.getByRole("heading", { name: group, level: 3 }),
      ).toBeInTheDocument();
    }
  });

  // The split itself, since the shim above deliberately renders all three at
  // once. A panel drawn on two subject tabs, or on none, is the failure this
  // catches -- and "on none" is invisible without it: a spec can be defined,
  // exported and simply left out of every group.
  // UNGROUPED_SLUGS is added to the grouped side rather than subtracted from
  // ALL_SPECS, so a spec that stops being drawn on the Overview and is not
  // put on a tab still fails this -- the list is an allowance for panels that
  // ARE drawn somewhere, not an exemption from having a home.
  it("draws each panel on exactly one tab", () => {
    const slugs = groupedSlugs();
    expect(new Set(slugs).size).toBe(slugs.length);
    expect([...slugs, ...UNGROUPED_SLUGS].sort()).toEqual(
      [...ALL_SPECS.map((s) => s.slug)].sort(),
    );
  });

  // Traffic for the whole host, then which link carried it as rows, then the
  // TCP charts, then every counter as a ledger row. IP sits below UDP, where
  // it was asked to be.
  it("orders the Network groups traffic, interfaces, TCP, then counters", () => {
    expect(NETWORK_GROUPS.map((g) => [g.title, g.form])).toEqual([
      ["Traffic", "lead"],
      ["By interface", "pairs"],
      ["TCP", "lead"],
      ["Interface errors", "ledger"],
      ["TCP counters", "ledger"],
      ["UDP", "ledger"],
      ["IP", "ledger"],
      ["ICMP", "ledger"],
    ]);
  });

  // The summed traffic is the Network tab's lead chart and the Overview's,
  // with the per-interface pairs as rows under it -- never two "Traffic"
  // charts on one tab.
  it("leads the Network tab with the summed traffic, interfaces as rows", () => {
    const traffic = NETWORK_GROUPS.slice(0, 2).map((g) =>
      g.specs.map((s) => s.slug),
    );
    expect(traffic).toEqual([["host-traffic-total"], ["host-traffic"]]);
    expect(UNGROUPED_SLUGS).toEqual([]);
  });

  it("puts the network panels on Network and the disks on Storage", () => {
    render(<NetworkGraphs host={fullHost} />);
    expect(
      screen.getByRole("heading", { name: "TCP connections" }),
    ).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Kernel" })).toBeNull();
    expect(screen.queryByRole("heading", { name: "Disks" })).toBeNull();
  });

  // These three used to be hardcoded "not collected" panels: spec §11 listed
  // them as families with no data behind them anywhere in the schema. They
  // are collected now, so they must read as rows of figures.
  it("reads the IP and ICMP counters from the host_snmp family", () => {
    render(<NetworkGraphs host={fullHost} hostSnmp={fullHostSnmp} />);

    for (const title of [
      "IP statistics",
      "ICMP statistics",
      "ICMP informational",
    ]) {
      expect(
        screen.getAllByText(new RegExp(`^${title} · `)).length,
      ).toBeGreaterThan(0);
    }
  });

  // Without the family the three have nothing to draw, and that must read as
  // "this window has no samples" rather than as a row of zeroes -- absent is
  // not zero (spec §7.5) -- even folded away with the quiet rows.
  it("says the IP and ICMP counters were not read when host_snmp is absent", async () => {
    render(<NetworkGraphs host={fullHost} />);

    for (const button of screen.getAllByRole("button", { name: "show" }))
      await userEvent.click(button);
    for (const title of [
      "IP statistics",
      "ICMP statistics",
      "ICMP informational",
    ]) {
      const row = screen.getByText(title).closest("tr")!;
      expect(row).toHaveTextContent(
        "No data has been read for this family yet.",
      );
    }
  });

  // The failure this replaces: a panel whose columns this tier does not
  // carry drew an empty box under its title, which asserts that the host
  // reported nothing rather than that the resolution cannot answer.
  it("names the columns a tier is missing instead of drawing a blank panel", () => {
    render(<Graphs host={sparseHost} />);

    // This tier is missing columns of its own, and each of those panels says
    // which ones and that a shorter range brings them back. (It used to also
    // count the three permanent schema gaps; those are collected now.)
    expect(screen.getAllByText("Not collected").length).toBeGreaterThan(0);
    expect(
      screen.getAllByText(/not stored at the .* resolution/).length,
    ).toBeGreaterThan(0);
  });

  // An all-null series is NOT the same case: the tier carries the column and
  // the host reported nothing in this window. That draws a real chart with a
  // hole in it and an absent headline, never a not-collected panel, because
  // the two statements are about different things.
  it("draws a chart with a hole when the column exists and the host was silent", () => {
    render(<Graphs host={silentHost} />);

    expect(
      screen.getByRole("region", { name: /^Load chart/ }),
    ).toBeInTheDocument();
  });

  // One size per kind of view: every lead chart alike, every small multiple
  // alike, and the lead ones taller.
  it("draws the charts of a kind at one size, the lead ones taller", () => {
    const { container } = render(<Graphs host={fullHost} />);
    const heights = (selector: string) =>
      new Set(
        Array.from(container.querySelectorAll(`${selector} svg.spark`)).map(
          (svg) => Number(svg.getAttribute("height")),
        ),
      );
    const lead = heights(".sm.lead");
    const small = heights(".sm:not(.lead)");
    expect(lead.size).toBe(1);
    // A panel's own height is 112; small multiples, where drawn, keep it
    expect(small.size).toBeLessThanOrEqual(1);
    expect([...lead][0]).toBeGreaterThan(112);
  });

  // A tier that carries some of a panel's columns and not others still
  // draws: the point is that it must not throw, and must not silently drop
  // the panel either.
  it("survives a tier that does not carry a panel's columns", () => {
    render(<Graphs host={sparseHost} />);

    const panel = screen.getByRole("region", {
      name: /^Load(, not collected| chart)/,
    });
    expect(panel).toBeInTheDocument();
  });

  // Hub latency is NULL by design while the hub is unreachable -- both gauges
  // time a handshake that completed -- so the panel correctly goes blank
  // during the exact event worth seeing. This counter is what carries it.
  it("draws hub connect failures as the increase per bucket, not the running total", () => {
    const agent = response({
      family: "agent",
      window: { from: "2025-08-10T00:00:00Z", to: "2025-08-10T00:03:00Z" },
      requested_window: {
        from: "2025-08-10T00:00:00Z",
        to: "2025-08-10T00:03:00Z",
      },
      columns: ["hub_connect_failures_total"],
      series: [
        {
          key: {},
          // Cumulative since the agent started: 4000 by the first bucket,
          // then two more failures. The headline must read 2, not 4002 --
          // the total says how many failures the agent has ever had, which
          // is not what a chart of this window is asking.
          points: [
            [1_754_784_000_000, 4000],
            [1_754_784_060_000, 4001],
            [1_754_784_120_000, 4002],
          ],
        },
      ],
    });
    render(<Graphs host={fullHost} agent={agent} />);
    // A ledger row now: its Now is the last bucket's increase
    const row = screen.getByText("Hub connect failures").closest("tr")!;
    expect(within(row).getAllByRole("cell")[1]).toHaveTextContent(/^1$/);
    expect(row).not.toHaveTextContent("4002");
  });

  it("carries no range control of its own — the header's drives every panel", () => {
    render(<Graphs host={fullHost} />);
    expect(screen.queryByRole("group")).toBeNull();
  });

  // A notice is about the RESPONSE, not about any one chart. Repeated under
  // twenty panels it was twenty pieces of noise nobody reads -- spec §7.2
  // puts it on the range control, once.
  //
  // Truncation is the fixture because it is what is left: a clamped window
  // says nothing at all now, since the chart already shows where its data
  // begins and ends. Truncation does not show -- the series is incomplete
  // and looks whole -- so it is still worth a sentence.
  it("states a notice once for the tab, not once per panel", () => {
    const truncated = response({
      family: "host",
      columns: ["uptime_s"],
      truncated: true,
      warnings: [
        "the result reached the 200000-point limit and is truncated; narrow the window or ask for fewer columns",
      ],
      series: [{ key: {}, points: [[1_754_784_000_000, 3600]] }],
    });

    // ONE subject tab, not the all-tabs shim: the notice is per tab, and
    // three tabs rendered together correctly say it three times.
    render(<SystemGraphs host={truncated} />);

    expect(screen.getAllByText(/point limit/)).toHaveLength(1);
  });
});

// The Memory panel is scaled against mem_total, on the tab as on the page.
//
// It is the one panel here whose ceiling comes from the DATA rather than
// from a literal in its spec, and the panel used to pass `spec.max` alone --
// undefined for host-memory -- so ChartPanel fell back to the stack's own
// running total. A stack scaled to itself always touches the top, which
// draws every host as nearly out of memory whatever its headroom: the single
// reading this chart exists to avoid, and the reason the fleet cell and the
// chart page both refuse to draw it without a total.
describe("the Memory panel's ceiling", () => {
  // Inside the response's own window, unlike the fixtures above: these two
  // assertions are about VALUES, and a point outside the window is gridded
  // to a null that would drop every band before the scale is ever chosen.
  const at = Date.parse("2026-08-10T00:00:00Z");

  function memory(columns: string[], point: number[]): MetricsResponse {
    return response({
      family: "host",
      columns,
      series: [
        {
          key: {},
          points: [
            [at, ...point],
            [at + 60_000, ...point],
          ],
        },
      ],
    });
  }

  it("draws the mem_total rule the page draws", () => {
    render(
      <Graphs
        host={memory(
          ["mem_used", "mem_free", "mem_total"],
          [1_073_741_824, 3_221_225_472, 4_294_967_296],
        )}
      />,
    );

    const panel = screen.getByRole("region", { name: "Memory chart" });
    // The dashed rule IS the ceiling made visible: with no reference the
    // panel is auto-scaled, and nothing in the plot says what the top of the
    // stack is a fraction of.
    expect(panel.querySelector("[data-reference]")).not.toBeNull();
  });

  it("refuses to draw at all when the host reported no total", () => {
    render(<Graphs host={memory(["mem_used"], [1_073_741_824])} />);

    // Named, not drawn -- the same refusal ChartPage makes, so the panel and
    // the page it links to cannot disagree about this host.
    //
    // "No scale", never "Not collected": mem_used and mem_free are right
    // there, and only the total to read them against is missing. Calling
    // that "not collected" would send a reader hunting a broken collector.
    const panel = screen.getByRole("region", {
      name: "Memory, no scale to draw against",
    });
    // Scoped to this panel: the fixture carries mem_used alone, so most of
    // the tab genuinely IS not collected and says so.
    expect(panel.textContent).not.toMatch(/Not collected/);
    expect(
      screen.getByText(/no memory ceiling in this window/i),
    ).toBeInTheDocument();
  });
});

// A cross-source panel whose foreign family answered with NO SERIES must draw
// the bands it does have, not throw.
//
// This is a real answer rather than an edge case: read/metrics.go initialises
// `out := []Series{}`, and InsertHostSnmpSamples skips a sample with none of
// its seventy columns set -- so a host reporting host_samples with no snmp
// rows in the window gets a 200 carrying `series: []`. seriesTimestamps()
// THROWS on that, and there is no error boundary above these panels, so the
// throw took the whole tab white. griddedValues() has guarded exactly this
// since it was written; the cross-source reader did not.
describe("a cross-source panel with an empty foreign family", () => {
  const fragHost = response({
    family: "host",
    columns: [
      "ip_reasm_reqds_per_s",
      "ip_reasm_fails_per_s",
      "ip_frag_fails_per_s",
      "ip_frag_creates_per_s",
    ],
    series: [
      {
        key: {},
        points: [
          [1_786_320_000_000, 1.4, 0.05, 0.03, 1.1],
          [1_786_320_060_000, 1.5, 0.04, 0.02, 1.2],
        ],
      },
    ],
  });

  // The columns exist on the tier; the host simply reported no rows.
  const emptySnmp = response({
    family: "host_snmp",
    columns: ["ip_reasm_oks_per_s", "ip_frag_oks_per_s"],
    series: [],
  });

  it("draws its own series instead of throwing", () => {
    render(<NetworkGraphs host={fragHost} hostSnmp={emptySnmp} />);

    // The four host_samples series are ledger rows...
    expect(
      screen.getByText("IP fragmentation · reasm reqd"),
    ).toBeInTheDocument();
    expect(
      screen.getByText("IP fragmentation · frag create"),
    ).toBeInTheDocument();
    // ...and the two that had no series to read are absent rather than drawn
    // as a flat zero the host never reported.
    expect(
      screen.queryByText("IP fragmentation · reasm ok"),
    ).not.toBeInTheDocument();
  });

  // A foreign family the page has not fetched at all is the same fact as one
  // that answered empty, and must not throw either.
  it("tolerates the foreign family being absent entirely", () => {
    render(<NetworkGraphs host={fragHost} />);
    expect(
      screen.getByText("IP fragmentation · reasm reqd"),
    ).toBeInTheDocument();
  });
});

// The tab passes each spec's own sentence through, and the specs that have
// none stay bare. Both halves matter: a glyph on every panel is the same as a
// glyph on none.
// "Loading" and "not collected" were the same panel: a family the page had
// not been answered for yet read as one the host never collected.
describe("a family that has not arrived", () => {
  it("is loading while the tab's first answer is outstanding", () => {
    const { container } = render(<SystemGraphs host={null} pending />);
    expect(
      screen.getByRole("region", { name: "Load, loading" }),
    ).toHaveTextContent("Asking the hub for this window.");
    expect(screen.queryByText(/Not collected/)).toBeNull();
    // The ledger says the same in its rows
    expect(
      within(container.querySelector(".ledger") as HTMLElement).getAllByText(
        "Asking the hub for this window.",
      ).length,
    ).toBeGreaterThan(0);
  });

  it("is not read once the answer came without it", () => {
    render(<SystemGraphs host={null} />);
    expect(
      screen.getByRole("region", { name: "Load, not collected" }),
    ).toHaveTextContent("No data has been read for this family yet.");
  });
});

describe("panel explanations", () => {
  it("carries a spec's text to its panel", () => {
    render(<SystemGraphs host={null} />);

    expect(
      screen.getByRole("button", { name: "About Load" }),
    ).toBeInTheDocument();
  });

  it("leaves a spec without one bare", () => {
    render(<SystemGraphs host={null} />);

    expect(
      screen.queryByRole("button", { name: "About Memory" }),
    ).not.toBeInTheDocument();
  });
});

describe("event markers", () => {
  const at = Date.parse("2026-08-10T00:30:00Z");

  it("are drawn on the lead charts of a tab and nowhere else", () => {
    // Given a host with load and context switches, and one event in the window
    const { container } = render(
      <SystemGraphs
        host={fullHost}
        events={[{ at, severity: "warning", label: "load high" }]}
      />,
    );

    // Then Load, a lead chart, carries it, and the ledger's trends -- context
    // switches among them -- do not
    const load = screen.getByRole("region", { name: "Load chart" });
    expect(load.querySelectorAll("[data-marker]")).toHaveLength(1);
    const ledger = container.querySelector(".ledger")!;
    expect(within(ledger as HTMLElement).getByText("Context switches"));
    expect(ledger.querySelectorAll("[data-marker]")).toHaveLength(0);
  });

  it("are the page's events, in the event log's words", () => {
    const base = {
      host_id: 7,
      hostname: "kessel",
      type: "unit_failed",
      detail: null,
    };
    const events = timedEvents([
      {
        ...base,
        id: "e:1",
        ts: "2026-08-10T00:30:00Z",
        subject: "nginx.service",
        severity: "critical",
      },
      // An unreadable timestamp has no place on a time axis
      { ...base, id: "e:2", ts: "not a time", subject: "x", severity: "info" },
    ]);
    expect(events).toHaveLength(1);
    expect(events[0]).toMatchObject({ at, severity: "critical" });
    expect(events[0]!.label).toMatch(/nginx/);
    expect(timedEvents(null)).toEqual([]);
  });
});
