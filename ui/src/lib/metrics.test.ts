import { describe, expect, it } from "vitest";
import {
  carriesColumn,
  column,
  counterDeltas,
  counterIncrease,
  griddedValues,
  hasGaps,
  optionalValues,
  peakBase,
  ratioValues,
  reduceToColumns,
  seriesCells,
  seriesTimestamps,
  seriesValues,
  SeriesIndexError,
  windowNotice,
} from "./metrics";

const raw = {
  family: "cpu_core",
  tier: "raw",
  step_s: 60,
  window: { from: "2026-08-09T14:00:00Z", to: "2026-08-10T14:00:00Z" },
  requested_window: {
    from: "2026-08-09T14:00:00Z",
    to: "2026-08-10T14:00:00Z",
  },
  warnings: [],
  key_columns: ["core"],
  columns: ["busy"],
  series: [
    {
      key: { core: "0" },
      points: [
        [1, 4.1],
        [2, null],
        [3, 5.0],
      ],
    },
  ],
  truncated: false,
} as const;

const fiveMin = {
  ...raw,
  tier: "5m",
  step_s: 300,
  columns: ["busy_avg", "busy_max"],
} as const;

describe("metrics", () => {
  // Column names differ per tier BY CONSTRUCTION -- that is the guarantee
  // that a client cannot silently read an average as a raw value.
  it("resolves a base column name per tier", () => {
    expect(column(raw as never, "busy")).toBe(0);
    expect(column(fiveMin as never, "busy")).toBe(0); // busy_avg
  });

  it("throws a named error when the tier has no such column", () => {
    expect(() => column(raw as never, "nonexistent")).toThrowError(
      /nonexistent/,
    );
  });

  // A null inside the window means the host reported nothing. It must survive
  // to the chart, which breaks the line. Coercing it to 0 or dropping the
  // point both turn "agent was down" into a measurement.
  it("preserves nulls rather than coercing them", () => {
    expect(seriesValues(raw as never, 0, "busy")).toEqual([4.1, null, 5.0]);
  });

  describe("seriesTimestamps", () => {
    it("extracts point[0] as epoch milliseconds", () => {
      const withRealTimestamps = {
        ...raw,
        series: [
          {
            key: { core: "0" },
            points: [
              [1_754_755_200_000, 4.1],
              [1_754_755_260_000, null],
            ],
          },
        ],
      };
      expect(seriesTimestamps(withRealTimestamps as never, 0)).toEqual([
        1_754_755_200_000, 1_754_755_260_000,
      ]);
    });

    it("throws a named error for an out-of-range series index", () => {
      expect(() => seriesTimestamps(raw as never, 5)).toThrowError(
        SeriesIndexError,
      );
      expect(() => seriesTimestamps(raw as never, 5)).toThrowError(/5/);
    });
  });

  describe("seriesCells bounds checking", () => {
    it("throws a named error for an out-of-range series index instead of a bare TypeError", () => {
      expect(() => seriesCells(raw as never, 5, "busy")).toThrowError(
        SeriesIndexError,
      );
    });
  });

  // A window shorter than the one asked for is not worth a sentence: the
  // chart starts where the data starts, over a dated axis, which says it
  // better than prose that named a storage tier the reader never picked.
  it("says nothing about a window the server clamped", () => {
    const clamped = {
      ...raw,
      window: { from: "2026-08-08T14:00:00Z", to: "2026-08-10T14:00:00Z" },
      requested_window: {
        from: "2026-05-12T14:00:00Z",
        to: "2026-08-10T14:00:00Z",
      },
    };
    expect(windowNotice(clamped as never)).toBeNull();
    expect(windowNotice(raw as never)).toBeNull();
  });

  describe("hasGaps", () => {
    it("is false when every value is a number", () => {
      expect(hasGaps([1, 2, 3])).toBe(false);
    });

    it("is true when any value is null", () => {
      expect(hasGaps([1, null, 3])).toBe(true);
    });

    it("is false for an empty series", () => {
      expect(hasGaps([])).toBe(false);
    });
  });

  describe("column resolution order", () => {
    it("tries the exact base name before the _avg/_max suffixed forms", () => {
      const both = { ...raw, columns: ["busy", "busy_avg"] } as const;
      expect(column(both as never, "busy")).toBe(0);
    });

    it("falls back to _max when only that suffix exists", () => {
      const maxOnly = { ...raw, tier: "1h", columns: ["busy_max"] } as const;
      expect(column(maxOnly as never, "busy")).toBe(0);
    });

    it("lists the available columns in the thrown error", () => {
      expect(() => column(fiveMin as never, "nonexistent")).toThrowError(
        /busy_avg.*busy_max|busy_max.*busy_avg/s,
      );
    });
  });

  describe("windowNotice edge differences", () => {
    // Both edges, at both kinds of tier, and none of them says anything.
    //
    // This is the whole change: the trailing clamp at a rolled-up tier, the
    // leading retention clamp, and the future-`to` clamp that fires even at
    // raw all used to produce a sentence here. They were the only prose this
    // app put under a chart about that chart, and it was written in the
    // storage engine's vocabulary.
    it.each([
      [
        "a trailing lag clamp at a rolled-up tier",
        {
          ...fiveMin,
          window: { from: "2026-08-09T14:00:00Z", to: "2026-08-10T13:50:00Z" },
          requested_window: {
            from: "2026-08-09T14:00:00Z",
            to: "2026-08-10T14:00:00Z",
          },
        },
      ],
      [
        "a future-to clamp at raw",
        {
          ...raw,
          window: { from: "2026-08-09T14:00:00Z", to: "2026-08-10T14:00:00Z" },
          requested_window: {
            from: "2026-08-09T14:00:00Z",
            to: "2026-08-11T00:00:00Z",
          },
        },
      ],
    ])("says nothing about %s", (_name, res) => {
      expect(windowNotice(res as never)).toBeNull();
    });

    // What the server still warns about -- a column this tier does not
    // carry, a truncated result -- is passed through word for word rather
    // than re-derived. Only the hub knows which of them fired.
    it("surfaces server warnings verbatim", () => {
      const serverWarned = {
        ...raw,
        warnings: [
          "column 'cpu_steal' is not available at this resolution and was dropped",
          "the result reached the 200000-point limit and is truncated; narrow the window or ask for fewer columns",
        ],
      };
      const notice = windowNotice(serverWarned as never);
      expect(notice).toContain(
        "column 'cpu_steal' is not available at this resolution and was dropped",
      );
      expect(notice).toContain("the result reached the 200000-point limit");
    });

    // Truncation is a point-limit cut -- exactly the "chart is silently
    // wrong" failure this module exists to catch -- and must never be
    // dropped from the notice. internal/hub/read/metrics.go:146-150 ALWAYS
    // appends its own truncation warning into res.warnings on the code path
    // that sets res.truncated, so this fixture -- truncated: true WITH the
    // server's own truncation warning present -- is what a real hub
    // response looks like. It must appear exactly once, not twice: the
    // client passes warnings through verbatim and must not also append its
    // own copy of a fact the server already stated.
    it("reports a truncated result exactly once, using the server's own warning", () => {
      const truncated = {
        ...raw,
        warnings: [
          "the result reached the 200000-point limit and is truncated; narrow the window or ask for fewer columns",
        ],
        truncated: true,
      };
      const notice = windowNotice(truncated as never);
      expect(notice).toMatch(/truncat/i);
      expect(notice?.match(/truncat/gi)?.length).toBe(1);
    });

    // A response with truncated: true and NO warning mentioning it cannot
    // happen from the real hub (metrics.go always pairs the two), but is
    // kept as a defensive fallback: a hand-built response, a future server
    // bug, or a client-side mock should still surface truncation rather than
    // silently rendering an incomplete series as complete.
    it("still reports truncation defensively when no warning mentions it", () => {
      const truncated = { ...raw, warnings: [], truncated: true };
      expect(windowNotice(truncated as never)).toMatch(/truncat/i);
    });

    it("combines a passed-through server warning with the server's truncation warning, without duplicating it", () => {
      const both = {
        ...raw,
        window: { from: "2026-08-08T14:00:00Z", to: "2026-08-10T14:00:00Z" },
        requested_window: {
          from: "2026-05-12T14:00:00Z",
          to: "2026-08-10T14:00:00Z",
        },
        warnings: [
          "column 'cpu_steal' is not available at this resolution and was dropped",
          "the result reached the 200000-point limit and is truncated; narrow the window or ask for fewer columns",
        ],
        truncated: true,
      };
      const notice = windowNotice(both as never);
      expect(notice).toMatch(/cpu_steal/);
      expect(notice).toMatch(/truncat/i);
      expect(notice?.match(/truncat/gi)?.length).toBe(1);
    });
  });

  // internal/hub/read/metrics.go's Series.Points is [][]any, not [][]number:
  // family=collector is the family that proves a column is not always
  // numeric -- ok is a boolean and error_code is a string, sitting beside a
  // numeric duration_ms in the same row.
  describe("non-numeric columns (family=collector)", () => {
    const collector = {
      family: "collector",
      tier: "raw",
      step_s: 60,
      window: { from: "2026-08-09T14:00:00Z", to: "2026-08-10T14:00:00Z" },
      requested_window: {
        from: "2026-08-09T14:00:00Z",
        to: "2026-08-10T14:00:00Z",
      },
      warnings: [],
      key_columns: ["collector"],
      columns: ["ok", "error_code", "duration_ms"],
      series: [
        {
          key: { collector: "smart" },
          points: [
            [1, true, null, 12],
            [2, false, "conn_refused", null],
          ],
        },
      ],
      truncated: false,
    } as const;

    it("seriesValues() rejects a boolean cell rather than passing it through as a number", () => {
      expect(() => seriesValues(collector as never, 0, "ok")).toThrowError(
        /ok/,
      );
      expect(() => seriesValues(collector as never, 0, "ok")).toThrowError(
        /boolean/,
      );
    });

    it("seriesValues() rejects a string cell rather than passing it through as a number", () => {
      expect(() =>
        seriesValues(collector as never, 0, "error_code"),
      ).toThrowError(/error_code/);
      expect(() =>
        seriesValues(collector as never, 0, "error_code"),
      ).toThrowError(/string/);
    });

    it("seriesCells() returns the raw boolean and string cells intact, nulls included", () => {
      expect(seriesCells(collector as never, 0, "ok")).toEqual([true, false]);
      expect(seriesCells(collector as never, 0, "error_code")).toEqual([
        null,
        "conn_refused",
      ]);
    });

    it("seriesValues() still works normally for a genuinely numeric column on the same response", () => {
      expect(seriesValues(collector as never, 0, "duration_ms")).toEqual([
        12,
        null,
      ]);
    });
  });
});

