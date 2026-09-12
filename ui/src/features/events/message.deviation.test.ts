// The deviation kinds in the event log.
//
// The detail shapes here are the real ones: judgeDeviation builds them in
// internal/hub/store/deviationscan.go and insertConditionEvent carries them
// into the events table verbatim. A fixture that invented its own keys would
// pass while the page rendered blanks -- which is the failure the note at the
// top of message.test.ts records.
import { describe, expect, it } from "vitest";
import type { Event } from "../../lib/api";
import { CONDITION_EVENT_TYPES, KNOWN_EVENT_TYPES, messageOf } from "./message";

function deviation(
  type: string,
  detail: Record<string, unknown>,
  subject = "",
): Event {
  return {
    id: "e:1",
    host_id: 3,
    hostname: "web-01",
    ts: "2026-08-10T13:59:00Z",
    type,
    subject,
    detail,
    severity: "warning",
  };
}

describe("messageOf, deviation conditions", () => {
  // THE WHOLE REASON THESE KINDS NAME THEIR NUMBERS. "Above normal" is not a
  // complete thought without the normal it is above, and that figure is
  // different on every subject and lives nowhere else once the condition
  // clears.
  it("names the reading and what normal is", () => {
    expect(
      messageOf(
        deviation(
          "temperature",
          {
            transition: "opened",
            value: 54.2,
            warn: 52,
            crit: 58,
            normal: 46.039215686,
            source: "baseline",
            unit: "C",
            span_days: 12,
          },
          "drivetemp/temp1/sda",
        ),
      ),
    ).toBe(
      "Temperature above normal — drivetemp/temp1/sda — 54.2 C, against a normal under 46 C",
    );
  });

  // The vendor's own limit and a calibrated threshold are different claims,
  // and an operator deciding whether to act reads them differently.
  it("says so when the hardware's own limit is what fired", () => {
    expect(
      messageOf(
        deviation(
          "temperature",
          {
            transition: "opened",
            value: 86,
            warn: 80,
            crit: 85,
            normal: 78,
            source: "device",
            unit: "C",
          },
          "nvme/Composite/nvme0n1",
        ),
      ),
    ).toBe(
      "Temperature above normal — nvme/Composite/nvme0n1 — 86 C, against its own limit of 85 C",
    );
  });

  // A unitless kind must not print a trailing space, and a whole number must
  // not grow a ".0".
  it("renders a unitless count without decoration", () => {
    expect(
      messageOf(
        deviation("processes", {
          transition: "opened",
          value: 1842,
          normal: 1210,
          source: "baseline",
        }),
      ),
    ).toBe("Process count above normal — 1842, against a normal under 1210");
  });

  it("still reports how long a cleared deviation was open", () => {
    expect(
      messageOf(
        deviation("load", {
          transition: "cleared",
          reason: "cleared",
          open_ms: 2_460_000,
        }),
      ),
    ).toBe("Load above normal cleared after 41 m");
  });

  // A transition carrying no figures falls back to the bare label rather than
  // printing an empty clause after a dash.
  it("falls back to the label when the detail carried no reading", () => {
    expect(messageOf(deviation("load", { transition: "opened" }))).toBe(
      "Load above normal",
    );
  });

  // A kind missing from the dropdown list renders as a generic detail dump
  // instead of a sentence, so the list growing with the observers is the point.
  it("offers every deviation kind in the type filter", () => {
    for (const kind of ["temperature", "processes", "load"]) {
      expect(CONDITION_EVENT_TYPES).toContain(kind);
      expect(KNOWN_EVENT_TYPES).toContain(kind);
    }
  });
});
