import { describe, expect, it } from "vitest";
import { render, screen, within } from "@testing-library/react";
import type { MetricsResponse } from "../../../lib/api";
import {
  CollectorGraphs,
  NetworkGraphs,
  StorageGraphs,
  SystemGraphs,
  type GraphsProps,
} from "./Graphs";
import { ABSENT } from "../../../lib/format";
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
      <CollectorGraphs {...props} />
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
          1_754_784_000_000, 800, 700, 220, 190, 0.2, 0.1, 0.3, 0.1, 1.2, 1.1,
          0.5, 0.4,
        ],
        [
          1_754_784_060_000, 820, 710, 225, 195, 0.3, 0.2, 0.4, 0.2, 1.3, 1.2,
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
    render(<Graphs host={fullHost} />);
    for (const group of [
      "Resources",
      "Kernel",
      "Agent",
      "Traffic",
      "IP",
      "TCP",
      "UDP",
      "ICMP",
      "Filesystems",
      "Disks",
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

  // The Overview's traffic panel and the Network tab's are two specs over one
  // pair of columns, and the split is the point: this page asks what the box
  // is moving, that tab asks which link moved it.
  it("keeps the summed traffic panel off the Network tab", () => {
    const network = NETWORK_GROUPS.flatMap((g) => g.specs.map((s) => s.slug));
    expect(network).toContain("host-traffic");
    expect(network).not.toContain("host-traffic-total");
    expect(UNGROUPED_SLUGS).toContain("host-traffic-total");
  });

  it("puts the network panels on Network and the disks on Storage", () => {
    render(<NetworkGraphs host={fullHost} />);
    expect(
      screen.getByRole("heading", { name: "TCP statistics" }),
    ).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Kernel" })).toBeNull();
    expect(screen.queryByRole("heading", { name: "Disks" })).toBeNull();
  });

  // These three used to be hardcoded "not collected" panels: spec §11 listed
  // IP statistics, ICMP statistics and ICMP informational as families with no
  // data behind them anywhere in the schema. They are collected now, so the
  // assertion is inverted -- they must draw, and nothing in the Network group
  // may still claim a permanent schema gap.
  it("draws the IP and ICMP panels from the host_snmp family", () => {
    render(<Graphs host={fullHost} hostSnmp={fullHostSnmp} />);

    for (const title of [
      "IP statistics",
      "ICMP statistics",
      "ICMP informational",
    ]) {
      expect(screen.getByRole("heading", { name: title })).toBeInTheDocument();
      expect(
        screen.queryByRole("region", { name: `${title}, not collected` }),
      ).not.toBeInTheDocument();
    }
  });

  // Without the family the three panels have nothing to draw, and that must
  // read as "this window has no samples" rather than as a chart of zeroes --
  // absent is not zero (spec §7.5). It must also not take the Network group's
  // other panels down with it.
  it("leaves the IP and ICMP panels empty when the host_snmp family is absent", () => {
    render(<Graphs host={fullHost} />);

    // Not merely present: each of the three says it has nothing to draw,
    // which is the whole point -- a blank chart would assert the host
    // reported zeroes.
    for (const title of [
      "IP statistics",
      "ICMP statistics",
      "ICMP informational",
    ]) {
      expect(
        screen.getByRole("region", { name: `${title}, not collected` }),
      ).toHaveTextContent("No data has been read for this family yet.");
    }
    // And the family it does have still draws, so one absent fetch does not
    // take the rest of the Network group with it.
    expect(
      screen.getByRole("heading", { name: "IP fragmentation" }),
    ).toBeInTheDocument();
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
      screen.getByRole("region", { name: /Load averages chart/ }),
    ).toBeInTheDocument();
  });

  it("draws every panel at the same size", () => {
    const { container } = render(<Graphs host={fullHost} />);
    const sizes = new Set(
      Array.from(container.querySelectorAll("svg.spark")).map(
        (svg) => `${svg.getAttribute("width")}x${svg.getAttribute("height")}`,
      ),
    );
    expect(sizes.size).toBe(1);
  });

  // A tier that carries some of a panel's columns and not others still
  // draws: the point is that it must not throw, and must not silently drop
  // the panel either.
  it("survives a tier that does not carry a panel's columns", () => {
    render(<Graphs host={sparseHost} />);

    const panel = screen.getByRole("region", {
      name: /Load averages(, not collected| chart)/,
    });
    expect(panel).toBeInTheDocument();
  });

  // A collector that STOPPED reports nothing for the buckets it missed, so

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
    const panel = screen.getByRole("region", {
      name: /Hub connect failures chart/,
    });
    expect(panel).toHaveTextContent("1");
    expect(panel).not.toHaveTextContent("4002");
  });

  it("carries no range control of its own — the header's drives every panel", () => {
    render(<Graphs host={fullHost} />);
    expect(screen.queryByRole("group")).toBeNull();
  });

  it("keeps one panel per metric when a family has many series", () => {
    const diskIo = response({
      family: "disk_io",
      key_columns: ["device"],
      columns: [
        "read_bytes",
        "write_bytes",
        "io_util_pct",
        "r_await_ms",
        "w_await_ms",
      ],
      series: [
        {
          key: { device: "sda" },
          points: [[1_754_784_000_000, 1000, 2000, 4, 1, 2]],
        },
        {
          key: { device: "sdb" },
          points: [[1_754_784_000_000, 10, 20, 1, 3, 4]],
        },
      ],
    });
    render(<Graphs host={fullHost} diskIo={diskIo} />);
    expect(
      screen.getAllByRole("region", { name: /Disk throughput chart/ }),
    ).toHaveLength(1);
    // One band per device, named by device, inside that single panel.
    expect(screen.getByText("sda read")).toBeInTheDocument();
    expect(screen.getByText("sdb write")).toBeInTheDocument();
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
          [1_754_784_000_000, 1.4, 0.05, 0.03, 1.1],
          [1_754_784_060_000, 1.5, 0.04, 0.02, 1.2],
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

  it("draws its own bands instead of throwing", () => {
    render(<NetworkGraphs host={fragHost} hostSnmp={emptySnmp} />);

    const panel = screen.getByRole("region", {
      name: "IP fragmentation chart",
    });
    expect(panel).toBeInTheDocument();
    // The four host_samples bands are there...
    expect(within(panel).getByText("reasm reqd")).toBeInTheDocument();
    expect(within(panel).getByText("frag create")).toBeInTheDocument();
    // ...and the two that had no series to read are absent rather than drawn
    // as a flat zero the host never reported.
    expect(within(panel).queryByText("reasm ok")).not.toBeInTheDocument();
  });

  it("does not report the panel as uncollected when it has bands", () => {
    render(<NetworkGraphs host={fragHost} hostSnmp={emptySnmp} />);
    expect(
      screen.queryByRole("region", { name: "IP fragmentation, not collected" }),
    ).not.toBeInTheDocument();
  });

  // A foreign family the page has not fetched at all is the same fact as one
  // that answered empty, and must not throw either.
  it("tolerates the foreign family being absent entirely", () => {
    render(<NetworkGraphs host={fragHost} />);
    const panel = screen.getByRole("region", {
      name: "IP fragmentation chart",
    });
    expect(within(panel).getByText("reasm reqd")).toBeInTheDocument();
  });
});

// The tab passes each spec's own sentence through, and the specs that have
// none stay bare. Both halves matter: a glyph on every panel is the same as a
// glyph on none.
describe("panel explanations", () => {
  it("carries a spec's text to its panel", () => {
    render(<StorageGraphs filesystem={null} diskIo={null} />);

    expect(
      screen.getByRole("button", { name: "About Disk utilisation" }),
    ).toBeInTheDocument();
  });

  it("leaves a spec without one bare", () => {
    render(<StorageGraphs filesystem={null} diskIo={null} />);

    expect(
      screen.queryByRole("button", { name: "About Disk throughput" }),
    ).not.toBeInTheDocument();
  });
});
