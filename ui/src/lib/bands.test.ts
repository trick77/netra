import { describe, expect, it } from "vitest";
import { memoryBands, perCoreBands } from "./bands";
import type { MetricsResponse } from "./api";

const t0 = Date.parse("2026-08-10T00:00:00Z");
const hour = 3_600_000;

function response(over: Partial<MetricsResponse>): MetricsResponse {
  return {
    family: "host",
    tier: "raw",
    step_s: 3600,
    window: { from: "2026-08-10T00:00:00Z", to: "2026-08-10T02:00:00Z" },
    requested_window: {
      from: "2026-08-10T00:00:00Z",
      to: "2026-08-10T02:00:00Z",
    },
    warnings: [],
    key_columns: [],
    columns: [],
    series: [],
    truncated: false,
    ...over,
  } as MetricsResponse;
}

describe("memoryBands", () => {
  const full = response({
    columns: [
      "mem_total",
      "mem_free",
      "mem_buffers",
      "mem_cached",
      "mem_sreclaimable",
      "mem_shared",
      "mem_zfs_arc",
    ],
    series: [
      {
        key: {},
        points: [
          [t0, 1000, 200, 30, 100, 20, 50, 100],
          [t0 + hour, 1000, 100, 30, 100, 20, 50, 200],
        ],
      },
    ],
  });

  // The reason this module exists. mem_used is MemTotal - MemAvailable, so it
  // already contains the ARC and the unreclaimable shmem pages; stacking them
  // on top of it draws the same bytes twice and can push the stack past
  // mem_total. Deriving used as the remainder cannot overflow by construction.
  it("derives used as the remainder so the stack never exceeds mem_total", () => {
    const bands = memoryBands(full);
    const used = bands.find((b) => b.name === "used")!;

    // 1000 - (200 free + 30 buffers + 120 cached+slab + 50 shared + 100 arc)
    expect(used.values[0]).toBe(500);
    // The second bucket frees 100 bytes and grows the ARC by 100, so used is
    // unchanged: memory moved between bands rather than appearing.
    expect(used.values[1]).toBe(500);

    for (let i = 0; i < 2; i++) {
      const stack = bands.reduce((sum, b) => sum + (b.values[i] as number), 0);
      const free = i === 0 ? 200 : 100;
      expect(stack).toBe(1000 - free);
      expect(stack).toBeLessThanOrEqual(1000);
    }
  });

  // Free is the gap to the top, never a band: stacking it makes every host
  // look full, which is the one reading these charts exist to avoid.
  it("never draws free as a band", () => {
    expect(memoryBands(full).map((b) => b.name)).not.toContain("free");
  });

  it("folds reclaimable slab into cached rather than inventing a sixth band", () => {
    const cached = memoryBands(full).find((b) => b.name === "cached")!;

    expect(cached.values[0]).toBe(120);
  });

  // A null anywhere makes the remainder unknowable rather than smaller.
  // Drawing zero would claim the host had no memory in use at that instant.
  it("keeps a gap a gap instead of subtracting into a false zero", () => {
    const gappy = response({
      columns: ["mem_total", "mem_free", "mem_buffers"],
      series: [
        {
          key: {},
          points: [
            [t0, 1000, 200, 30],
            [t0 + hour, 1000, null, 30],
          ],
        },
      ],
    });

    const used = memoryBands(gappy).find((b) => b.name === "used")!;
    expect(used.values[0]).toBe(770);
    expect(used.values[1]).toBeNull();
  });

  // Older data, or a tier that predates the columns, cannot support a
  // partition. One true band beats five wrong ones -- and silently letting
  // used absorb the whole free pool would draw every host as nearly full.
  it("falls back to a single used band when mem_free is absent", () => {
    const old = response({
      columns: ["mem_used", "mem_total"],
      series: [{ key: {}, points: [[t0, 400, 1000]] }],
    });

    const bands = memoryBands(old);
    expect(bands).toHaveLength(1);
    expect(bands[0]!.name).toBe("used");
    expect(bands[0]!.values[0]).toBe(400);
  });

  // The column exists on the family whether or not a given host uses ZFS, so
  // a machine without it reports mem_zfs_arc as NULL in every bucket. Treating
  // that as "unknown" rather than "none" made the remainder null throughout and
  // silently deleted the used band from every non-ZFS host -- three of the four
  // simulator archetypes, and it looked exactly like a host that had reported
  // nothing.
  it("treats an absent subsystem as none, not as unknown", () => {
    const noZfs = response({
      columns: [
        "mem_total",
        "mem_free",
        "mem_buffers",
        "mem_cached",
        "mem_shared",
        "mem_zfs_arc",
      ],
      series: [
        {
          key: {},
          points: [[t0, 1000, 200, 30, 100, 50, null]],
        },
      ],
    });

    const bands = memoryBands(noZfs);
    const used = bands.find((b) => b.name === "used");

    expect(used).toBeDefined();
    expect(used!.values[0]).toBe(620);
    expect(bands.map((b) => b.name)).not.toContain("ARC");
  });

  it("drops the ARC band on a host that has no ZFS", () => {
    const noZfs = response({
      columns: ["mem_total", "mem_free", "mem_buffers"],
      series: [{ key: {}, points: [[t0, 1000, 200, 30]] }],
    });

    expect(memoryBands(noZfs).map((b) => b.name)).not.toContain("ARC");
  });
});

