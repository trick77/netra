// Hub latency draws two units on one axis labelled ms: the handshake is
// stored in microseconds, the round trip in milliseconds. These pin that the
// panel converts the handshake -- line AND peak envelope -- and leaves the
// round trip alone.
import { describe, expect, it } from "vitest";
import type { MetricsResponse } from "../../lib/api";
import { bandsFor, specForSlug, type PanelSpec } from "./chartSpecs";

const at = (i: number) => Date.parse(`2026-08-10T00:0${i}:00Z`);
const iso = (i: number) => `2026-08-10T00:0${i}:00Z`;

function agent(
  tier: string,
  columns: string[],
  rows: (number | null)[][],
): MetricsResponse {
  return {
    family: "agent",
    tier,
    step_s: 60,
    // Half-open at raw, inclusive of `to` at a rolled-up tier (seriesOnGrid).
    window: {
      from: iso(0),
      to: iso(tier === "raw" ? rows.length : rows.length - 1),
    },
    requested_window: { from: iso(0), to: iso(rows.length) },
    warnings: [],
    key_columns: [],
    columns,
    series: [{ key: {}, points: rows.map((r, i) => [at(i), ...r]) }],
    truncated: false,
  } as unknown as MetricsResponse;
}

const spec = specForSlug("hub-latency")!;

function byName(bands: ReturnType<typeof bandsFor>, name: string) {
  const band = bands.find((b) => b.name === name);
  expect(band).toBeDefined();
  return band!;
}

describe("the hub-latency spec", () => {
  it("draws the raw handshake in milliseconds", () => {
    // Given a raw-tier response: a LAN hub, 350us at its best and 900us at
    // its worst handshake, 12ms for the whole POST
    const res = agent(
      "raw",
      ["hub_connect_us", "hub_connect_max_us", "post_latency_ms"],
      [
        [350, 900, 12],
        [null, null, null],
        [1500, 2400, 15],
      ],
    );

    // When the panel's bands are built
    const bands = bandsFor(spec, res, { withPeakBand: true });

    // Then the handshake reads in ms, below the round trip it is part of,
    // a gap stays a gap, and the round trip is drawn as stored
    expect(byName(bands, "handshake").values).toEqual([0.35, null, 1.5]);
    expect(byName(bands, "handshake peak").values).toEqual([0.9, null, 2.4]);
    expect(byName(bands, "round trip").values).toEqual([12, null, 15]);
  });

  it("scales the peak envelope with its line at a rolled-up tier", () => {
    // Given a 5m-tier response, where every base carries a mean and a max
    const res = agent(
      "5m",
      [
        "hub_connect_us_avg",
        "hub_connect_us_max",
        "hub_connect_max_us_avg",
        "hub_connect_max_us_max",
        "post_latency_ms_avg",
        "post_latency_ms_max",
      ],
      [
        [400, 2000, 800, 3000, 10, 40],
        [600, 1000, 1200, 5000, 20, 30],
      ],
    );

    // When the panel's bands are built with the envelope
    const bands = bandsFor(spec, res, { withPeakBand: true });

    // Then the handshake's envelope is in ms too, not a thousand times above
    // its own line, and the round trip's is untouched
    const handshake = byName(bands, "handshake");
    expect(handshake.values).toEqual([0.4, 0.6]);
    expect(handshake.band).toEqual([2, 1]);
    expect(byName(bands, "handshake peak").band).toEqual([3, 5]);
    const roundTrip = byName(bands, "round trip");
    expect(roundTrip.values).toEqual([10, 20]);
    expect(roundTrip.band).toEqual([40, 30]);
  });

  it("prints a sub-millisecond handshake rather than 0", () => {
    // A LAN handshake is a fraction of a millisecond, one to a hub on the
    // same host a few microseconds; rounding either to two decimals is the
    // truncation the agent stores microseconds to avoid.
    expect(spec.fmt?.(0.35)).toBe("0.35");
    expect(spec.fmt?.(0.004)).toBe("0.004");
    expect(spec.fmt?.(0.0345)).toBe("0.035");
    // At a millisecond and above it reads like every other count.
    expect(spec.fmt?.(12.345)).toBe("12.35");
    expect(spec.fmt?.(null)).toBe(
      specForSlug("hub-connect-failures")!.fmt?.(null),
    );
  });
});

describe("a base with a divisor", () => {
  it("is ordered by worst in the panel's unit, not as stored", () => {
    // Given a keyed spec ordered by worst, one base in µs and one in ms:
    // "a" is worst on screen (50 ms against b's 200), "b" is worst as stored
    // (200 against a's 600 and 50000)
    const ordered: PanelSpec = {
      slug: "test-ordered",
      title: "Ordered",
      source: "agent",
      bases: [
        { base: "fine_us", label: "fine", divisor: 1000 },
        { base: "coarse_ms", label: "coarse" },
      ],
      orderByWorst: true,
    };
    const res = {
      ...agent("raw", ["fine_us", "coarse_ms"], []),
      key_columns: ["name"],
      series: [
        { key: { name: "b" }, points: [[at(0), 2_000_000, 200]] },
        { key: { name: "a" }, points: [[at(0), 50_000, 600]] },
      ],
      window: { from: iso(0), to: iso(1) },
    } as unknown as MetricsResponse;

    // When the bands are built
    const bands = bandsFor(ordered, res);

    // Then "a" comes first, its fine reading divided before it was compared
    expect(bands.map((b) => b.name)).toEqual([
      "a fine",
      "a coarse",
      "b fine",
      "b coarse",
    ]);
    expect(bands[0]!.values).toEqual([50]);
  });
});
