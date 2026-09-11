// The deviation kinds as the fleet list renders them.
//
// The detail shapes are the real ones: judgeDeviation builds them in
// internal/hub/store/deviationscan.go. A fixture with invented keys would pass
// while the page rendered a row with no numbers in it, which for these kinds is
// a row that says nothing -- "above normal" is not a complete thought without
// the normal it is above.
import { describe, expect, it } from "vitest";
import { catalogueOf, hostConditions } from "./conditions";
import type { ConditionKindInfo, ConditionRow } from "../../lib/api";

const NOW = new Date("2026-08-12T12:00:00Z");

const KINDS: ConditionKindInfo[] = [
  { kind: "silent", label: "Stopped reporting", severity: "critical" },
  {
    kind: "temperature",
    label: "Temperature above normal",
    severity: "warning",
  },
  {
    kind: "processes",
    label: "Process count above normal",
    severity: "warning",
  },
  { kind: "load", label: "Load above normal", severity: "warning" },
];

const CATALOGUE = catalogueOf(KINDS);

function deviationRow(
  kind: string,
  detail: Record<string, unknown>,
  subject = "",
): ConditionRow {
  return {
    id: 1,
    host_id: 1,
    hostname: "web-01",
    kind,
    subject,
    severity: "warning",
    opened_ts: "2026-08-12T09:46:00Z",
    opened_at_least: true,
    detail,
    measured_ts: "2026-08-12T11:59:30Z",
    stale: false,
  };
}

function sentence(rows: ConditionRow[]): string {
  const out = hostConditions(rows, CATALOGUE, NOW);
  return out.length === 0 ? "" : String(out[0].what);
}

describe("the deviation conditions", () => {
  it("names the sensor, its reading, and what it normally reads", () => {
    expect(
      sentence([
        deviationRow(
          "temperature",
          {
            value: 61,
            warn: 52,
            crit: 58,
            p99: 46,
            source: "baseline",
            unit: "C",
            chip: "drivetemp",
            window_days: 7,
          },
          "drivetemp/temp1/sda",
        ),
      ]),
    ).toBe("drivetemp/temp1/sda is 61 C — normally under 46 C");
  });

  // A host-wide kind has no subject to name, so the sentence starts with the
  // verb rather than with a stray space.
  it("renders a host-wide kind without a subject", () => {
    expect(
      sentence([
        deviationRow("processes", {
          value: 1842,
          warn: 1400,
          crit: 1600,
          p99: 1210,
          source: "baseline",
        }),
      ]),
    ).toBe("is 1842 — normally under 1210");
  });

  it("names the hardware's own limit when that is what fired", () => {
    expect(
      sentence([
        deviationRow(
          "temperature",
          {
            value: 86,
            warn: 80,
            crit: 85,
            p99: 78,
            source: "device",
            unit: "C",
            chip: "nvme",
          },
          "nvme/Composite/nvme0n1",
        ),
      ]),
    ).toBe("nvme/Composite/nvme0n1 is 86 C — past its 85 C limit");
  });

  // The same judgement the disk row makes: a reading on a machine that is off
  // is still that reading, and the tense stops it claiming to describe now.
  it("shifts to the past tense on a host that has stopped reporting", () => {
    const rows = [
      deviationRow("silent", {}),
      deviationRow("load", {
        value: 14.2,
        warn: 8,
        crit: 10,
        p99: 6.1,
        source: "baseline",
      }),
    ];
    rows[0] = { ...rows[0], kind: "silent", severity: "critical" };

    const out = hostConditions(rows, CATALOGUE, NOW);
    const load = out.find((c) => c.kind === "load");
    expect(String(load?.what)).toBe("was 14.2 — normally under 6.1");
  });

  // No meter, deliberately: a reading against a per-subject threshold is not a
  // proportion of anything, and a meter would invent a full scale.
  it("offers no meter and links to the tab holding the series", () => {
    const out = hostConditions(
      [
        deviationRow("load", {
          value: 14.2,
          p99: 6.1,
          source: "baseline",
        }),
      ],
      CATALOGUE,
      NOW,
    );
    expect(out[0].evidence).toBeNull();
    expect(out[0].tab).toBe("system");
  });

  // A row whose detail lost its reading STILL APPEARS, named by the catalogue.
  //
  // Dropping it would be the failure this module exists to prevent: the kind is
  // in the `written` set, so the catch-all that rescues unrecognised kinds does
  // not run for it either, and the host would read clean on the fleet page
  // while the hub had a condition open on it.
  it("still shows a row carrying no reading, named by the catalogue", () => {
    expect(sentence([deviationRow("load", { p99: 6.1 })])).toBe(
      "Load above normal",
    );
  });

  // One row per kind, with a count of the rest -- the same shape the drive row
  // uses. A host with three hot drives is three things wrong, and collapsing to
  // the worst alone loses the other two silently.
  it("counts the other sensors over their own lines", () => {
    const rows = [
      deviationRow(
        "temperature",
        { value: 61, p99: 46, source: "baseline", unit: "C" },
        "drivetemp/temp1/sda",
      ),
      deviationRow(
        "temperature",
        { value: 57, p99: 45, source: "baseline", unit: "C" },
        "drivetemp/temp1/sdb",
      ),
      deviationRow(
        "temperature",
        { value: 55, p99: 44, source: "baseline", unit: "C" },
        "drivetemp/temp1/sdc",
      ),
    ];
    const out = hostConditions(rows, CATALOGUE, NOW);
    expect(out).toHaveLength(1);
    expect(String(out[0].what)).toBe(
      "drivetemp/temp1/sda is 61 C — normally under 46 C (+2 more)",
    );
  });
});
