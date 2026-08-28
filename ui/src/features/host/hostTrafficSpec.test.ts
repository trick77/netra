// The fleet's traffic cell and the Traffic chart page are ONE chart.
//
// The cell sums every interface into one in/out pair; the page STACKS them,
// one pair per interface. That is the same chart and not a different one only
// because the stack's envelope is the sum -- which is an arithmetic claim, so
// this file makes it as one: the per-interface bands, added up at every
// bucket, have to BE the cell's series, gap and all.
//
// It compares READINGS, not implementations. Asserting that both import
// sumSeries would pass forever while one of them started scaling, averaging
// or dropping a null.
import { describe, expect, it } from "vitest";
import type { MetricsResponse } from "../../lib/api";
import { bandsFor, familyFor, specForSlug } from "./chartSpecs";
import { cpuBands, trafficSeries } from "../fleet/hostTrends";
import { perCoreBands } from "../../lib/bands";
import { DOWN_SHADES, UP_SHADES } from "../../ui/charts/UpDownSparkline";

// Two interfaces, at the raw tier -- where peakBase() falls back to the bare
// column and there is no _max peer, so no envelope is drawn. A null in eth0's
// third bucket, because "unknowable" has to survive the sum as a gap rather
// than shrink the total.
function net(): MetricsResponse {
  // points[0] is epoch MILLIseconds -- the one timestamp in the read API
  // that is not an ISO string (see seriesTimestamps) -- and `columns` names
  // the cells after it.
  const at = (i: number) => Date.parse(`2026-08-10T00:0${i}:00Z`);
  const iso = (i: number) => `2026-08-10T00:0${i}:00Z`;
  return {
    family: "net",
    tier: "raw",
    step_s: 60,
    window: { from: iso(0), to: iso(4) },
    requested_window: { from: iso(0), to: iso(4) },
    warnings: [],
    key_columns: ["iface"],
    columns: ["rx_bytes", "tx_bytes"],
    series: [
      {
        key: { iface: "eth0" },
        points: [
          [at(0), 100, 10],
          [at(1), 200, 20],
          [at(2), null, 30],
          [at(3), 400, 40],
        ],
      },
      {
        key: { iface: "eth1" },
        points: [
          [at(0), 1, 2],
          [at(1), 2, 4],
          [at(2), 3, 6],
          [at(3), 4, 8],
        ],
      },
    ],
    truncated: false,
  } as unknown as MetricsResponse;
}