describe("perCoreBands", () => {
  const cores = response({
    family: "cpu_core",
    key_columns: ["core"],
    columns: ["busy"],
    series: [
      { key: { core: "0" }, points: [[t0, 80]] },
      { key: { core: "1" }, points: [[t0, 20]] },
      { key: { core: "2" }, points: [[t0, 0]] },
      { key: { core: "3" }, points: [[t0, 0]] },
    ],
  });

  // Each core reports 0-100, so a raw stack of N cores runs to N x 100 and
  // overflows a chart whose ceiling is 100. Dividing by N makes the top of
  // the stack the mean -- which is cpu_total -- so the chart agrees with the
  // number the meter shows, and hosts with different core counts stay
  // comparable in a list.
  it("normalises by core count so the stack tops out at cpu_total", () => {
    const bands = perCoreBands(cores);
    const top = bands.reduce((sum, b) => sum + (b.values[0] as number), 0);

    expect(bands).toHaveLength(4);
    expect(top).toBe(25);
    expect(bands[0]!.values[0]).toBe(20);
  });

  it("names each band after the core key, not its position", () => {
    expect(perCoreBands(cores).map((b) => b.name)).toEqual([
      "core 0",
      "core 1",
      "core 2",
      "core 3",
    ]);
  });

  // Colour cannot carry identity across thirty-two bands; all it can do is
  // keep neighbours apart. Two alternating tokens gave the stack no internal
  // structure, and a single-hue light-to-dark ramp fails the palette
  // validator's adjacent-lightness check outright -- 0.047 apart over eight
  // steps, and it read as a blob of blues. Hue is the only channel with the
  // range, so every band gets its own.
  it("gives every core a distinct hue rather than a repeating pair", () => {
    const colors = perCoreBands(cores).map((b) => b.color);

    expect(new Set(colors).size).toBe(colors.length);
    for (const c of colors) expect(c).toMatch(/^hsl\(/);
  });

  // The sweep has to subdivide to fit the host: 32 cores land about 9 degrees
  // apart, 4 cores about 95. Either way no two neighbours share a hue, which
  // is the whole job.
  it("spreads the sweep across however many cores the host has", () => {
    const hueOf = (c: string) => Number(/^hsl\((\d+(?:\.\d+)?)/.exec(c)![1]);
    const four = perCoreBands(cores).map((b) => hueOf(b.color));

    expect(new Set(four).size).toBe(4);
    // Adjacent cores are far enough apart to tell without a legend.
    for (let i = 1; i < four.length; i++) {
      const gap = Math.abs(four[i]! - four[i - 1]!);
      expect(Math.min(gap, 360 - gap)).toBeGreaterThan(20);
    }
  });

  // A single-core host must not divide by zero working out its position in
  // the sweep.
  it("colours a single-core host without dividing by zero", () => {
    const one = response({
      family: "cpu_core",
      key_columns: ["core"],
      columns: ["busy"],
      series: [{ key: { core: "0" }, points: [[t0, 50]] }],
    });

    const bands = perCoreBands(one);
    expect(bands).toHaveLength(1);
    expect(bands[0]!.color).toMatch(/^hsl\(\d+(\.\d+)? 65% 50%\)$/);
  });

  it("has nothing to draw for a host that reported no cores", () => {
    expect(perCoreBands(response({ series: [] }))).toEqual([]);
    expect(perCoreBands(null)).toEqual([]);
  });
});
