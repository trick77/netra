import { describe, expect, it } from "vitest";
import {
  bytes,
  bitrate,
  percent,
  duration,
  relative,
  absolute,
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
});
