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

  // The stacking order is the whole claim this function makes about memory,
  // and it lives here rather than only in the fleet-trends test two layers
  // away. Bottom to top, in increasing order of how readily the kernel can
  // take the bytes back: shared is Shmem, unreclaimable under pressure, so it
  // stacks with used rather than above the caches. Drawing it topmost, against
  // the free gap, told the reader it was the first thing the host would hand
  // back.
  it("stacks the unreclaimable pages below the caches", () => {
    expect(memoryBands(full).map((b) => b.name)).toEqual([
      "used",
      "shared",
      "ARC",
      "buffers",
      "cached",
    ]);
  });

  // The colours are not decoration and neither is their ORDER: the ramp runs
  // from full chroma to neutral as the bands get more reclaimable, so the
  // assignment and the stacking order are one decision. This pins the
  // sequence that was measured (s4 -> s3 -> s6 -> the two neutrals, worst
  // adjacent pair dE 15.3 on the #1b1b1a surface across normal vision and
  // simulated protan/deutan/tritan) so that reordering the stack, or
  // reassigning a hue, fails here rather than silently shipping a pair nobody
  // checked.
  it("paints the stack in the validated hue sequence", () => {
    expect(memoryBands(full).map((b) => b.color)).toEqual([
      "var(--s4)",
      "var(--s3)",
      "var(--s6)",
      "var(--s-neutral)",
      "var(--s-neutral-2)",
    ]);
  });

  // --s7 is --accent's hue and --st-serious's neighbour, and --s8 is the amber
  // beside it; between them they carried ARC and cached until the palette
  // reserved warm for attention and gave page cache the neutrals. These bands
  // are drawn in a 32px fleet cell with a severity badge two columns away,
  // which is the case the rule exists for -- a large legended chart may still
  // use either.
  it("keeps attention's warm hues out of the memory stack", () => {
    const colors = memoryBands(full).map((b) => b.color);
    expect(colors).not.toContain("var(--s7)");
    expect(colors).not.toContain("var(--s8)");
  });

  // Dropping a band a host does not have must not shuffle the ones it does:
  // a reader moving between a ZFS host and a non-ZFS one reads the same
  // gradient, minus one layer.
  it("keeps the order when a host has no ARC to draw", () => {
    const noZfs = response({
      columns: [
        "mem_total",
        "mem_free",
        "mem_buffers",
        "mem_cached",
        "mem_shared",
      ],
      series: [{ key: {}, points: [[t0, 1000, 200, 30, 100, 50]] }],
    });

    expect(memoryBands(noZfs).map((b) => b.name)).toEqual([
      "used",
      "shared",
      "buffers",
      "cached",
    ]);
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

  // An upgraded hub carries mem_free on the family for EVERY host, so the
  // column check passed even for a host still running an agent that never
  // sent it. All five bands then came back all-null, the trailing filter
  // dropped every one of them, and the page said "reported no memory
  // samples" about a host reporting perfectly well.
  it("falls back to mem_used when the column is carried but never filled", () => {
    const nulled = response({
      columns: ["mem_total", "mem_free", "mem_used"],
      series: [
        {
          key: {},
          points: [
            [t0, null, null, 400],
            [t0 + hour, null, null, 450],
          ],
        },
      ],
    });

    const bands = memoryBands(nulled);

    expect(bands.map((b) => b.name)).toEqual(["used"]);
    expect(bands[0]!.values).toEqual([400, 450]);
  });

  // The same fallback the missing-column case has always taken. Absent data
  // and an absent column are the same fact about what can be drawn.
  it("falls back to mem_used when the tier does not carry mem_free at all", () => {
    const noColumn = response({
      columns: ["mem_total", "mem_used"],
      series: [{ key: {}, points: [[t0, 1000, 400]] }],
    });

    expect(memoryBands(noColumn).map((b) => b.name)).toEqual(["used"]);
  });

  // A kernel with no SReclaimable line is supported (memory.go,
  // TestMemoryAbsentShmemLeavesCachedWhole) and reports it NULL for every
  // bucket. add() returned null wherever EITHER input was null, so one
  // all-null optional poisoned the whole cached band; the band was then
  // dropped and counted as 0 in the remainder, moving several GB of page
  // cache into "used" and reporting it as resident.
  it("keeps the cached band whole when mem_sreclaimable is all null", () => {
    const noSlab = response({
      columns: [
        "mem_total",
        "mem_free",
        "mem_buffers",
        "mem_cached",
        "mem_sreclaimable",
        "mem_shared",
      ],
      series: [
        {
          key: {},
          points: [
            [t0, 1000, 200, 30, 100, null, 50],
            [t0 + hour, 1000, 200, 30, 100, null, 50],
          ],
        },
      ],
    });

    const bands = memoryBands(noSlab);
    const cached = bands.find((b) => b.name === "cached");
    const used = bands.find((b) => b.name === "used")!;

    // cached survives at mem_cached's own value -- the absent slab
    // contributes nothing rather than deleting the band.
    expect(cached).toBeDefined();
    expect(cached!.values).toEqual([100, 100]);
    // And the remainder is not inflated by the 100 bytes the dropped band
    // used to donate to it: 1000 - (200 + 30 + 100 + 50).
    expect(used.values[0]).toBe(620);
  });

  // Both inputs null IS still null: neither said anything about that bucket,
  // so there is no total to state. Only a ONE-sided null is a partial sum.
  it("leaves a bucket null where both cached inputs are null", () => {
    const gap = response({
      columns: ["mem_total", "mem_free", "mem_cached", "mem_sreclaimable"],
      series: [
        {
          key: {},
          points: [
            [t0, 1000, 200, null, null],
            [t0 + hour, 1000, 200, 100, null],
          ],
        },
      ],
    });

    const cached = memoryBands(gap).find((b) => b.name === "cached")!;

    expect(cached.values[0]).toBeNull();
    expect(cached.values[1]).toBe(100);
  });

  // The third member of the family, and the one that decides whether the
  // fallback is a fix or a new bug: a host whose agent was down for the whole
  // window reports NOTHING, mem_used included. Returning a lone band of nulls
  // draws an empty chart with no explanation, where naming the absence is the
  // whole point -- absent must never render as a fact.
  it("draws nothing at all for a host that reported no memory whatsoever", () => {
    const silent = response({
      columns: ["mem_total", "mem_free", "mem_used"],
      series: [
        {
          key: {},
          points: [
            [t0, null, null, null],
            [t0 + hour, null, null, null],
          ],
        },
      ],
    });

    expect(memoryBands(silent)).toEqual([]);
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

  // The fleet list draws a 4-core and a 32-core host in the same 0-100 cell,
  // so there the stack has to top out at cpu_total -- the mean across cores.
  it("normalises by core count so the stack tops out at cpu_total", () => {
    const bands = perCoreBands(cores, { normalise: true });
    const top = bands.reduce((sum, b) => sum + (b.values[0] as number), 0);

    expect(bands).toHaveLength(4);
    expect(top).toBe(25);
    expect(bands[0]!.values[0]).toBe(20);
  });

  // The host page draws the same cores raw, because there the numbers matter
  // more than cross-host comparability: a core at 80% must read 80, not its
  // 20-point share of the host total. The chart drawing it hides its y axis,
  // since the stack then runs to cores x 100.
  it("keeps each core's real utilisation when it is not normalised", () => {
    const bands = perCoreBands(cores);

    expect(bands[0]!.values[0]).toBe(80);
    expect(bands[1]!.values[0]).toBe(20);
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
    expect(bands[0]!.color).toMatch(
      /^hsl\(\d+(\.\d+)? var\(--chart-saturation\) var\(--chart-lightness\)\)$/,
    );
  });

  it("has nothing to draw for a host that reported no cores", () => {
    expect(perCoreBands(response({ series: [] }))).toEqual([]);
    expect(perCoreBands(null)).toEqual([]);
  });
});