describe("optionalValues", () => {
  // A page rendering N panels across families cannot know which columns the
  // answering tier carries. seriesValues throws for an absent one, and there
  // is no error boundary in this app, so one missing column took the whole
  // render down: a blank page instead of one panel reading "not collected".
  it("returns an empty series for a column this tier does not carry", () => {
    expect(() => seriesValues(raw as never, 0, "iowait")).toThrow();
    expect(optionalValues(raw as never, 0, "iowait")).toEqual([]);
  });

  // [] and [null, null] are different facts: the first says the tier does
  // not carry the column, the second says the host reported nothing. They
  // draw differently -- a not-collected panel versus a hole in a line.
  it("still reports the host's own gaps as nulls when the column is present", () => {
    expect(optionalValues(raw as never, 0, "busy")).toEqual([4.1, null, 5.0]);
  });

  it("treats a null response as no data rather than throwing", () => {
    expect(optionalValues(null, 0, "busy")).toEqual([]);
  });

  it("returns an empty series for a series index that does not exist", () => {
    expect(optionalValues(raw as never, 9, "busy")).toEqual([]);
  });
});

describe("ratioValues", () => {
  // collector_samples_5m: the counts are kept and `ok` is dropped, so this
  // is the only way the availability panel draws at any range but 1h.
  const counts = {
    ...raw,
    family: "collector",
    tier: "5m",
    step_s: 300,
    window: { from: "2026-08-10T00:00:00Z", to: "2026-08-10T00:15:00Z" },
    requested_window: {
      from: "2026-08-10T00:00:00Z",
      to: "2026-08-10T00:15:00Z",
    },
    key_columns: ["collector"],
    columns: ["sample_count", "failure_count"],
    series: [
      {
        key: { collector: "smart" },
        points: [
          [Date.parse("2026-08-10T00:00:00Z"), 5, 1],
          [Date.parse("2026-08-10T00:05:00Z"), 5, 0],
          // No scrapes at all in the bucket, which is not a total failure.
          [Date.parse("2026-08-10T00:10:00Z"), 0, 0],
        ],
      },
    ],
  } as const;

  it("divides one column by another, on the window's grid", () => {
    expect(
      ratioValues(counts as never, 0, "failure_count", "sample_count"),
    ).toEqual([0.2, 0, null]);
  });

  // The same [] as optionalValues, and for the same reason: at raw the two
  // counts do not exist, and "this tier does not carry it" has to stay
  // tellable apart from "the host reported nothing".
  it("returns an empty series when either column is absent from this tier", () => {
    expect(
      ratioValues(raw as never, 0, "failure_count", "sample_count"),
    ).toEqual([]);
    expect(ratioValues(counts as never, 0, "failure_count", "busy")).toEqual(
      [],
    );
  });

  it("treats a null response and an unknown series index as no data", () => {
    expect(ratioValues(null, 0, "failure_count", "sample_count")).toEqual([]);
    expect(
      ratioValues(counts as never, 9, "failure_count", "sample_count"),
    ).toEqual([]);
  });
});

