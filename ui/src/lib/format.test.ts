import { describe, expect, it } from "vitest";
import {
  ABSENT,
  absolute,
  absoluteMs,
  binaryBytes,
  binaryBytesPair,
  bitrate,
  byterate,
  bytes,
  bytesPair,
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

  // Installed RAM is the one byte count read against a binary spec sheet.
  // 33_260_000_000 is roughly what a 32 GiB machine reports as MemTotal;
  // decimally that is "33.3 GB", which is above the capacity the machine
  // actually has and ~7% above the figure on the invoice.
  it("formats installed memory in binary units", () => {
    expect(binaryBytes(33_260_000_000)).toBe("31 GiB");
    expect(bytes(33_260_000_000)).toBe("33.3 GB");
    expect(binaryBytes(8 * 1024 ** 3)).toBe("8 GiB");
    expect(binaryBytes(1024)).toBe("1 KiB");
    expect(binaryBytes(512)).toBe("512 B");
  });

  it("renders absent and zero memory the same way decimal bytes do", () => {
    expect(binaryBytes(null)).toBe("—");
    expect(binaryBytes(0)).toBe("0 B");
  });

  // The promotion guard has to move with the base, or a value just under a
  // binary boundary renders as "1024 GiB" instead of "1 TiB".
  it("promotes to the next binary unit rather than printing 1024", () => {
    expect(binaryBytes(1024 ** 4 - 1)).toBe("1 TiB");
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

describe("a value against its ceiling", () => {
  // The reason this exists at all: formatted apart, the two halves pick
  // their own units and the reader has to convert one before the pair means
  // anything. Scaled against the ceiling, "0.9" is visibly a fraction of 16.
  it("states one unit, chosen by the ceiling", () => {
    expect(bytesPair(900_000_000, 16_000_000_000)).toBe("0.9 · 16 GB");
    expect(`${bytes(900_000_000)} of ${bytes(16_000_000_000)}`).toBe(
      "900 MB of 16 GB",
    );
  });

  it("keeps memory binary, like binaryBytes", () => {
    expect(binaryBytesPair(21_900_000_000, 33_260_000_000)).toBe(
      "20.4 · 31 GiB",
    );
  });

  // An absent reading is still absent next to a ceiling that is known: the
  // host has 31 GiB whether or not it reported what it is using.
  it("marks an absent value without losing the ceiling", () => {
    expect(binaryBytesPair(null, 33_260_000_000)).toBe("— · 31 GiB");
  });

  // No ceiling is not a pair. The value alone is still a measurement.
  it("falls back to the value alone when there is no ceiling", () => {
    expect(bytesPair(1_200_000, null)).toBe("1.2 MB");
    expect(bytesPair(null, null)).toBe("—");
  });

  // 4 MB of swap against 8 GiB is 0.0037 GiB. Printed at the ceiling's unit
  // that is "0 · 8 GiB", which says the host is not swapping -- and on swap,
  // that it is swapping at all is the entire reading.
  it("keeps a value the shared unit would round to zero", () => {
    expect(binaryBytesPair(4_000_000, 8_589_934_592)).toBe("3.8 MiB · 8 GiB");
    expect(bytesPair(30_000_000, 4_000_000_000)).toBe("30 MB · 4 GB");
  });

  // A true zero is not the same case: it says the host has none, and "0"
  // against the ceiling is the clearest way to say so.
  it("still shares the unit for a true zero", () => {
    expect(binaryBytesPair(0, 8_589_934_592)).toBe("0 · 8 GiB");
  });

  it("promotes a ceiling that rounds up to the base", () => {
    expect(bytesPair(500_000_000_000, 999_960_000_000)).toBe("0.5 · 1 TB");
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
