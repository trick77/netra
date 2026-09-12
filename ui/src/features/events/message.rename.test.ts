// The detail key rename, against the events already in the database.
//
// `p99` became `normal` when the threshold stopped being a percentile. The event
// log is history: its retention is 90 days, so rows written under the old key
// stay on screen for months, and a reader that only knows the new one renders a
// sentence that stops in the middle.
import { describe, expect, it } from "vitest";
import type { Event } from "../../lib/api";
import { messageOf } from "./message";

function deviation(detail: Record<string, unknown>, subject = ""): Event {
  return {
    id: "e:1",
    host_id: 3,
    hostname: "web-01",
    ts: "2026-08-10T13:59:00Z",
    type: "temperature",
    subject,
    detail,
    severity: "warning",
  };
}

describe("messageOf, the p99 to normal rename", () => {
  it("reads the old p99 key on an event written before the rename", () => {
    expect(
      messageOf(
        deviation(
          {
            transition: "opened",
            value: 54.2,
            p99: 46,
            source: "baseline",
            unit: "C",
          },
          "drivetemp/temp1/sda",
        ),
      ),
    ).toBe(
      "Temperature above normal — drivetemp/temp1/sda — 54.2 C, against a normal under 46 C",
    );
  });

  it("prefers the new key when both are present", () => {
    expect(
      messageOf(
        deviation({
          transition: "opened",
          value: 54.2,
          normal: 46,
          p99: 99,
          source: "baseline",
          unit: "C",
        }),
      ),
    ).toBe("Temperature above normal — 54.2 C, against a normal under 46 C");
  });

  // The failure this guards: neither key present must not leave a sentence
  // hanging after the word "under".
  it("drops the clause rather than dangling it when neither key is present", () => {
    const out = messageOf(
      deviation({
        transition: "opened",
        value: 54.2,
        source: "baseline",
        unit: "C",
      }),
    );
    expect(out).toBe("Temperature above normal — 54.2 C");
    expect(out).not.toMatch(/under\s*$/);
    expect(out).not.toMatch(/against\s*$/);
  });
});