describe("column and the rollup suffixes", () => {
  // The schema rolls `free` up as min(free) AS free_min and only as that
  // (0001_init.sql). Resolving avg/max alone made the column exist at raw
  // and disappear at 5m and 1h -- a root filesystem at 97% reading "— free"
  // with no warning raised, and the same host turning critical the moment
  // someone changed the range to 1h.
  it("resolves a column the schema only rolls up as a minimum", () => {
    const fs = {
      ...raw,
      tier: "5m",
      columns: ["used_avg", "used_max", "free_min"],
    };

    expect(column(fs as never, "free")).toBe(2);
  });

  // The three resolvers must agree on the candidate list. They briefly did
  // not -- column() learned _min while optionalValues did not -- and the
  // result was that carriesColumn said "free is here", the meter drew, and
  // the values behind it came back empty: every host's disk went blank
  // while the code deciding whether to draw it insisted the column existed.
  it("resolves the same names in column, optionalValues and carriesColumn", () => {
    const fs = {
      ...raw,
      tier: "5m",
      columns: ["used_avg", "free_min"],
      series: [{ key: {}, points: [[1, 10, 90]] }],
    };

    expect(carriesColumn(fs as never, "free")).toBe(true);
    expect(optionalValues(fs as never, 0, "free")).toEqual([90]);
    expect(column(fs as never, "free")).toBe(1);
  });
});

