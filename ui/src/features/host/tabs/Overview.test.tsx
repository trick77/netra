import { describe, expect, it, onTestFinished, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import type {
  Event,
  HostDetail,
  MetricsResponse,
  Unit,
} from "../../../lib/api";
import { ABSENT } from "../../../lib/format";
import { Overview, needsAttention } from "./Overview";
import { catalogueOf } from "../../fleet/conditions";
import type { ConditionKindInfo, ConditionRow } from "../../../lib/api";

// The catalogue as internal/hub/conditions/catalogue.go serves it.
const KINDS: ConditionKindInfo[] = [
  { kind: "silent", label: "Stopped reporting", severity: "critical" },
  { kind: "sporadic", label: "Reporting sporadically", severity: "warning" },
  { kind: "failed-units", label: "Failed units", severity: "warning" },
  {
    kind: "disk",
    label: "Filesystem nearly full",
    severity: "warning",
    thresholds: {
      warn_pct: 90,
      crit_pct: 95,
      warn_free: 100 * 1024 ** 3,
      crit_free: 20 * 1024 ** 3,
    },
  },
  { kind: "drive", label: "Drive errors", severity: "critical" },
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

const host: HostDetail = {
  id: 7,
  hostname: "kessel",
  location: "Roubaix, France",
  provider: "OVH",
  facility: "RBX2",
  last_seen: "2026-08-10T01:00:00Z",
  cpu_total: 12,
  mem_used: 4_000_000_000,
  mem_total: 8_000_000_000,
  uptime_s: 86_400,
  net_rx_bytes: 1.5e6,
  net_tx_bytes: 4e5,
  services_total: 397,
  services_failed: 1,
  fingerprint: "fp",
  host_type: "vps",
  agent_version: "0.4.1",
  go_version: "go1.25",
  kernel: "6.8.0-31-generic",
  os_name: "Ubuntu 24.04",
  arch: "amd64",
  cpu_model: "EPYC 7003",
  cores: 4,
  threads: 8,
  memory_total: 8_000_000_000,
  latitude: null,
  longitude: null,
  created_at: "2026-01-01T00:00:00Z",
  capabilities: { docker: "ok", smart: "not permitted" },
};

function response(
  over: Partial<MetricsResponse> & { family: string },
): MetricsResponse {
  return {
    tier: "raw",
    step_s: 60,
    window: { from: "2026-08-10T00:00:00Z", to: "2026-08-10T01:00:00Z" },
    requested_window: {
      from: "2026-08-10T00:00:00Z",
      to: "2026-08-10T01:00:00Z",
    },
    warnings: [],
    key_columns: [],
    columns: [],
    series: [],
    truncated: false,
    ...over,
  };
}

// One host sample: swap_total null is the case §7.5 exists for.
function hostMetrics(swapTotal: number | null, swapUsed: number | null) {
  return response({
    family: "host",
    columns: [
      "cpu_user",
      "cpu_system",
      "cpu_iowait",
      "cpu_steal",
      "mem_total",
      "mem_used",
      "swap_total",
      "swap_used",
    ],
    series: [
      {
        key: {},
        points: [
          [
            1_754_784_000_000,
            10,
            4,
            1,
            0,
            8_000_000_000,
            4_000_000_000,
            swapTotal,
            swapUsed,
          ],
        ],
      },
    ],
  });
}

const fsMetrics = response({
  family: "filesystem",
  key_columns: ["filesystem"],
  columns: ["total", "used", "free", "inodes_total", "inodes_used"],
  series: [
    {
      key: { filesystem: "/" },
      // The FINAL bucket of the response window, for netMetrics' reason: the
      // card reads the latest bucket, so a point outside the window is not a
      // stale reading, it is no reading. This fixture carried a timestamp a
      // year before its own window and went unnoticed while the card read
      // the last value the series happened to hold.
      points: [
        [
          1_786_323_540_000, 100_000_000_000, 40_000_000_000, 55_000_000_000,
          100, 40,
        ],
      ],
    },
  ],
});

const netMetrics = response({
  family: "net",
  key_columns: ["interface"],
  columns: ["rx_bytes", "tx_bytes"],
  series: [
    {
      key: { interface: "eth0" },
      // The FINAL bucket of the response window above. Inside the window at
      // all, because griddedValues() drops a point that falls outside it --
      // and in the last bucket specifically, so this test is about the
      // formatter rather than about which bucket the card reads from.
      points: [[1_786_323_540_000, 2_000_000, 500_000]],
    },
  ],
});

function renderOverview(over: Partial<Parameters<typeof Overview>[0]> = {}) {
  return render(
    <Overview
      host={host}
      hostMetrics={hostMetrics(null, null)}
      filesystemMetrics={fsMetrics}
      units={[]}
      now={new Date("2026-08-10T01:00:30Z")}
      {...over}
    />,
  );
}

/** The one-line facts under the name. */
function facts() {
  return [
    ...screen
      .getByRole("region", { name: "System" })
      .querySelectorAll(".hostfacts .txt"),
  ].map((s) => s.textContent);
}

describe("Overview facts line", () => {
  // Text, not a ::before: a copied line reads as one line, dots included.
  it("separates the facts with a middot that copies with them", () => {
    renderOverview();
    const line = screen
      .getByRole("region", { name: "System" })
      .querySelector(".hostfacts")!.textContent!;
    const en = String.fromCharCode(0x2002);
    expect(line.split(`${en}·${en}`)).toEqual(facts());
  });
});

describe("Overview system facts", () => {
  // GOOS is a build constant, so an agent that could not read /etc/os-release
  // falls back to it and the page used to print the compiler's token.
  it("names the operating system rather than printing GOOS", () => {
    renderOverview({ host: { ...host, os_name: "linux" } });
    expect(facts()).toContain("Linux");
  });

  it("does not title-case its way to Darwin and Freebsd", () => {
    renderOverview({ host: { ...host, os_name: "darwin" } });
    expect(facts()).toContain("macOS");
  });

  // ~32 GiB of MemTotal. Decimally this reads "33.3 GB", which is above the
  // capacity the machine actually has.
  it("states installed memory in the binary units it was sold in", () => {
    renderOverview({ host: { ...host, memory_total: 33_260_000_000 } });
    expect(facts()).toContain("31 GiB");
  });

  // One line, read once when the page opens: what the machine is and where.
  it("states the machine in one line, where it is included", () => {
    renderOverview();
    expect(facts()).toEqual([
      "Ubuntu 24.04",
      "6.8.0-31-generic",
      "amd64",
      "EPYC 7003",
      "4 cores, 8 threads",
      "7.5 GiB",
      "Roubaix, France",
      "OVH",
      "RBX2",
      "agent 0.4.1",
    ]);
  });

  // A fact the agent never reported is not written: a dash is a placeholder
  // for a value that should be there, and a VPS without a cpu_model is not
  // missing anything.
  it("omits a fact the host never reported rather than writing a dash", () => {
    renderOverview({
      host: {
        ...host,
        kernel: null,
        cpu_model: null,
        location: null,
        provider: null,
        facility: null,
        cores: 1,
        threads: 1,
      },
    });
    expect(facts()).toEqual([
      "Ubuntu 24.04",
      "amd64",
      "1 core",
      "7.5 GiB",
      "agent 0.4.1",
    ]);
    expect(facts().join(" ")).not.toContain(ABSENT);
  });

  it("writes no line at all for a host that reported nothing", () => {
    renderOverview({
      host: {
        ...host,
        os_name: null,
        kernel: null,
        arch: null,
        cpu_model: null,
        cores: null,
        memory_total: null,
        location: null,
        provider: null,
        facility: null,
        agent_version: null,
      },
    });
    expect(screen.queryByRole("region", { name: "System" })).toBeNull();
  });
});

describe("Overview", () => {
  // "Nothing is wrong" and "netra could not be asked what is wrong" render
  // identically as an absent panel, and only one of them is a fact about this
  // host. Without this the failure is invisible: an offline host with a full
  // disk and a failing drive shows a page with no attention panel at all.
  it("says so when the conditions could not be fetched", () => {
    renderOverview({ conditionsUnavailable: true });
    expect(screen.getByRole("alert")).toHaveTextContent(
      /could not be asked what is wrong/i,
    );
  });

  it("says nothing of the sort when they were fetched and there is nothing wrong", () => {
    renderOverview();
    expect(screen.queryByRole("alert")).toBeNull();
  });

  // The four readings the host's card on the main page shows, drawn by the
  // same cells -- so a reading here cannot disagree with the one a page up.
  it("leads with the main page's four readings", () => {
    const { container } = renderOverview({ netMetrics });
    const readings = container.querySelectorAll(".readings > .reading");
    expect(readings).toHaveLength(4);
    expect(
      [...readings].map((r) => r.querySelector(".k")?.textContent),
    ).toEqual(["CPU", "Memory", "Filesystem", "Traffic"]);
  });

  // The CPU figure is the host's current reading (12), not the series' last
  // bucket (77): at 12mo that bucket is yesterday's daily mean.
  it("prints the current CPU, not the series' last bucket", () => {
    // isReporting reads the wall clock; the fixture host last reported at
    // 01:00 on its own day.
    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(new Date("2026-08-10T01:00:30Z"));
    onTestFinished(() => {
      vi.useRealTimers();
    });
    renderOverview({
      netMetrics,
      hostMetrics: response({
        family: "host",
        columns: ["cpu_total", "mem_total", "mem_used"],
        series: [
          {
            key: {},
            points: [[1_786_323_540_000, 77, 8_000_000_000, 4_000_000_000]],
          },
        ],
      }),
    });
    expect(screen.getByRole("img", { name: "CPU now 12 %" })).toBeTruthy();
  });

  // The band is read first: above the facts and the readings, never among
  // them.
  it("puts the band above the readings", () => {
    const { container } = renderOverview({
      units: [
        {
          id: 1,
          unit_name: "exim4.service",
          state: "failed",
          substate: "failed",
          since: "2026-08-10T00:00:00Z",
          restarts_1h: 0,
        },
      ],
    });
    const band = screen.getByRole("region", { name: /needs attention/i });
    const readings = container.querySelector(".readings")!;
    expect(
      band.compareDocumentPosition(readings) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
  });

  it("lists every mount in the Filesystems table", () => {
    renderOverview({
      filesystems: [{ id: 1, label: "/", mountpoint: "/", device_id: null }],
    });
    const table = screen.getByRole("region", { name: "Filesystems" });
    // One row for the one mount, its Use% from the window: 40 of 95 GB
    expect(within(table).getAllByRole("row")).toHaveLength(2);
    expect(within(table).getByText("42%")).toBeInTheDocument();
  });

  it("says nothing at all when nothing is wrong", () => {
    renderOverview();
    expect(
      screen.queryByRole("region", { name: /needs attention/i }),
    ).toBeNull();
    expect(screen.queryByText(/needs attention/i)).toBeNull();
  });
});

describe("Overview systemd units", () => {
  const NOW = new Date("2026-08-10T01:00:30Z");

  function unit(over: Partial<Unit> = {}): Unit {
    return {
      id: 1,
      unit_name: "exim4.service",
      state: "failed",
      substate: "failed",
      since: "2026-08-10T00:00:00Z",
      restarts_1h: 0,
      ...over,
    };
  }

  function attention(units: Unit[]): string {
    renderOverview({ units, now: NOW });
    const band = screen.queryByRole("region", { name: /needs attention/i });
    return band?.textContent ?? "";
  }

  it("warns about a failed unit", () => {
    expect(attention([unit()])).toMatch(/exim4\.service failed/);
  });

  // The bug this whole change exists for. The warning used to be pinned by
  // whatever the last event said, so a unit that recovered while the agent was
  // down stayed "failed" on this page forever.
  it("stops warning once the unit is reported healthy again", () => {
    const text = attention([unit({ state: "active", substate: "running" })]);
    expect(text).not.toMatch(/exim4\.service/);
  });

  // A purged unit is deleted hub-side rather than corrected, so it reaches the
  // page by being absent rather than by changing state.
  it("stops warning about a unit that is no longer on the host", () => {
    expect(attention([])).not.toMatch(/exim4\.service/);
  });

  // The unit nothing else can catch: a service that runs a few minutes, dies
  // and comes back is HEALTHY at almost every scrape, and systemd never
  // escalates it to `failed` because it does not trip the start limit. Only
  // the transition count gives it away, which is why the warning is keyed on a
  // rate rather than on the state in front of it.
  it("warns about a unit that keeps restarting, even while it looks healthy", () => {
    const text = attention([
      unit({ state: "active", substate: "running", restarts_1h: 9 }),
    ]);
    expect(text).toMatch(/exim4\.service restarted 9 times in the last hour/);
  });

  it("does not call an ordinary restart a loop", () => {
    const text = attention([
      unit({ state: "active", substate: "running", restarts_1h: 2 }),
    ]);
    expect(text).not.toMatch(/exim4\.service/);
  });

  // A single sighting of auto-restart is not a rate. It is the gap BETWEEN
  // attempts -- at the default RestartSec=100ms a 60s scrape essentially never
  // lands in it, so treating one as proof of a loop would be a coin toss
  // dressed up as a warning.
  it("does not treat one sighting of auto-restart as a loop", () => {
    const text = attention([
      unit({ state: "activating", substate: "auto-restart", restarts_1h: 1 }),
    ]);
    expect(text).not.toMatch(/restarting|restarted/);
  });

  // A failed unit is reported as failed, not doubly as a loop.
  it("reports a failed unit once", () => {
    const text = attention([unit({ restarts_1h: 9 })]);
    expect(text).toMatch(/exim4\.service failed/);
    expect(text).not.toMatch(/restarted/);
  });
});

describe("the Events card", () => {
  const event = (i: number, severity: Event["severity"]): Event => ({
    id: `e:${i}`,
    host_id: host.id,
    hostname: host.hostname,
    ts: new Date(Date.parse("2026-08-10T00:00:00Z") + i * 60_000).toISOString(),
    type: "unit_failed",
    subject: `svc${i}.service`,
    detail: null,
    severity,
  });

  it("lists the window's events newest first, in the log's words", () => {
    renderOverview({
      events: [event(1, "info"), event(3, "critical"), event(2, "warning")],
    });
    const card = screen.getByRole("region", { name: "Events" });
    const items = within(card).getAllByRole("listitem");
    expect(items.map((li) => li.textContent)).toEqual([
      expect.stringMatching(/svc3/),
      expect.stringMatching(/svc2/),
      expect.stringMatching(/svc1/),
    ]);
    // Severity by shape as well as hue
    expect(within(items[0]!).getByRole("img", { name: "critical" }));
    expect(within(items[2]!).getByRole("img", { name: "info" }));
  });

  it("hands the rest over to the Events tab", () => {
    renderOverview({
      events: Array.from({ length: 11 }, (_, i) => event(i, "info")),
    });
    expect(
      screen.getByRole("link", { name: "3 more on the Events tab" }),
    ).toHaveAttribute("href", "/hosts/7/events");
  });

  // A full page is the hub's limit, not the window's count.
  it("does not count past the hub's limit as the window's total", () => {
    renderOverview({
      range: "1h",
      events: Array.from({ length: 500 }, (_, i) => event(i, "info")),
    });
    expect(
      screen.getByRole("link", { name: "Over 492 more on the Events tab" }),
    ).toBeInTheDocument();
  });

  // A notification follows the event it reports; the card keeps its slots
  // for causes, and the Events tab has the rest.
  it("leaves notifications to the Events tab", () => {
    renderOverview({
      events: [
        event(1, "critical"),
        { ...event(2, "info"), type: "notification", subject: null },
      ],
    });
    const card = screen.getByRole("region", { name: "Events" });
    expect(within(card).getAllByRole("listitem")).toHaveLength(1);
  });

  it("says a quiet window was quiet, and an unread one was not read", () => {
    const { unmount } = renderOverview({ events: [], range: "24h" });
    expect(
      screen.getByText("Nothing happened on this host in the last 24h."),
    ).toBeInTheDocument();
    unmount();
    renderOverview({ events: null });
    expect(screen.getByText("No events have been read yet."));
  });
});

describe("the Docker CPU panel", () => {
  const containers = (n: number) =>
    response({
      family: "container",
      key_columns: ["container"],
      columns: ["cpu_pct", "mem_used", "mem_limit"],
      series: Array.from({ length: n }, (_, i) => ({
        key: { container: `stack/svc-${i}` },
        points: [[1_786_323_540_000, i + 1, 10, null]],
      })),
    });

  it("draws the heaviest containers on a host that runs any", () => {
    renderOverview({ containerMetrics: containers(3) });
    expect(
      screen.getByRole("region", { name: "Docker CPU chart" }),
    ).toBeInTheDocument();
  });

  it("draws nothing on a host without containers", () => {
    renderOverview({ containerMetrics: containers(0) });
    expect(screen.queryByRole("region", { name: /Docker CPU/ })).toBeNull();
  });
});

// The band renders the HUB's verdicts now. The thresholds it used to apply
// here -- written out in TypeScript, beside a second copy in the fleet module
// and a third in Go -- are the hub's, and they are pinned where they live:
// internal/hub/conditions/rules_test.go for the numbers,
// TestIntegrationHubVerdictEqualsTheBrowsersOnTheSharedFixture for the
// agreement this page and the fleet page used to state in comments to each
// other. What is left here is the sentence and the onset beside it.
describe("the attention band", () => {
  const quiet = {
    agentMetrics: null,
    hostMetrics: null,
    units: null,
    catalogue: CATALOGUE,
  };
  const row = (over: Partial<ConditionRow> = {}): ConditionRow => ({
    id: 1,
    host_id: host.id,
    hostname: host.hostname,
    kind: "disk",
    subject: "pool",
    severity: "critical",
    opened_ts: "2026-08-10T00:00:00Z",
    opened_at_least: false,
    detail: { pct: 96, mount: "/srv/pool", free: 4 * 1024 ** 3 },
    measured_ts: "2026-08-10T01:00:00Z",
    stale: false,
    ...over,
  });

  // The evaluator declines to judge silence for fourteen minutes after a hub
  // restart (conditions.WarmUp), which is right for the LOG -- a false outage
  // there is permanent -- and wrong for a page, which states what is true now
  // and forgets it.
  //
  // Reading the tense off the `silent` condition would print "is 96% full" on
  // a machine that has been off since Tuesday, for fourteen minutes after
  // every hub restart, while the fleet row's own pill said offline beside it.
  // That is #92's disagreement rebuilt on a timer.
  it("dates the host from last_seen, not from a condition the hub may be withholding", () => {
    const conditions = [
      row({ id: 9, kind: "disk", detail: { pct: 96, mount: "/srv/pool" } }),
    ];
    const testee = needsAttention({
      ...quiet,
      host,
      // No `silent` row at all -- the hub is inside its warm-up.
      conditions,
      now: new Date("2026-08-13T01:00:00Z"),
    });

    // The page says so anyway, off the timestamp it already has.
    expect(testee[0]!.severity).toBe("critical");
    expect(String(testee[0]!.what)).toMatch(/last reported/);
    // And the tense follows it, so the two lines agree with each other.
    const disk = testee.find((a) => /srv\/pool/.test(String(a.what)))!;
    expect(String(disk.what)).toMatch(/was 96% full/);
  });

  // The hub refuses to raise this: a critical condition in the gap between
  // creating a host and installing its agent is false history in the log an
  // alerting engine reads. The page states what is true now and forgets it.
  it("says a never-reported host is critical, though the hub raised nothing", () => {
    const testee = needsAttention({
      ...quiet,
      host: { ...host, last_seen: null },
      conditions: [],
    });
    expect(testee).toEqual([
      { severity: "critical", what: "never reported", since: null },
    ]);
  });

  // The column that used to be empty on every line here, because nothing in
  // the browser could know: a derivation reading the current row has no memory
  // of when it first became true.
  it("prints how long a condition has been true", () => {
    const testee = needsAttention({
      ...quiet,
      host,
      conditions: [row()],
      now: new Date("2026-08-10T06:00:00Z"),
    });
    // The host is five hours past its last_seen at this instant, so the band
    // leads with that; the onset being asserted is the disk's own.
    const disk = testee.find((a) => /srv\/pool/.test(String(a.what)))!;
    expect(disk.since).toBe("2026-08-10T00:00:00Z");
    expect(disk.sinceAtLeast).toBe(false);
  });

  it("marks an onset the hub could only bound as a floor", () => {
    const testee = needsAttention({
      ...quiet,
      host,
      conditions: [row({ opened_at_least: true })],
      now: new Date("2026-08-10T06:00:00Z"),
    });
    const disk = testee.find((a) => /srv\/pool/.test(String(a.what)))!;
    expect(disk.sinceAtLeast).toBe(true);
  });

  // The tense, and only the tense. A host that is off keeps its disk figure
  // and keeps its severity -- a 96 % disk on a machine that is switched off is
  // still a 96 % disk, and it is worth clearing before the machine comes back.
  // What changes is that the sentence stops claiming to describe this minute.
  it("says a disk WAS full once the host has stopped reporting", () => {
    const offline = needsAttention({
      ...quiet,
      host,
      conditions: [
        row({ id: 2, kind: "silent", subject: "", detail: {} }),
        row(),
      ],
      now: new Date("2026-08-13T01:00:00Z"),
    });
    const disk = offline.find((a) => /\/srv\/pool/.test(String(a.what)));
    expect(String(disk?.what)).toMatch(/\/srv\/pool was 96% full/);
    expect(disk?.severity).toBe("critical");

    const live = needsAttention({
      ...quiet,
      host,
      conditions: [row()],
      now: new Date("2026-08-10T01:00:00Z"),
    });
    expect(String(live[0]?.what)).toMatch(/\/srv\/pool is 96% full/);
  });

  // One line per MOUNT, not the fullest one: the fleet page collapses because
  // there the unit of interest is the machine, and here it is the thing to go
  // and fix.
  it("lists every bad mount rather than the worst one", () => {
    const testee = needsAttention({
      ...quiet,
      host,
      conditions: [
        row({ subject: "pool", detail: { pct: 96, mount: "/srv/pool" } }),
        row({
          id: 2,
          subject: "var",
          severity: "warning",
          detail: { pct: 91, mount: "/var" },
        }),
      ],
      now: new Date("2026-08-10T01:00:00Z"),
    });
    expect(testee.map((a) => String(a.what))).toEqual([
      "/srv/pool is 96% full",
      "/var is 91% full",
    ]);
  });

  // Said rather than hidden. The page used to drop a mount whose reading had
  // stopped moving, which silently retired the condition on it -- and the hub
  // will not make that call at all, because a hung NFS export and an unmounted
  // volume are indistinguishable from where it stands.
  it("keeps a subject the hub can no longer measure, and says so", () => {
    const testee = needsAttention({
      ...quiet,
      host,
      conditions: [row({ stale: true, measured_ts: "2026-08-09T21:00:00Z" })],
      now: new Date("2026-08-10T01:00:00Z"),
    });
    expect(String(testee[0]!.what)).toMatch(/not measured since 4 h ago/);
  });

  // The units stay derived here, and that is not an oversight: the hub keeps
  // ONE failed-units condition per host because the fleet list counts hosts,
  // while this panel lists the units -- and the flapping ones, which are a
  // rate off the event log and not a condition at all.
  it("lists the failed units itself, dated from systemd's own timestamp", () => {
    const testee = needsAttention({
      ...quiet,
      host,
      conditions: [],
      units: [
        {
          id: 1,
          unit_name: "nginx.service",
          state: "failed",
          substate: "failed",
          since: "2026-08-09T22:00:00Z",
          restarts_1h: 0,
        },
      ],
      now: new Date("2026-08-10T01:00:00Z"),
    });
    expect(testee).toEqual([
      {
        severity: "warning",
        what: "nginx.service failed",
        since: "2026-08-09T22:00:00Z",
        mute: {
          kind: "failed-units",
          subject: "",
          open: false,
          reach: "Mutes every failed unit on this host.",
        },
      },
    ]);
  });

  // The deviation kinds read the same sentence the fleet page writes: the
  // reading, what it normally is, and whether it is still being measured.
  it("prints a deviation's reading and its normal, as the fleet page does", () => {
    const testee = needsAttention({
      ...quiet,
      host,
      conditions: [
        row({
          kind: "temperature",
          subject: "drivetemp/temp1/sda",
          severity: "warning",
          detail: { value: 61, normal: 46, source: "baseline", unit: "C" },
          stale: true,
          measured_ts: "2026-08-09T21:00:00Z",
        }),
        row({
          id: 2,
          kind: "load",
          subject: "",
          severity: "warning",
          detail: { value: 14.23, normal: 6.1, source: "baseline", unit: "" },
        }),
      ],
      now: new Date("2026-08-10T01:00:00Z"),
    });
    const what = testee.map((a) => String(a.what));
    expect(what[0]).toMatch(
      /^drivetemp\/temp1\/sda is 61 C — normally under 46 C/,
    );
    expect(what[0]).toMatch(/not measured since 4 h ago$/);
    expect(what[1]).toMatch(/is 14\.2 — normally under 6\.1$/);
  });

  // A deviation row whose detail lost its reading still appears.
  it("keeps a deviation row with no reading, named by the catalogue", () => {
    const testee = needsAttention({
      ...quiet,
      host,
      conditions: [
        row({
          kind: "processes",
          subject: "",
          severity: "warning",
          detail: {},
        }),
      ],
      now: new Date("2026-08-10T01:00:00Z"),
    });
    expect(String(testee[0]!.what)).toBe("process count above normal");
  });

  // A kind the hub raised that this file has no sentence for still appears.
  // Dropping it would be a host page reading clean because the browser did not
  // recognise what was wrong with it.
  it("shows a kind it has no sentence for, named by the catalogue", () => {
    const catalogue = catalogueOf([
      ...KINDS,
      { kind: "thermal", label: "Running hot", severity: "warning" },
    ]);
    const testee = needsAttention({
      ...quiet,
      catalogue,
      host,
      conditions: [
        row({ kind: "thermal", subject: "", severity: "warning", detail: {} }),
      ],
      now: new Date("2026-08-10T01:00:00Z"),
    });
    expect(String(testee[0]!.what)).toBe("running hot");
  });
});

// The panel this tab is judged on used to read clean on a host whose Drives
// table, one tab away, was showing a critical disk in red.
describe("the attention band and the host's drives", () => {
  const quiet = {
    agentMetrics: null,
    hostMetrics: null,
    units: null,
    catalogue: CATALOGUE,
  };
  const drive = (
    device: string,
    text: string,
    over: Partial<ConditionRow> = {},
  ): ConditionRow => ({
    id: device.charCodeAt(2),
    host_id: host.id,
    hostname: host.hostname,
    kind: "drive",
    subject: device,
    severity: "critical",
    opened_ts: "2026-08-10T00:00:00Z",
    opened_at_least: false,
    detail: { device, text, alarms: 1, urgency: 0 },
    measured_ts: "2026-08-10T01:00:00Z",
    stale: false,
    ...over,
  });

  // One line per device: this panel is what to DO, and two disks are two
  // replacements. The fleet page is the one that collapses them, because
  // there the unit of interest is the machine.
  it("names each failing drive, one line per thing to replace", () => {
    const testee = needsAttention({
      ...quiet,
      host,
      conditions: [
        drive("sda", "3 pending sectors"),
        drive("sdb", "12 reallocated sectors"),
      ],
      now: new Date("2026-08-10T01:00:00Z"),
    });
    expect(testee).toEqual([
      {
        severity: "critical",
        what: "sda — 3 pending sectors",
        since: null,
        sinceAtLeast: false,
        mute: { kind: "drive", subject: "sda", open: true },
      },
      {
        severity: "critical",
        what: "sdb — 12 reallocated sectors",
        since: null,
        sinceAtLeast: false,
        mute: { kind: "drive", subject: "sdb", open: true },
      },
    ]);
  });

  // SMART attributes are counters with no zero baseline, sampled hourly: the
  // first non-zero reading netra holds is when netra started LOOKING, not when
  // the sector went bad.
  it("dates no drive, however old the condition is", () => {
    const testee = needsAttention({
      ...quiet,
      host,
      conditions: [drive("sda", "3 pending sectors")],
      now: new Date("2026-08-10T01:00:00Z"),
    });
    expect(testee[0]!.since).toBeNull();
  });
});

// A mute is set on the alert it silences, so only a line a notification
// stands behind offers one.
describe("muting from the attention band", () => {
  const quiet = { hostMetrics: null, units: null, catalogue: CATALOGUE };
  const cond = (over: Partial<ConditionRow>): ConditionRow => ({
    id: 1,
    host_id: host.id,
    hostname: host.hostname,
    kind: "disk",
    subject: "pool",
    severity: "critical",
    opened_ts: "2026-08-10T00:00:00Z",
    opened_at_least: false,
    detail: { pct: 96, mount: "/srv/pool" },
    measured_ts: "2026-08-10T01:00:00Z",
    stale: false,
    ...over,
  });
  const unit = (over: Partial<Unit>): Unit => ({
    id: 1,
    unit_name: "backup.service",
    state: "failed",
    substate: "failed",
    since: null,
    restarts_1h: 0,
    ...over,
  });

  it("targets the stored subject, not the label the line prints", () => {
    const testee = needsAttention({
      ...quiet,
      host,
      conditions: [cond({})],
      now: new Date("2026-08-10T01:00:30Z"),
    });
    expect(testee[0]!.mute).toEqual({
      kind: "disk",
      subject: "pool",
      open: true,
    });
  });

  // The NAS that is off most days: its line comes from last_seen, and the
  // hub's `silent` condition may not exist yet (warm-up).
  it("mutes a host that stopped reporting by its silent condition", () => {
    const later = new Date("2026-08-13T01:00:00Z");
    const without = needsAttention({
      ...quiet,
      host,
      conditions: [],
      now: later,
    });
    expect(without[0]!.mute).toEqual({
      kind: "silent",
      subject: "",
      open: false,
    });
    const withRow = needsAttention({
      ...quiet,
      host,
      conditions: [cond({ kind: "silent", subject: "", detail: {} })],
      now: later,
    });
    expect(withRow[0]!.mute).toEqual({
      kind: "silent",
      subject: "",
      open: true,
    });
  });

  it("offers nothing where nobody is notified, and says a unit's mute covers all", () => {
    const never = needsAttention({
      ...quiet,
      host: { ...host, last_seen: null },
      conditions: [],
    });
    expect(never[0]!.mute).toBeUndefined();

    const units = needsAttention({
      ...quiet,
      host,
      units: [
        unit({}),
        unit({
          id: 2,
          unit_name: "web.service",
          state: "active",
          restarts_1h: 9,
        }),
      ],
      conditions: [cond({ kind: "failed-units", subject: "", detail: {} })],
      now: new Date("2026-08-10T01:00:30Z"),
    });
    const failed = units.find((a) => /backup/.test(String(a.what)))!;
    expect(failed.mute).toMatchObject({ kind: "failed-units", open: true });
    expect(failed.mute?.reach).toMatch(/every failed unit/);
    const flapping = units.find((a) => /web\.service/.test(String(a.what)))!;
    expect(flapping.mute).toBeUndefined();
  });

  it("draws a mute on each alert line and on critical events that notify", async () => {
    const mutes = {
      mutes: [],
      loadError: null,
      liftErrors: [],
      lifting: new Set<number>(),
      lift: async () => {},
      add: () => {},
      refresh: () => {},
    };
    const event = (over: Partial<Event>): Event =>
      ({
        id: "e:1",
        host_id: host.id,
        hostname: host.hostname,
        ts: "2026-08-10T00:59:00Z",
        type: "disk_error",
        subject: "sda",
        detail: {},
        severity: "critical",
        ...over,
      }) as Event;
    renderOverview({
      conditions: [cond({})],
      events: [
        event({}),
        event({
          id: "e:2",
          type: "link_change",
          subject: "eth0",
          severity: "warning",
        }),
      ],
      mutes,
    });
    const band = screen.getByRole("region", { name: "Needs attention" });
    expect(
      within(band).getByRole("button", {
        name: "Mute Filesystem nearly full — pool",
      }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Mute disk_error — sda" }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /Mute link_change/ }),
    ).not.toBeInTheDocument();
  });
});