describe("the host-traffic spec", () => {
  it("resolves from its slug", () => {
    // The slug is published -- a link someone sent has to keep working.
    expect(specForSlug("host-traffic")?.title).toBe("Traffic");
  });

  it("draws one in/out pair per interface, in that order", () => {
    // Given a two-interface response
    const res = net();

    // When the page's bands are built
    const bands = bandsFor(specForSlug("host-traffic")!, res);

    // Then there is a pair per interface, named for the link that carried it
    // -- and the two of a pair are ADJACENT, in in/out order. Chart's
    // mirrored stack reads its halves off band position (even is up, odd is
    // down), so an interleaving that put both "in" bands first would draw
    // eth1's inbound below the midline.
    expect(bands.map((b) => b.name)).toEqual([
      "eth0 in",
      "eth0 out",
      "eth1 in",
      "eth1 out",
    ]);
  });

  it("stacks to exactly what the fleet cell draws", () => {
    // Given the same response the fleet row reads
    const res = net();

    // When both are computed
    const cell = trafficSeries(res);
    const page = bandsFor(specForSlug("host-traffic")!, res);

    // Then each half of the stack, summed, IS the cell's series -- which is
    // what makes the cell and the chart it opens the same chart. A null
    // survives as a gap rather than shrinking the total: the stack cannot
    // draw a bucket one interface never reported, and neither can the cell.
    const half = (offset: number) =>
      page[0]!.values.map((_, i) => {
        const at = page
          .filter((_, b) => b % 2 === offset)
          .map((b) => b.values[i]);
        return at.some((v) => v === null)
          ? null
          : at.reduce<number>((a, v) => a + v!, 0);
      });
    expect(half(0)).toEqual(cell.rx);
    expect(half(1)).toEqual(cell.tx);
    expect(cell.rx[2]).toBeNull();
  });

  it("keeps green above and purple below, one step per interface", () => {
    // Given the page's bands
    const bands = bandsFor(specForSlug("host-traffic")!, net());

    // Then the FIRST interface draws the sparkline's pinned pair exactly, so
    // a one-NIC host's panel is the fleet cell at another size; the second
    // steps within the same two hues rather than taking the --s1/--s3 the
    // SERIES_VARS index walk would have handed it. Green means "in" on the
    // fleet row; it has to mean "in" here.
    expect(bands.map((b) => b.color)).toEqual([
      UP_SHADES[0],
      DOWN_SHADES[0],
      UP_SHADES[1],
      DOWN_SHADES[1],
    ]);
  });

  it("leaves out an interface that moved nothing all window", () => {
    // Given a response whose second interface read zero in both directions
    // at every bucket
    const res = net();
    res.series[1]!.points = res.series[1]!.points.map(([at]) => [
      at,
      0,
      0,
    ]) as (typeof res.series)[number]["points"];

    // When the page's bands are built
    const bands = bandsFor(specForSlug("host-traffic")!, res);

    // Then it is not drawn and not named: a flat line on the midline and a
    // legend row are what the panel would spend on it, and the address table
    // on the same tab is where "this NIC exists" belongs.
    expect(bands.map((b) => b.name)).toEqual(["eth0 in", "eth0 out"]);

    // And the live interface keeps the FIRST shade, so which step a link
    // draws in follows what was drawn rather than what was answered.
    expect(bands.map((b) => b.color)).toEqual([UP_SHADES[0], DOWN_SHADES[0]]);
  });

  it("drops an interface that can only draw half its pair", () => {
    // Given an interface whose rx is null at every bucket -- the case a bare
    // metal host's cpu_steal is on the CPU stack: correctly absent, not zero
    // -- while its tx reports normally
    const res = net();
    res.series[1]!.points = res.series[1]!.points.map(([at], i) => [
      at,
      null,
      i + 1,
    ]) as (typeof res.series)[number]["points"];

    // When the page's bands are built
    const bands = bandsFor(specForSlug("host-traffic")!, res);

    // Then eth1 is left out ENTIRELY, both halves. A stacked mirror reads its
    // halves off band position, so contributing "eth1 out" alone would shift
    // it to an even index and draw an outbound reading above the midline.
    // Losing one interface's half-reading is the smaller lie.
    expect(bands.map((b) => b.name)).toEqual(["eth0 in", "eth0 out"]);
  });

  it("keeps an interface that moved once", () => {
    // Given an interface idle but for a single bucket
    const res = net();
    res.series[1]!.points = res.series[1]!.points.map(([at], i) => [
      at,
      i === 2 ? 7 : 0,
      0,
    ]) as (typeof res.series)[number]["points"];

    // Then it stays: that one bucket is the thing somebody opened the panel
    // to find.
    const bands = bandsFor(specForSlug("host-traffic")!, res);
    expect(bands.map((b) => b.name)).toEqual([
      "eth0 in",
      "eth0 out",
      "eth1 in",
      "eth1 out",
    ]);
  });

  // Every fleet sparkline has a page, and each one reads the family it
  // actually draws. host-cpu asking for "host" instead of "cpu_core" is the
  // failure this catches: the hub 400s on nothing, it just answers with a
  // response carrying none of the columns, and the page says "no samples"
  // about a host reporting perfectly well.
  it.each([
    ["host-cpu", "cpu_core"],
    ["host-memory", "host"],
    ["host-filesystem", "filesystem"],
    ["host-traffic", "net"],
  ])("resolves %s to family %s", (slug, family) => {
    const spec = specForSlug(slug);
    expect(spec).toBeDefined();
    expect(familyFor(spec!)).toBe(family);
  });
});

// The 5m tier, where rx_bytes and rx_bytes_max are different columns.
//
// Traffic draws no envelope any more, and this is the tier that would show
// one: stacked, the mark would be the running total of each interface's
// bucket MAXIMUM, and the interfaces do not peak in the same bucket -- it
// would state a throughput no bucket ever carried. The line stays the mean,
// which is what the fleet cell reads.
function netRolledUp(): MetricsResponse {
  const at = (i: number) => Date.parse(`2026-08-10T00:0${i}:00Z`);
  const iso = (i: number) => `2026-08-10T00:0${i}:00Z`;
  return {
    family: "net",
    tier: "5m",
    step_s: 60,
    window: { from: iso(0), to: iso(3) },
    requested_window: { from: iso(0), to: iso(3) },
    warnings: [],
    key_columns: ["iface"],
    columns: ["rx_bytes", "rx_bytes_max", "tx_bytes", "tx_bytes_max"],
    series: [
      {
        key: { iface: "eth0" },
        points: [
          [at(0), 10, 100, 1, 10],
          [at(1), 20, 200, 2, 20],
          [at(2), 30, 300, 3, 30],
        ],
      },
      {
        key: { iface: "eth1" },
        points: [
          [at(0), 1, 5, 1, 2],
          [at(1), 2, 6, 2, 4],
          [at(2), 3, 7, 3, 6],
        ],
      },
    ],
    truncated: false,
  } as unknown as MetricsResponse;
}

