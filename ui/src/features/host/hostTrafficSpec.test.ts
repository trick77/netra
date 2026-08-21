// The fleet's traffic cell and the Traffic chart page are ONE chart.
//
// That is the whole reason host-traffic exists as its own slug rather than
// the cell linking to interface-throughput: the cell sums every interface
// into one in/out pair, the throughput panel draws a pair per interface and
// sums nothing, and an enlarged view that quietly becomes a different chart
// is the one thing clicking a chart must not do.
//
// So this compares the two READINGS, not their implementations. Both now go
// through sumSeries in lib/metrics, and asserting that they import the same
// function would pass forever while one of them started scaling, averaging
// or dropping a null.
import { describe, expect, it } from "vitest";
import type { MetricsResponse } from "../../lib/api";
import { bandsFor, familyFor, specForSlug } from "./chartSpecs";
import { cpuBands, trafficSeries } from "../fleet/hostTrends";
import { perCoreBands } from "../../lib/bands";
import { DOWN_COLOR, UP_COLOR } from "../../ui/charts/UpDownSparkline";

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

  it("folds every interface into one in/out pair", () => {
    // Given a two-interface response
    const res = net();

    // When the page's bands are built
    const bands = bandsFor(specForSlug("host-traffic")!, res);

    // Then there are two bands, not two per interface -- and they are named
    // for the direction, never for an interface that was summed away.
    expect(bands.map((b) => b.name)).toEqual(["in", "out"]);
  });

  it("draws exactly what the fleet cell draws", () => {
    // Given the same response the fleet row reads
    const res = net();

    // When both are computed
    const cell = trafficSeries(res);
    const page = bandsFor(specForSlug("host-traffic")!, res);

    // Then the page's values ARE the cell's, gap and all
    expect(page[0]?.values).toEqual(cell.rx);
    expect(page[1]?.values).toEqual(cell.tx);
    expect(cell.rx[2]).toBeNull();
  });

  it("keeps green above and purple below", () => {
    // Given the page's bands
    const bands = bandsFor(specForSlug("host-traffic")!, net());

    // Then the colours are the sparkline's pinned pair, NOT the --s1/--s2
    // the SERIES_VARS index walk would have handed positions 0 and 1. Green
    // means "in" on the fleet row; it has to mean "in" here.
    expect(bands.map((b) => b.color)).toEqual([UP_COLOR, DOWN_COLOR]);
  });

  it("leaves interface-throughput on its per-interface hues", () => {
    // Given the same two-interface response through the OTHER net spec
    const bands = bandsFor(specForSlug("interface-throughput")!, net());

    // Then every band has its own colour: that panel answers "which
    // interface", and four bands in two colours is the collision
    // SERIES_VARS was widened to eight to fix.
    expect(bands).toHaveLength(4);
    expect(new Set(bands.map((b) => b.color)).size).toBe(4);
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

// The 5m tier, where rx_bytes and rx_bytes_max are different columns and the
// page therefore draws an envelope. The raw fixture above cannot exercise
// that path at all: peakBase falls back to the bare name there, so the line
// and the band resolve to the same column and any identity claim passes
// trivially.
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

describe("the Traffic page's envelope", () => {
  // The design claim is "the sparkline, enlarged -- nothing moves", and this
  // pins it across the two places traffic is drawn. It used to hold the other
  // way up: the cell read the bucket's PEAK, so what had to equal the cell was
  // the page's BAND. The cell reads the mean now (trafficSeries), which makes
  // the agreement a stronger one -- the cell and the page's LINE are the same
  // series, and the envelope is the extra the larger chart has room for.
  // Asserted on the options the page actually passes (ChartPage:
  // withPeakBand: true), because on the default options the line is the peak
  // and the claim is true for free.
  it("draws the cell's own curve as the line, with the peak as an envelope over it", () => {
    // Given a rolled-up response, where peak and mean are separate columns
    const res = netRolledUp();

    // When the page's bands are built the way the page builds them
    const page = bandsFor(specForSlug("host-traffic")!, res, {
      withPeakBand: true,
    });
    const cell = trafficSeries(res);

    // Then the page's line IS the fleet cell's curve, both being the mean
    expect(page[0]?.values).toEqual(cell.rx);
    expect(page[1]?.values).toEqual(cell.tx);
    expect(page[0]?.values).toEqual([11, 22, 33]);

    // And the envelope over it is the peak -- a different, higher series.
    // `band` lives on Band (the chart-panel type bandsFor returns) but not on
    // the narrower OverlaySeries the array is typed as here, so it is read
    // off explicitly.
    const envelope = (b: (typeof page)[number]) =>
      (b as { band?: (number | null)[] }).band;
    expect(envelope(page[0]!)).toEqual([105, 206, 307]);
    expect(envelope(page[0]!)).not.toEqual(cell.rx);
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
