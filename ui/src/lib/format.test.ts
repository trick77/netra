import { describe, expect, it } from "vitest";
import {
  ABSENT,
  absolute,
  absoluteMs,
  bitrate,
  byterate,
  bytes,
  cardinal,
  duration,
  percent,
  relative,
  relativeMs,
} from "./format";

describe("format", () => {
  // Network is bits, storage is bytes. Mixing them is the classic
  // monitoring-UI bug and it is silent: the number still looks plausible.
  it("formats storage in decimal bytes and network in bits", () => {
    expect(bytes(503_000_000_000)).toBe("503 GB");
    expect(bytes(7_800_000_000_000)).toBe("7.8 TB");
    expect(bitrate(940_000_000)).toBe("940 Mb/s");
    expect(bitrate(1_400_000_000)).toBe("1.4 Gb/s");
  });

  // null means "not collected". Rendering it as 0 states a measurement
  // that was never taken.
  it("renders null as an em dash, never zero", () => {
    expect(bytes(null)).toBe("—");
    expect(bitrate(null)).toBe("—");
    expect(percent(null)).toBe("—");
    expect(duration(null)).toBe("—");
  });

  it("distinguishes zero from absent", () => {
    expect(bytes(0)).toBe("0 B");
    expect(percent(0)).toBe("0 %");
  });

  it("formats durations at two units of precision", () => {
    expect(duration(266 * 86400 + 6 * 3600 + 41 * 60)).toBe("266 d 6 h");
    expect(duration(240)).toBe("4 m");
    expect(duration(41)).toBe("41 s");
  });

  it("formats relative times against a fixed now", () => {
    const now = new Date("2026-08-10T14:00:00Z");
    expect(relative("2026-08-10T13:59:19Z", now)).toBe("41 s ago");
    expect(relative("2026-08-10T11:46:00Z", now)).toBe("2 h 14 m ago");
  });

  it("formats small byte counts below the kB threshold", () => {
    expect(bytes(512)).toBe("512 B");
    expect(bytes(1_500)).toBe("1.5 kB");
  });

  it("formats bitrate below the Mb/s threshold in kb/s", () => {
    expect(bitrate(500_000)).toBe("500 kb/s");
    expect(bitrate(0)).toBe("0 b/s");
  });

  it("percent respects an explicit digits argument", () => {
    expect(percent(42.567, 1)).toBe("42.6 %");
    expect(percent(42)).toBe("42 %");
  });

  it("absolute renders a fixed instant for hover titles", () => {
    // Fixed instant, fixed zone: the output must be deterministic and
    // must not depend on the machine's local timezone.
    expect(absolute("2026-08-10T13:59:19Z", "UTC")).toContain("2026");
  });

  // Host.last_seen (api.ts) is `string | null`; relative/absolute must
  // accept the null a caller will actually hand them, and render it the
  // same way every other absent value renders, not force a separate guard
  // at every call site.
  it("relative and absolute render null as the absent marker", () => {
    expect(relative(null)).toBe(ABSENT);
    expect(absolute(null)).toBe(ABSENT);
  });

  // An unparseable date must render as absent, not as a fabricated
  // "NaN d ago" -- that string looks like a duration but is not one.
  it("relative renders an unparseable date as absent, not NaN d ago", () => {
    expect(relative("garbage")).toBe(ABSENT);
    expect(relative("garbage")).not.toMatch(/NaN/);
  });

  it("absolute renders an unparseable date as absent", () => {
    expect(absolute("garbage")).toBe(ABSENT);
  });

  it("ABSENT is exported so call sites share one absent marker", () => {
    expect(ABSENT).toBe("—");
  });

  // relativeMs/absoluteMs are the epoch-millis counterparts consumed by
  // metrics.ts's seriesTimestamps(), the one timestamp shape in the read
  // API that is not an ISO string.
  it("relativeMs and absoluteMs format an epoch-millisecond instant", () => {
    const now = new Date("2026-08-10T14:00:00Z");
    expect(relativeMs(now.getTime() - 41_000, now)).toBe("41 s ago");
    expect(absoluteMs(now.getTime(), "UTC")).toContain("2026");
  });

  it("relativeMs and absoluteMs render null as absent", () => {
    expect(relativeMs(null)).toBe(ABSENT);
    expect(absoluteMs(null)).toBe(ABSENT);
  });

  // The counterpart to bitrate, and the one netra almost always wants: every
  // rate the agent reports is BYTES per second. Handing one to bitrate() is
  // the 8x-wrong-but-plausible number bitrate's own doc comment warns about,
  // and both traffic cells did exactly that.
  it("byterate scales bytes per second and never divides by eight", () => {
    expect(byterate(1_200_000)).toBe("1.2 MB/s");
    expect(byterate(950)).toBe("950 B/s");
    expect(byterate(1_500_000_000)).toBe("1.5 GB/s");
  });

  // The whole point of keeping the two apart: the same number must not read
  // the same way through both.
  it("byterate and bitrate disagree by a factor of eight in unit, not value", () => {
    expect(byterate(1_000_000)).toBe("1 MB/s");
    expect(bitrate(1_000_000)).toBe("1 Mb/s");
  });

  it("byterate renders null as absent", () => {
    expect(byterate(null)).toBe(ABSENT);
  });
});

describe("cardinal", () => {
  it("groups digits in threes", () => {
    expect(cardinal(48231)).toBe("48\u202f231");
    expect(cardinal(262144)).toBe("262\u202f144");
    expect(cardinal(999)).toBe("999");
    expect(cardinal(0)).toBe("0");
  });

  // Deliberately NOT scale(): "48 k of 262 k" throws away the only digits
  // that distinguish a comfortable descriptor table from a nearly-full one.
  it("keeps every digit rather than scaling to a magnitude", () => {
    expect(cardinal(1_234_567)).toBe("1\u202f234\u202f567");
    expect(cardinal(1_234_567)).not.toContain("M");
  });

  it("renders null as absent, never as 0", () => {
    expect(cardinal(null)).toBe(ABSENT);
  });

  it("keeps a negative sign outside the grouping", () => {
    expect(cardinal(-4200)).toBe("-4\u202f200");
  });
});