describe("the Traffic page on a rolled-up tier", () => {
  it("draws the mean, and no peak envelope over it", () => {
    // Given a rolled-up response, where peak and mean are separate columns
    const res = netRolledUp();

    // When the page's bands are built the way the page builds them
    const page = bandsFor(specForSlug("host-traffic")!, res, {
      withPeakBand: true,
    });
    const cell = trafficSeries(res);

    // Then the stack is the MEAN of each bucket
    expect(page.map((b) => b.values[0])).toEqual([10, 1, 1, 1]);

    // ...and the fleet cell is the PEAK, so the two are deliberately not the
    // same number any more. The panel draws one band per interface and a
    // stack of per-interface peaks would state a throughput no bucket ever
    // carried; the cell draws a single summed pair at 170 px, where a mean
    // of a mean is a burst nobody can see. Same host, two questions.
    expect(cell.rx[0]).toBe(105);

    // And no band carries an envelope. `band` lives on Band (the chart-panel
    // type bandsFor returns) but not on the narrower OverlaySeries the array
    // is typed as here, so it is read off explicitly.
    const envelope = (b: (typeof page)[number]) =>
      (b as { band?: (number | null)[] }).band;
    expect(page.map(envelope)).toEqual([
      undefined,
      undefined,
      undefined,
      undefined,
    ]);
  });
});

describe("the CPU page on a host too large for the fleet cell", () => {
  function cores(n: number): MetricsResponse {
    const at = (i: number) => Date.parse(`2026-08-10T00:0${i}:00Z`);
    const iso = (i: number) => `2026-08-10T00:0${i}:00Z`;
    return {
      family: "cpu_core",
      tier: "raw",
      step_s: 60,
      window: { from: iso(0), to: iso(3) },
      requested_window: { from: iso(0), to: iso(3) },
      warnings: [],
      key_columns: ["core"],
      columns: ["busy"],
      series: Array.from({ length: n }, (_, c) => ({
        key: { core: String(c) },
        points: [
          [at(0), 40],
          [at(1), 80],
          [at(2), 40],
        ],
      })),
      truncated: false,
    } as unknown as MetricsResponse;
  }

  // Above MAX_PER_CORE the fleet row will not fetch 128 series for a 170px
  // chart and falls back to a single cpu_total band, while the page draws
  // every core. That divergence is deliberate -- but only because the
  // SILHOUETTE survives it: a normalised per-core stack sums to cpu_total.
  // If it ever stops summing to cpu_total, the page stops being the chart the
  // reader clicked, and that is what this pins.
  it("stacks to the same total the cell's single band draws", () => {
    // Given a 128-core response
    const res = cores(128);

    // When the page's bands are built
    const page = bandsFor(specForSlug("host-cpu")!, res);

    // Then there is one band per core...
    expect(page).toHaveLength(128);

    // ...and they sum, at every bucket, to the mean across cores -- which is
    // cpu_total, the one band the large-host cell falls back to.
    const totals = page[0]!.values.map((_, i) =>
      page.reduce((sum, b) => sum + (b.values[i] ?? 0), 0),
    );
    expect(totals.map((t) => Math.round(t))).toEqual([40, 80, 40]);
  });

  // And the small-host case, where the two are the same chart outright.
  it("matches the cell exactly on a host the cell does fetch cores for", () => {
    const res = cores(4);

    const page = bandsFor(specForSlug("host-cpu")!, res);
    const cell = cpuBands(null, res).bands;

    expect(page.map((b) => b.values)).toEqual(cell.map((b) => b.values));
    expect(page.map((b) => b.name)).toEqual(
      perCoreBands(res, { normalise: true }).map((b) => b.name),
    );
  });
});
