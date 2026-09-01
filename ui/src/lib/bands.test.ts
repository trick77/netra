import { describe, expect, it } from "vitest";
import {
  CONTAINER_MEM_SHADES,
  CPU_SHADES,
  containerBands,
  containerStackTotal,
  memoryBands,
  perCoreBands,
} from "./bands";
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
  // warm to cool AND bright to dim as the bands get more reclaimable, so the
  // assignment and the stacking order are one decision. This pins the sequence
  // that was measured (worst adjacent pair dE 11.0 on the #1b1b1a surface
  // across normal vision and simulated protan/deutan/tritan) so that
  // reordering the stack, or reassigning a hue, fails here rather than
  // silently shipping a pair nobody checked.
  it("paints the stack in the validated hue sequence", () => {
    expect(memoryBands(full).map((b) => b.color)).toEqual([
      "var(--mem-used)",
      "var(--mem-shared)",
      "var(--mem-arc)",
      "var(--mem-buffers)",
      "var(--mem-cached)",
    ]);
  });

  // The stack draws from --mem-* and nothing else. It used to borrow s-tokens,
  // and every borrowed one dragged the series ramp's brightness into a 32px
  // fleet cell -- --s7 was --accent's hue and --s8 the amber beside it, and
  // both had to be argued out one at a time. --mem-used is amber again, but it
  // is THIS chart's amber: dimmer than --s8 and tuned against the four bands
  // above it. Reaching back into the series ramp for a memory band fails here.
  it("draws the stack from the memory palette only", () => {
    const colors = memoryBands(full).map((b) => b.color);
    colors.forEach((c) => expect(c).toMatch(/^var\(--mem-/));
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
  // structure, and a single-hue light-to-DARK ramp fails the palette
  // validator's adjacent-lightness check outright -- 0.047 apart over eight
  // steps, and it read as a blob of blues. A four-step walk that WRAPS is
  // neither: it never subdivides, so every adjacent pair stays a full step
  // apart however many cores the host has.
  it("walks four shades and wraps rather than spreading a sweep", () => {
    const colors = perCoreBands(cores).map((b) => b.color);

    expect(colors).toEqual(CPU_SHADES);
    for (const c of colors) expect(c).toMatch(/^var\(--cpu-\d\)$/);
  });

  // Neighbours, not identity: on a host with more cores than shades the walk
  // repeats, and what must hold is that no band is drawn in the colour of the
  // band touching it.
  it("never repeats a shade between two touching bands", () => {
    const many = response({
      family: "cpu_core",
      key_columns: ["core"],
      columns: ["busy"],
      series: Array.from({ length: 32 }, (_, i) => ({
        key: { core: String(i) },
        points: [[t0, 50]],
      })),
    });

    const colors = perCoreBands(many).map((b) => b.color);
    expect(colors).toHaveLength(32);
    for (let i = 1; i < colors.length; i++) {
      expect(colors[i]).not.toBe(colors[i - 1]!);
    }
  });

  // A single-core host draws the shade a host with no per-core series draws
  // its cpu_total silhouette in, so the two are the same chart.
  it("colours a single-core host in the silhouette's own shade", () => {
    const one = response({
      family: "cpu_core",
      key_columns: ["core"],
      columns: ["busy"],
      series: [{ key: { core: "0" }, points: [[t0, 50]] }],
    });

    const bands = perCoreBands(one);
    expect(bands).toHaveLength(1);
    expect(bands[0]!.color).toBe("var(--cpu-1)");
  });

  it("has nothing to draw for a host that reported no cores", () => {
    expect(perCoreBands(response({ series: [] }))).toEqual([]);
    expect(perCoreBands(null)).toEqual([]);
  });
});

describe("containerBands", () => {
  const containers = (
    points: Record<string, ([number, ...(number | null)[]] | null)[]>,
  ) =>
    response({
      family: "container",
      key_columns: ["container"],
      columns: ["cpu_pct", "mem_used", "mem_limit"],
      series: Object.entries(points).map(([container, rows]) => ({
        key: { container },
        points: rows.filter((r) => r !== null),
      })),
    } as Partial<MetricsResponse>);

  const three = containers({
    "web/api": [
      [t0, 40, 100, null],
      [t0 + hour, 60, 120, null],
    ],
    "web/db": [
      [t0, 10, 400, null],
      [t0 + hour, 12, 420, null],
    ],
    "web/cache": [
      [t0, 5, 50, null],
      [t0 + hour, 6, 55, null],
    ],
  });

  it("draws one band per container, named by its key", () => {
    expect(containerBands(three, "cpu").map((b) => b.name)).toEqual([
      "web/api",
      "web/db",
      "web/cache",
    ]);
    expect(containerBands(three, "cpu")[0]!.values).toEqual([40, 60]);
    expect(containerBands(three, "mem")[1]!.values).toEqual([400, 420]);
  });

  // The stack total is the whole point of the panel, and stackBands drops
  // EVERY band at an index where any one of them is null. A container that
  // started inside the window is the ordinary case, not an edge one.
  it("counts a container that was not running as zero, not as a gap", () => {
    const late = containers({
      "web/api": [
        [t0, 40, 100, null],
        [t0 + hour, 60, 120, null],
      ],
      // Started an hour in: griddedValues leaves it null at t0.
      "web/new": [[t0 + hour, 20, 80, null]],
    });

    const bands = containerBands(late, "cpu");
    expect(bands[1]!.values).toEqual([0, 20]);
    expect(containerStackTotal(bands)).toEqual([40, 80]);
  });

  // ...and the opposite rule, which is the one absent-is-never-zero exists
  // for: a bucket NO container reported in is the host saying nothing, and a
  // stack drawn flat at zero there would claim Docker went idle.
  it("keeps a bucket no container reported in as a gap", () => {
    const silent = {
      ...containers({
        "web/api": [
          [t0, 40, 100, null],
          [t0 + 2 * hour, 60, 120, null],
        ],
        "web/db": [
          [t0, 10, 400, null],
          [t0 + 2 * hour, 12, 420, null],
        ],
      }),
      // Three buckets, so the middle one can be the silent one.
      window: { from: "2026-08-10T00:00:00Z", to: "2026-08-10T03:00:00Z" },
      requested_window: {
        from: "2026-08-10T00:00:00Z",
        to: "2026-08-10T03:00:00Z",
      },
    };

    const bands = containerBands(silent, "cpu");
    expect(bands[0]!.values).toEqual([40, null, 60]);
    expect(bands[1]!.values).toEqual([10, null, 12]);
    expect(containerStackTotal(bands)).toEqual([50, null, 72]);
  });

  // A wrapping walk, exactly as the per-core stack: no adjacent pair shares a
  // shade however many containers the host runs.
  it("walks its shades so no two neighbours share one", () => {
    const many = containers(
      Object.fromEntries(
        Array.from({ length: 11 }, (_, i) => [
          `stack/svc-${i}`,
          [[t0, i, i * 10, null]],
        ]),
      ),
    );

    const cpu = containerBands(many, "cpu").map((b) => b.color);
    expect(cpu).toHaveLength(11);
    expect(cpu[0]).toBe(CPU_SHADES[0]);
    expect(cpu[4]).toBe(CPU_SHADES[0]);
    for (let i = 1; i < cpu.length; i++) {
      expect(cpu[i]).not.toBe(cpu[i - 1]!);
    }

    // Memory walks its own family, so a row's blue CPU cell and green memory
    // cell keep their pairing in the panels above the list.
    expect(containerBands(many, "mem")[0]!.color).toBe(CONTAINER_MEM_SHADES[0]);
  });

  // Band ORDER is the response's. The page polls, and a stack re-sorted by
  // size on every refresh would shuffle under the reader.
  it("keeps the response's order rather than sorting by size", () => {
    expect(containerBands(three, "cpu").map((b) => b.name)).toEqual([
      "web/api",
      "web/db",
      "web/cache",
    ]);
  });

  it("has nothing to draw for a host running no containers", () => {
    expect(containerBands(response({ series: [] }), "cpu")).toEqual([]);
    expect(containerBands(null, "mem")).toEqual([]);
    expect(containerStackTotal([])).toEqual([]);
  });
});
