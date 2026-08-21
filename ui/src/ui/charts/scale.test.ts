import { describe, expect, it } from "vitest";
import { asinhScale, linearScale, TRAFFIC_KNEE, trafficScale } from "./scale";

describe("linearScale", () => {
  it("maps the floor to 0 and the ceiling to 1", () => {
    // Given a proportional scale
    const scale = linearScale(200);

    // Then the two ends are the two ends, and the middle is the middle
    expect(scale(0)).toBe(0);
    expect(scale(200)).toBe(1);
    expect(scale(100)).toBe(0.5);
  });

  it("reports 0 for a zero ceiling rather than dividing by it", () => {
    // Given a chart whose series is entirely zero -- an idle interface
    const scale = linearScale(0);

    // Then it draws on the baseline instead of returning NaN
    expect(scale(0)).toBe(0);
    expect(scale(5)).toBe(0);
  });
});

describe("asinhScale", () => {
  it("maps zero to zero, which is why it is asinh and not log", () => {
    // Given a compressed scale
    const scale = asinhScale(TRAFFIC_KNEE, 100_000_000);

    // Then a genuine zero reading -- an interface that moved nothing all day
    // -- sits on the midline. log10(0) is -Infinity and would need an
    // invented floor to be drawable at all.
    expect(scale(0)).toBe(0);
    expect(Number.isFinite(scale(0))).toBe(true);
  });

  it("is proportional below the knee", () => {
    // Given readings an order of magnitude under the knee
    const scale = asinhScale(TRAFFIC_KNEE, 400);

    // Then the curve has barely started bending: half the ceiling is still
    // very nearly half the height (0.509, not 0.500). This is what lets one
    // scale serve a host moving a few hundred bytes a second without
    // distorting its shape, and it is why the sparkline's existing geometry
    // tests -- written against small numbers -- still hold unchanged.
    expect(scale(200)).toBeCloseTo(0.509, 3);
    expect(scale(100)).toBeCloseTo(0.256, 3);
  });

  it("compresses above the knee, spending equal height per decade", () => {
    // Given a ceiling four decades above the knee
    const scale = asinhScale(TRAFFIC_KNEE, 10_000_000);

    // Then the decades are spread up the box rather than sharing a
    // thousandth of it at the bottom. Not exactly a quarter each: the lowest
    // decade sits where the curve is still part linear, so it takes 0.30 and
    // the two above it 0.23 apiece. Evenly ENOUGH is the property -- every
    // decade gets pixels.
    const decades = [1e4, 1e5, 1e6, 1e7].map(scale);
    expect(decades[0]).toBeCloseTo(0.303, 3);
    expect(decades[1]).toBeCloseTo(0.535, 3);
    expect(decades[2]).toBeCloseTo(0.7675, 3);
    expect(decades[3]).toBe(1);
  });

  it("never goes backwards", () => {
    // Given the scale a traffic chart uses
    const scale = asinhScale(TRAFFIC_KNEE, 1e8);

    // Then more traffic is always drawn higher -- a chart where it was not
    // would be worse than an unreadable one
    let previous = -1;
    for (const v of [0, 1, 100, 1e3, 1e4, 1e5, 1e6, 1e7, 1e8]) {
      const at = scale(v);
      expect(at).toBeGreaterThan(previous);
      previous = at;
    }
  });

  it("refuses a degenerate knee or ceiling instead of returning NaN", () => {
    // Given a chart with nothing to scale to
    // Then every reading draws on the baseline
    expect(asinhScale(0, 100)(50)).toBe(0);
    expect(asinhScale(TRAFFIC_KNEE, 0)(50)).toBe(0);
  });
});

describe("trafficScale on the measurement this exists for", () => {
  // ark.o11.net, 24 h to 2026-08-21 06:35 UTC, 286 five-minute buckets from
  // /api/v1/hosts/1/metrics: typical bucket 29.0 kB/s, day's peak 101.4 MB/s.
  // The fleet cell is 32px tall with pad 2, so a mirrored half is 14px.
  const TYPICAL = 29_000;
  // The cell's ceiling is the peak of the series it DRAWS, and it draws the
  // bucket means (trafficSeries): 37.0 MB/s. The peak of the _max column over
  // the same day was 101.4 MB/s, which is what it used to scale to.
  const TYPICAL_CEILING = 37_040_000;
  const HALF_PX = 14;

  it("draws the typical bucket at a readable height where linear cannot", () => {
    // Given the measured day
    const compressed = trafficScale(TYPICAL_CEILING);
    const proportional = linearScale(TYPICAL_CEILING);

    // Then proportionally it is a hundredth of a pixel -- the flat line this
    // change exists to fix, and no rounding of it is visible
    expect(proportional(TYPICAL) * HALF_PX).toBeLessThan(0.02);

    // And through the traffic scale it is five pixels of the fourteen
    expect(compressed(TYPICAL) * HALF_PX).toBeGreaterThan(4.5);
    expect(compressed(TYPICAL) * HALF_PX).toBeLessThan(5.5);
  });

  it("still puts the day's peak at the top of the box", () => {
    // Given the same day
    const scale = trafficScale(TYPICAL_CEILING);

    // Then compressing the quiet hours has not cost the spike its height:
    // "did this host spike" is still answered at a glance, which is what the
    // bucket peak used to be read for.
    expect(scale(TYPICAL_CEILING)).toBe(1);
    expect(scale(TYPICAL_CEILING / 10)).toBeGreaterThan(0.75);
  });
});