describe("peakBase", () => {
  // The whole point of the helper: column() prefers _avg, and for traffic
  // that average is what flattened every burst. Asking for the peak has to
  // return a name that column() resolves on its EXACT-name branch, or it
  // would fall straight back into the _avg-preferring order.
  it("when a rolled_up tier carries both peers_then resolves the max", () => {
    const net = {
      ...raw,
      tier: "5m",
      columns: ["rx_bytes_avg", "rx_bytes_max"],
      series: [{ key: {}, points: [[1, 10, 90]] }],
    };

    expect(peakBase(net as never, "rx_bytes")).toBe("rx_bytes_max");
    expect(column(net as never, peakBase(net as never, "rx_bytes"))).toBe(1);
    // The reading actually changes -- 90, the peak, not 10, the mean. A test
    // that only asserted the NAME would still pass if the column resolved
    // back to the average behind it.
    expect(
      seriesValues(net as never, 0, peakBase(net as never, "rx_bytes")),
    ).toEqual([90]);
  });

  // The raw table has no _max peer at all, because at raw resolution the
  // sample IS the peak. Without the fallback this threw UnknownColumnError
  // and took the whole render down -- there is no error boundary in this
  // app -- so the 1h range would have been a blank page rather than a
  // sharper chart.
  it("when the tier has no max peer_then falls back to the base name", () => {
    const net = {
      ...raw,
      tier: "raw",
      columns: ["rx_bytes"],
      series: [{ key: {}, points: [[1, 42]] }],
    };

    expect(peakBase(net as never, "rx_bytes")).toBe("rx_bytes");
    expect(
      seriesValues(net as never, 0, peakBase(net as never, "rx_bytes")),
    ).toEqual([42]);
  });

  // Same reason carriesColumn takes `== null`: these names are resolved
  // during render from a response prop that may not have arrived yet, and
  // reading .columns off undefined is a blank page.
  it("when the response is absent_then falls back rather than throwing", () => {
    expect(peakBase(null, "rx_bytes")).toBe("rx_bytes");
    expect(peakBase(undefined, "rx_bytes")).toBe("rx_bytes");
  });
});

