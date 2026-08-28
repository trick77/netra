import { describe, expect, it } from "vitest";
import {
  mirroredTicks,
  niceStep,
  niceTicks,
  timeLabel,
  timeTicks,
} from "./ticks";

const HOUR = 3600_000;
const DAY = 24 * HOUR;

/** The value of every labelled tick, in axis order. */
function majors(ticks: { value: number; major: boolean }[]): number[] {
  return ticks.filter((t) => t.major).map((t) => t.value);
}

describe("ticks", () => {
  describe("niceStep", () => {
    // The bug this rounding direction exists to prevent, kept as a test
    // because it is invisible until an axis renders with no labels at all:
    // a 5.4 GiB stack asking for a ~2.7 GiB step got 5 GiB when this
    // rounded up, which is larger than any tick that fits under the ceiling.
    it("rounds down, so the step never exceeds the range it divides", () => {
      expect(niceStep(2.7)).toBe(2);
      expect(niceStep(4.9)).toBe(2);
      expect(niceStep(9.9)).toBe(5);
    });

    it("returns exact powers when the input already is one", () => {
      expect(niceStep(1)).toBe(1);
      expect(niceStep(100)).toBe(100);
    });

    it("steps binary quantities on powers of two", () => {
      const GiB = 1024 ** 3;
      expect(niceStep(2.7 * GiB, 1024)).toBe(2 * GiB);
      expect(niceStep(7 * GiB, 1024)).toBe(4 * GiB);
      expect(niceStep(9 * GiB, 1024)).toBe(8 * GiB);
    });

    // The mantissa runs to 512, because `norm` lives in [1, 1024) rather
    // than [1, 10). Capping it at 8 made any step 16x or more above a power
    // of 1024 up to 128 times too small -- a 100 GiB filesystem asking for
    // ~33 GiB steps got 8 GiB, which is fifty ticks on a 260px panel.
    it("keeps stepping past 8x a power of 1024", () => {
      const GiB = 1024 ** 3;
      expect(niceStep(33 * GiB, 1024)).toBe(32 * GiB);
      expect(niceStep(100 * GiB, 1024)).toBe(64 * GiB);
      expect(niceStep(700 * GiB, 1024)).toBe(512 * GiB);
    });

    it("keeps a binary step a whole number of binary units", () => {
      const GiB = 1024 ** 3;
      for (const rough of [3 * GiB, 33 * GiB, 300 * GiB]) {
        const step = niceStep(rough, 1024);
        expect(Number.isInteger(step / GiB)).toBe(true);
      }
    });

    // Not an axis this app draws, but the function is total and a caller
    // deriving a step from an empty series must not get NaN back.
    it("is total for degenerate input", () => {
      expect(niceStep(0)).toBe(1);
      expect(niceStep(-5)).toBe(1);
      expect(niceStep(NaN)).toBe(1);
    });
  });

  describe("niceTicks", () => {
    it("labels round values rather than fractions of the data's peak", () => {
      // The failure in prose: a chart peaking at 843.6 used to label
      // 843.6 / 421.8 / 0, three numbers nobody would choose.
      expect(majors(niceTicks(0, 843.6))).toEqual([0, 200, 400, 600, 800]);
    });

    it("always yields at least one labelled tick", () => {
      // The regression the round-down rule fixes, stated at the level a
      // reader sees it: this axis had none.
      const GiB = 1024 ** 3;
      expect(majors(niceTicks(0, 5.4 * GiB, 3, 1024)).length).toBeGreaterThan(
        0,
      );
    });

    it("puts unlabelled helper ticks between the labelled ones", () => {
      const ticks = niceTicks(0, 800);
      expect(majors(ticks)).toEqual([0, 200, 400, 600, 800]);
      // Four minor divisions per major, so 50 is a tick and is not labelled.
      const minor = ticks.find((t) => t.value === 50);
      expect(minor).toBeDefined();
      expect(minor?.major).toBe(false);
    });

    it("places a tick at the fraction of the box its value sits at", () => {
      const ticks = niceTicks(0, 100);
      expect(ticks.find((t) => t.value === 50)?.fraction).toBeCloseTo(0.5, 10);
      expect(ticks.find((t) => t.value === 100)?.fraction).toBeCloseTo(1, 10);
    });

    it("steps from a non-zero floor on round values, not round offsets", () => {
      // A free-scaled panel -- the sensor list -- is drawn between its own
      // extent. Ticks must still be round numbers, not floor + n * step.
      const ticks = niceTicks(44, 47);
      for (const value of majors(ticks)) {
        expect(Math.abs(value * 2 - Math.round(value * 2))).toBeLessThan(1e-9);
      }
    });

    it("keeps the top tick when it lands exactly on the ceiling", () => {
      // Float drift used to drop this one, and the axis lost its top label.
      expect(majors(niceTicks(0, 1000))).toContain(1000);
    });

    // scaleY() centres a min === max series at mid-height rather than
    // dividing by zero, so the axis has to say that one value at that one
    // height -- a fan pinned at 1200 RPM must not be labelled 1201/1201/1200.
    it("states a flat series once, at mid-height", () => {
      expect(niceTicks(1200, 1200)).toEqual([
        { fraction: 0.5, value: 1200, major: true },
      ]);
    });

    it("labels a binary axis on whole binary units", () => {
      const GiB = 1024 ** 3;
      expect(majors(niceTicks(0, 16 * GiB, 3, 1024))).toEqual([
        0,
        4 * GiB,
        8 * GiB,
        12 * GiB,
        16 * GiB,
      ]);
    });
  });

  describe("mirroredTicks", () => {
    // Traffic has a direction, not a sign: both halves label a magnitude,
    // and zero is the midline. A signed range would put "-200 M" below the
    // line and state a negative rate.
    it("is symmetric about a zero midline and labels magnitudes", () => {
      const ticks = mirroredTicks(800);
      expect(ticks.find((t) => t.fraction === 0.5)?.value).toBe(0);
      expect(ticks.every((t) => t.value >= 0)).toBe(true);

      const above = ticks.find((t) => t.value === 400 && t.fraction > 0.5);
      const below = ticks.find((t) => t.value === 400 && t.fraction < 0.5);
      expect(above?.fraction).toBeCloseTo(0.75, 10);
      expect(below?.fraction).toBeCloseTo(0.25, 10);
    });

    it("reaches both edges of the box at the ceiling", () => {
      const ticks = mirroredTicks(800);
      expect(ticks.some((t) => t.value === 800 && t.fraction === 1)).toBe(true);
      expect(ticks.some((t) => t.value === 800 && t.fraction === 0)).toBe(true);
    });

    it("states an idle interface once, at zero", () => {
      expect(mirroredTicks(0)).toEqual([
        { fraction: 0.5, value: 0, major: true },
      ]);
    });
  });

  describe("timeTicks", () => {
    // Local, not UTC: rounding the epoch value lands on midnight UTC, which
    // is not midnight where the reader lives, and every daily tick on the
    // axis would sit at an odd hour.
    it("breaks on local clock boundaries", () => {
      const from = new Date(2026, 7, 15, 13, 18).getTime();
      const ticks = timeTicks(from, from + DAY, 8);
      for (const tick of ticks.filter((t) => t.major)) {
        const at = new Date(from + tick.fraction * DAY);
        expect(at.getMinutes()).toBe(0);
        expect(at.getSeconds()).toBe(0);
      }
    });

    it("never labels more often than the axis has room for", () => {
      const from = new Date(2026, 7, 15, 13, 18).getTime();
      // A 260px panel asks for three labels and must not get eight.
      const ticks = timeTicks(from, from + DAY, 3);
      expect(ticks.filter((t) => t.major).length).toBeLessThanOrEqual(4);
    });

    it("puts unlabelled helper ticks between the labelled ones", () => {
      const from = new Date(2026, 7, 15, 13, 18).getTime();
      const ticks = timeTicks(from, from + DAY, 8);
      expect(ticks.some((t) => !t.major && t.label === null)).toBe(true);
      expect(ticks.filter((t) => t.major).every((t) => t.label !== null)).toBe(
        true,
      );
    });

    it("stays inside the window", () => {
      const from = new Date(2026, 7, 15, 13, 18).getTime();
      const ticks = timeTicks(from, from + DAY);
      expect(ticks.every((t) => t.fraction >= 0 && t.fraction <= 1)).toBe(true);
    });

    it("returns nothing for a window with no width", () => {
      const at = Date.UTC(2026, 7, 15);
      expect(timeTicks(at, at)).toEqual([]);
    });
  });

  describe("timeLabel", () => {
    const at = new Date(2026, 7, 15, 18, 0);

    it("shows the clock alone inside a day", () => {
      expect(timeLabel(at, 6 * HOUR)).toBe("18:00");
    });

    // A bare "18:00" appears twice on a two-day axis; the weekday is what
    // tells the reader which one they are looking at.
    it("adds the weekday once the axis spans more than a day", () => {
      expect(timeLabel(at, 2 * DAY)).toBe("Sat 18:00");
    });

    it("switches to the date past a week, where a weekday repeats", () => {
      expect(timeLabel(at, 30 * DAY)).toBe("15 Aug");
    });
  });
});
