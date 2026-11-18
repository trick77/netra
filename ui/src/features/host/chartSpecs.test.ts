import { describe, expect, it } from "vitest";
import type { MetricsResponse } from "../../lib/api";
import { ALL_SPECS, bandsFor, familiesFor } from "./chartSpecs";
import { trendColor } from "../../ui/Meter";

/**
 * An editorial guard, not a rendering one.
 *
 * `about` exists because a third of these panels are titled in the kernel's
 * words, and it stops being read the moment it turns into documentation. Two
 * failure modes are worth a test: a stub that says nothing, and a paragraph
 * nobody finishes. Both are cheap to introduce one spec at a time.
 */
describe("panel explanations", () => {
  const explained = ALL_SPECS.filter((spec) => spec.about !== undefined);

  it("are given to the panels whose titles are not enough", () => {
    // Not a count anyone should chase -- it is here so that deleting the lot,
    // or bolting one onto every spec, fails rather than passes quietly.
    expect(explained.length).toBeGreaterThan(20);
    expect(explained.length).toBeLessThan(ALL_SPECS.length);
  });

  it("say something, and stop", () => {
    for (const spec of explained) {
      const about = spec.about as string;
      expect(about.trim(), spec.slug).toBe(about);
      expect(about.length, spec.slug).toBeGreaterThan(40);
      // Three sentences is the brief. A fourth is a manual page.
      const sentences = about.split(". ").length;
      expect(sentences, spec.slug).toBeLessThanOrEqual(3);
      expect(about.endsWith("."), spec.slug).toBe(true);
    }
  });

  // The title is already on screen an inch away; repeating it is the first
  // sentence of every explanation nobody reads.
  it("do not open by restating the title", () => {
    for (const spec of explained) {
      const about = spec.about as string;
      expect(
        about.toLowerCase().startsWith(spec.title.toLowerCase()),
        spec.slug,
      ).toBe(false);
    }
  });
});

describe("the CPU lead chart", () => {
  const t = (i: number) => Date.parse(`2026-08-10T00:0${i}:00Z`);
  const window = {
    from: "2026-08-10T00:00:00Z",
    to: "2026-08-10T00:10:00Z",
  };
  const base = {
    step_s: 300,
    window,
    requested_window: window,
    warnings: [],
    truncated: false,
  };
  // A 5m tier: the host total with its peak, and two cores with theirs
  const host = {
    ...base,
    family: "host",
    tier: "5m",
    key_columns: [],
    columns: ["cpu_total_avg", "cpu_total_max"],
    series: [
      {
        key: {},
        points: [
          [t(0), 20, 40],
          [t(5), 30, 90],
        ],
      },
    ],
  } as unknown as MetricsResponse;
  const cores = {
    ...base,
    family: "cpu_core",
    tier: "5m",
    key_columns: ["core"],
    columns: ["busy_avg", "busy_max"],
    series: [
      {
        key: { core: "0" },
        points: [
          [t(0), 35, 60],
          [t(5), 10, 20],
        ],
      },
      {
        key: { core: "1" },
        points: [
          [t(0), 5, 10],
          [t(5), 50, 100],
        ],
      },
    ],
  } as unknown as MetricsResponse;
  const spec = ALL_SPECS.find((s) => s.slug === "host-cpu")!;

  it("draws the host's total with its peak, and the busiest core", () => {
    const bands = bandsFor(spec, host, {
      withPeakBand: true,
      extra: { cpuCore: cores },
    });
    expect(bands.map((b) => b.name)).toEqual(["total", "busiest core"]);
    expect(bands[0]!.values).toEqual([20, 30, null]);
    expect(bands[0]!.band).toEqual([40, 90, null]);
    // The per-bucket maximum over the cores, a different core each time
    expect(bands[1]!.values).toEqual([35, 50, null]);
    expect(bands[1]!.band).toEqual([60, 100, null]);
  });

  it("colours the total by its severity and the busiest core in ink", () => {
    const bands = bandsFor(spec, host, { extra: { cpuCore: cores } });
    expect(bands[0]!.color).toBe(trendColor(30));
    expect(bands[1]!.color).toBe("var(--ink-3)");
  });

  it("draws the total alone when the per-core family has not answered", () => {
    const bands = bandsFor(spec, host, { extra: { cpuCore: null } });
    expect(bands.map((b) => b.name)).toEqual(["total"]);
  });

  it("names the per-core family, so the page and the dialog fetch it", () => {
    expect(familiesFor(spec)).toEqual(["host", "cpu_core"]);
  });
});

describe("the colour of a panel's series", () => {
  const res = (columns: string[], row: number[]) =>
    ({
      family: "host",
      tier: "raw",
      step_s: 60,
      window: { from: "2026-08-10T00:00:00Z", to: "2026-08-10T00:01:00Z" },
      requested_window: {
        from: "2026-08-10T00:00:00Z",
        to: "2026-08-10T00:01:00Z",
      },
      warnings: [],
      key_columns: [],
      columns,
      series: [
        { key: {}, points: [[Date.parse("2026-08-10T00:00:00Z"), ...row]] },
      ],
      truncated: false,
    }) as unknown as MetricsResponse;
  const spec = (slug: string) => ALL_SPECS.find((s) => s.slug === slug)!;

  it("is ink for a lone series with no judgement behind it", () => {
    const bands = bandsFor(
      spec("context-switches"),
      res(["ctxt_per_s"], [1200]),
    );
    expect(bands.map((b) => b.color)).toEqual(["var(--ink-2)"]);
  });

  it("walks the six categorical hues for parts with a legend", () => {
    const bands = bandsFor(
      spec("memory-pressure"),
      res(["pgmajfault_per_s", "pswpin_per_s", "pswpout_per_s"], [1, 2, 3]),
    );
    expect(bands.map((b) => b.color)).toEqual([
      "var(--s1)",
      "var(--s2)",
      "var(--s3)",
    ]);
  });
});