describe("seriesOnGrid", () => {
  // The read API emits only the rows that exist, so an outage is a MISSING
  // point, not a null cell -- and the geometry breaks a line only on an
  // explicit null. Without this, a host down for three hours inside a 24h
  // window came back short and drew one unbroken line across the gap.
  it("inserts a null for every bucket the response has no row for", () => {
    const res = {
      ...raw,
      step_s: 3600,
      window: { from: "2026-08-10T00:00:00Z", to: "2026-08-10T05:00:00Z" },
      columns: ["busy"],
      series: [
        {
          key: {},
          points: [
            [Date.parse("2026-08-10T00:00:00Z"), 1],
            [Date.parse("2026-08-10T01:00:00Z"), 2],
            // 02:00 and 03:00 missing -- the host was down
            [Date.parse("2026-08-10T04:00:00Z"), 5],
          ],
        },
      ],
    };

    expect(griddedValues(res as never, 0, "busy")).toEqual([
      1,
      2,
      null,
      null,
      5,
    ]);
  });

  // Two series of different lengths in one panel were each spread across
  // their own length, so they were misaligned in time against each other
  // inside a single chart.
  it("puts two ragged series on the same grid, so they line up", () => {
    const res = {
      ...raw,
      step_s: 3600,
      window: { from: "2026-08-10T00:00:00Z", to: "2026-08-10T03:00:00Z" },
      columns: ["busy"],
      series: [
        {
          key: { core: "0" },
          points: [
            [Date.parse("2026-08-10T00:00:00Z"), 1],
            [Date.parse("2026-08-10T01:00:00Z"), 2],
            [Date.parse("2026-08-10T02:00:00Z"), 3],
          ],
        },
        {
          key: { core: "1" },
          points: [[Date.parse("2026-08-10T02:00:00Z"), 9]],
        },
      ],
    };

    expect(griddedValues(res as never, 0, "busy")).toEqual([1, 2, 3]);
    expect(griddedValues(res as never, 1, "busy")).toEqual([null, null, 9]);
  });

  // The hub clamps what it can serve, so the grid is the ANSWERED window:
  // drawing the requested span would pad the difference with gaps nobody
  // asked about.
  it("builds the grid from the answered window, not the requested one", () => {
    const res = {
      ...raw,
      step_s: 3600,
      window: { from: "2026-08-10T00:00:00Z", to: "2026-08-10T02:00:00Z" },
      requested_window: {
        from: "2026-07-10T00:00:00Z",
        to: "2026-08-10T02:00:00Z",
      },
      columns: ["busy"],
      series: [{ key: {}, points: [[Date.parse("2026-08-10T00:00:00Z"), 1]] }],
    };

    expect(griddedValues(res as never, 0, "busy")).toHaveLength(2);
  });

  // Only the rollup tiers are bucket-aligned: at raw, read/metrics.go
  // selects bare s.ts, so samples land wherever the agent's clock and the
  // collection latency put them. Flooring sent a sample 50ms short of a
  // boundary into the previous bucket, overwrote the sample already there,
  // and left the bucket it belonged in empty -- a one-minute hole drawn for
  // a host that never missed a scrape.
  it("does not fabricate a gap from samples that drift inside their bucket", () => {
    const t = Date.parse("2026-08-10T00:00:00Z");
    const res = {
      ...raw,
      step_s: 60,
      window: { from: "2026-08-10T00:00:00Z", to: "2026-08-10T00:04:00Z" },
      columns: ["busy"],
      series: [
        {
          key: {},
          points: [
            [t, 1],
            [t + 59_950, 2],
            [t + 119_900, 3],
            [t + 179_850, 4],
          ],
        },
      ],
    };

    expect(griddedValues(res as never, 0, "busy")).toEqual([1, 2, 3, 4]);
  });

  it("returns an empty series when the tier does not carry the column", () => {
    expect(griddedValues(raw as never, 0, "iowait")).toEqual([]);
    expect(griddedValues(null, 0, "busy")).toEqual([]);
  });
});

describe("counterDeltas", () => {
  it("reports the increase between buckets, never the running total", () => {
    expect(counterDeltas([10, 12, 12, 15])).toEqual([null, 2, 0, 3]);
  });

  // The leading edge is the trap: a counter's first bucket is its value
  // since boot, so drawing it as an increase puts "4000 failures" in the
  // first five minutes of every chart.
  it("has no value for the first bucket, rather than the counter's absolute value", () => {
    expect(counterDeltas([4000, 4001])).toEqual([null, 1]);
  });

  // A gap is unknown, not quiet, and not a jump. Both mistakes are wrong in
  // opposite directions: zero draws a calm stretch the host never confirmed,
  // and attributing the whole rise to the bucket where reporting resumed
  // invents a spike that never happened.
  it("yields nothing across a gap, in both directions", () => {
    expect(counterDeltas([10, null, 40])).toEqual([null, null, null]);
  });

  // The agent restarted, so the true increase across the boundary is
  // unknowable. A negative number is not a lower bound on it.
  it("yields nothing across a counter reset rather than a negative rate", () => {
    expect(counterDeltas([90, 95, 3, 7])).toEqual([null, 5, null, 4]);
  });

  it("distinguishes a host reporting no events from a host not reporting", () => {
    // Equal neighbours are a real zero: the host answered and nothing
    // happened. That is a fact and belongs on the chart.
    expect(counterDeltas([5, 5, 5])).toEqual([null, 0, 0]);
  });

  it("handles the degenerate lengths without inventing a point", () => {
    expect(counterDeltas([])).toEqual([]);
    expect(counterDeltas([7])).toEqual([null]);
  });
});

describe("counterIncrease", () => {
  it("sums what happened inside the window", () => {
    expect(counterIncrease([10, 12, 12, 15])).toBe(5);
  });

  // "No kills happened" and "we cannot say whether any did" are different
  // facts, and an attention badge must not render the second as the first.
  it("is null when no usable pair exists at all, and 0 when the host said so", () => {
    expect(counterIncrease([])).toBeNull();
    expect(counterIncrease([3])).toBeNull();
    expect(counterIncrease([null, null])).toBeNull();
    expect(counterIncrease([3, 3])).toBe(0);
  });

  it("skips a reset instead of subtracting through it", () => {
    expect(counterIncrease([90, 95, 3, 7])).toBe(9);
  });
});

describe("reduceToColumns", () => {
  // The fold RRDtool does before it draws: one value per pixel column,
  // keeping the largest reading in each. A chart handed more buckets than it
  // has pixels otherwise zigzags between neighbouring buckets inside a single
  // column, and a burst comes out as one more notch in a serrated edge.
  it("averages within a column by default", () => {
    // Given six buckets folded into three columns
    const out = reduceToColumns([1, 9, 2, 3, 4, 5], 3);

    // Then each column is the mean of what landed in it, which is what
    // rrdtool's reduce does against an AVERAGE RRA -- and what Observium's
    // DEF asks for.
    expect(out).toEqual([5, 2.5, 4.5]);
  });

  it("keeps the largest reading in each column when asked", () => {
    // The envelope behind a line wants the peak, not the mean.
    expect(reduceToColumns([1, 9, 2, 3, 4, 5], 3, "max")).toEqual([9, 3, 5]);
  });

  it("leaves a series shorter than the chart alone", () => {
    // Nothing to fold: a chart with more pixels than data draws at the data's
    // own resolution rather than stretching it.
    expect(reduceToColumns([1, 2, 3], 10)).toEqual([1, 2, 3]);
    expect(reduceToColumns([1, 2, 3], 3)).toEqual([1, 2, 3]);
  });

  it("reads through a null rather than widening it into a hole", () => {
    // A column holding both a null and a number is that number: a gap inside
    // one pixel is not a gap anyone can see, and widening it would draw an
    // outage that did not happen. The null is skipped rather than counted as
    // a zero, so it cannot drag the column's mean down either.
    expect(reduceToColumns([1, null, 5, 2], 2)).toEqual([1, 3.5]);
    expect(reduceToColumns([1, null, 5, 2], 2, "max")).toEqual([1, 5]);
  });

  it("keeps a column that has no reading at all as a hole", () => {
    // A host that stopped reporting for a stretch wide enough to own a whole
    // column still leaves a break in the line.
    expect(reduceToColumns([1, 2, null, null, 7, 8], 3)).toEqual([
      1.5,
      null,
      7.5,
    ]);
    expect(reduceToColumns([1, 2, null, null, 7, 8], 3, "max")).toEqual([
      2,
      null,
      8,
    ]);
  });

  it("covers the whole window, with no column left half-empty", () => {
    // Boundaries come from the exact ratio rather than a rounded stride: with
    // a rounded one the error accumulates across the width and the last
    // column reads a fraction of the span the others do. 100 buckets into 7
    // columns is the case that exposes it -- every value must survive into
    // some column, so the largest of the whole series is the largest here.
    const vals = Array.from({ length: 100 }, (_, i) => i);
    const out = reduceToColumns(vals, 7, "max");
    expect(out).toHaveLength(7);
    expect(Math.max(...(out as number[]))).toBe(99);
    // And strictly rising input folds to strictly rising columns, which it
    // cannot do if two columns overlap or one is skipped.
    expect([...(out as number[])].sort((a, b) => a - b)).toEqual(out);
  });

  it("refuses a nonsense width instead of dividing by zero", () => {
    expect(reduceToColumns([1, 2, 3], 0)).toEqual([1, 2, 3]);
  });
});
