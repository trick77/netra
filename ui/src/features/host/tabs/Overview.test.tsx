import { describe, expect, it } from "vitest";
import { render, screen, within } from "@testing-library/react";
import type { HostDetail, MetricsResponse, Unit } from "../../../lib/api";
import { ABSENT } from "../../../lib/format";
import { Overview, filesystemRows, needsAttention } from "./Overview";
import { DISK_WARN_PCT, DISK_CRIT_PCT } from "../../fleet/conditions";
import { STALE_THRESHOLD_MS } from "../../../lib/host";

const host: HostDetail = {
  id: 7,
  hostname: "kessel",
  site_id: 3,
  last_seen: "2026-08-10T01:00:00Z",
  cpu_total: 12,
  mem_used: 4_000_000_000,
  mem_total: 8_000_000_000,
  uptime_s: 86_400,
  net_rx_bytes: 1.5e6,
  net_tx_bytes: 4e5,
  services_total: 397,
  services_failed: 1,
  site_name: "Zurich",
  provider_name: "Hetzner",
  fingerprint: "fp",
  host_type: "vps",
  agent_version: "0.4.1",
  go_version: "go1.25",
  build_commit: "abc1234",
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

function agentMetrics(dropped: number) {
  return response({
    family: "agent",
    columns: ["buffer_depth", "buffer_dropped_total", "post_failures_total"],
    series: [{ key: {}, points: [[1_754_784_000_000, 2, dropped, 0]] }],
  });
}

function renderOverview(over: Partial<Parameters<typeof Overview>[0]> = {}) {
  return render(
    <Overview
      host={host}
      hostMetrics={hostMetrics(null, null)}
      filesystemMetrics={fsMetrics}
      agentMetrics={agentMetrics(0)}
      sensorMetrics={null}
      containers={[]}
      units={[]}
      now={new Date("2026-08-10T01:00:30Z")}
      {...over}
    />,
  );
}

// post_failures_total is cumulative for the life of the agent PROCESS and is
// never reset by a success, so it needs the same window-relative reading as
// oom_kill_total. Two points, so counterIncrease has a pair to difference.
function deliveryFailures(points: [number, number][]) {
  return response({
    family: "agent",
    columns: ["buffer_depth", "buffer_dropped_total", "post_failures_total"],
    series: [{ key: {}, points: points.map(([ts, n]) => [ts, 2, 0, n]) }],
  });
}

describe("Overview delivery failures", () => {
  // The bug this replaced: a hub restart produced one failed post, the agent
  // re-sent that 1 on every scrape for the rest of its life, and the page
  // carried "1 failed deliveries to the hub" permanently -- even though the
  // ring buffer replayed the samples and nothing was lost.
  it("clears once the failures fall outside the window", () => {
    renderOverview({
      agentMetrics: deliveryFailures([
        [1_786_320_000_000, 1],
        [1_786_320_060_000, 1],
      ]),
    });
    expect(screen.queryByText(/failed deliver/i)).toBeNull();
  });

  it("reports failures that happened inside the window", () => {
    renderOverview({
      agentMetrics: deliveryFailures([
        [1_786_320_000_000, 1],
        [1_786_320_060_000, 4],
      ]),
    });
    const attention = screen.getByRole("region", { name: /needs attention/i });
    expect(
      within(attention).getByText(/3 failed deliveries to the hub/i),
    ).toBeInTheDocument();
  });

  // "1 failed deliveries" was the old copy, and it was wrong twice over.
  it("says delivery, singular, for a single failure", () => {
    renderOverview({
      agentMetrics: deliveryFailures([
        [1_786_320_000_000, 0],
        [1_786_320_060_000, 1],
      ]),
    });
    expect(
      screen.getByText(/1 failed delivery to the hub in this window/i),
    ).toBeInTheDocument();
  });

  // The counter zeroes when the agent process restarts. counterDeltas drops
  // a negative step, so the restart is skipped rather than counted.
  it("does not read an agent restart as a burst of failures", () => {
    renderOverview({
      agentMetrics: deliveryFailures([
        [1_786_320_000_000, 900],
        [1_786_320_060_000, 0],
      ]),
    });
    expect(screen.queryByText(/failed deliver/i)).toBeNull();
  });
});

describe("Overview system facts", () => {
  // GOOS is a build constant, so an agent that could not read /etc/os-release
  // falls back to it and the page used to print the compiler's token.
  it("names the operating system rather than printing GOOS", () => {
    renderOverview({ host: { ...host, os_name: "linux" } });
    const system = screen.getByRole("region", { name: "System" });
    expect(within(system).getByText("Linux")).toBeInTheDocument();
  });

  it("does not title-case its way to Darwin and Freebsd", () => {
    renderOverview({ host: { ...host, os_name: "darwin" } });
    expect(
      within(screen.getByRole("region", { name: "System" })).getByText("macOS"),
    ).toBeInTheDocument();
  });

  // A distro string is already the right answer and must pass through.
  it("leaves a distribution name alone", () => {
    renderOverview({ host: { ...host, os_name: "Debian GNU/Linux 13" } });
    expect(screen.getByText("Debian GNU/Linux 13")).toBeInTheDocument();
  });

  // ~32 GiB of MemTotal. Decimally this reads "33.3 GB", which is above the
  // capacity the machine actually has.
  it("states installed memory in the binary units it was sold in", () => {
    renderOverview({ host: { ...host, memory_total: 33_260_000_000 } });
    const system = screen.getByRole("region", { name: "System" });
    expect(within(system).getByText("31 GiB")).toBeInTheDocument();
    expect(within(system).queryByText(/33.3 GB/)).toBeNull();
  });
});

describe("Overview", () => {
  it("renders an absent swap as none, never as 0", () => {
    renderOverview();
    const memory = screen.getByRole("region", { name: "Memory" });
    expect(within(memory).getByText(/none/i)).toBeInTheDocument();
    expect(within(memory).queryByText(/\b0 B\b/)).toBeNull();
  });

  it("distinguishes swap that exists and is unused from swap that does not exist", () => {
    renderOverview({ hostMetrics: hostMetrics(2_000_000_000, 0) });
    const memory = screen.getByRole("region", { name: "Memory" });
    expect(within(memory).queryByText(/none/i)).toBeNull();
  });

  it("shows disk as absolute bytes per filesystem, never as a ratio", () => {
    renderOverview();
    const disk = screen.getByRole("region", { name: /disk/i });
    expect(within(disk).getByText("/")).toBeInTheDocument();
    expect(within(disk).getByText(/40 GB used/)).toBeInTheDocument();
    expect(disk.textContent).not.toMatch(/%/);
  });

  it("raises a dropped-sample count into needs-attention, with a word beside the dot", () => {
    renderOverview({ agentMetrics: agentMetrics(12) });
    const attention = screen.getByRole("region", { name: /needs attention/i });
    expect(within(attention).getByText(/critical/i)).toBeInTheDocument();
    expect(within(attention).getByText(/12/)).toBeInTheDocument();
  });

  // The band is the first thing on the tab and spans the page, so a healthy
  // host must not spend that position on a box saying nothing. One quiet line
  // confirms the check ran instead -- the same rule the fleet band follows.
  // .grid2 is a CSS multi-column flow, so a card placed first inside it only
  // reaches the top of the LEFT column. The band has to be outside the grid
  // altogether to span the page and be read first.
  it("puts the band above the card grid, not inside it", () => {
    const { container } = renderOverview({ agentMetrics: agentMetrics(12) });
    const band = screen.getByRole("region", { name: /needs attention/i });
    const grid = container.querySelector(".grid2");
    expect(grid).not.toBeNull();
    expect(grid?.contains(band)).toBe(false);
    expect(
      band.compareDocumentPosition(grid!) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
  });

  // The System card is lifted out for a different reason: a multi-column flow
  // has no top-right slot to reorder a card into, so full width above the flow
  // is the only position that holds. Inside .grid2 it drifts with the break
  // point the browser picks from content height.
  it("puts the System card above the card grid, not inside it", () => {
    const { container } = renderOverview();
    const system = screen.getByRole("region", { name: "System" });
    const grid = container.querySelector(".grid2");
    expect(grid).not.toBeNull();
    expect(grid?.contains(system)).toBe(false);
    expect(
      system.compareDocumentPosition(grid!) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
  });

  // The only link between the hoisted card and the CSS that lays it out two
  // pairs across. Without it the card is a thin column of eight one-line rows
  // down the left of a full-width card -- which renders perfectly, passes
  // every other test here, and is exactly what the hoist was meant to fix.
  it("lays the System card's facts out two pairs across", () => {
    renderOverview();
    const system = screen.getByRole("region", { name: "System" });
    expect(system.querySelector("dl")?.className).toContain("wide");
  });

  // Traffic took the slot System left: first in the flow is the top of the
  // LEFT column, and the network is the subsystem most likely to explain a
  // problem.
  it("leads the card grid with Traffic", () => {
    const { container } = renderOverview();
    const grid = container.querySelector(".grid2");
    const first = grid?.firstElementChild;
    expect(first?.getAttribute("aria-label")).toBe("Traffic");
  });

  it("says so in one line when nothing is wrong, without the band", () => {
    renderOverview();
    expect(
      screen.queryByRole("region", { name: /needs attention/i }),
    ).toBeNull();
    expect(screen.getByText(/nothing needs attention/i)).toBeInTheDocument();
  });

  it("summarises the tabs instead of duplicating them", () => {
    renderOverview({
      containers: [
        {
          id: 1,
          container_key: "proj/web",
          name: "web",
          image: "nginx",
          is_agent: false,
        },
        {
          id: 2,
          container_key: "proj/db",
          name: "db",
          image: "pg",
          is_agent: false,
        },
      ],
      units: [
        {
          id: 1,
          unit_name: "ssh.service",
          state: "active",
          substate: "running",
          since: null,
          restarts_1h: 0,
        },
        {
          id: 2,
          unit_name: "cron.service",
          state: "failed",
          substate: "dead",
          since: null,
          restarts_1h: 0,
        },
      ],
    });
    // A count and a link, not the inventory itself.
    expect(screen.getByText("2 containers")).toBeInTheDocument();
    expect(screen.queryByText("nginx")).toBeNull();
    // The host's OWN service counts, not the length of `units`. The units
    // endpoint returns only what needs attention -- two rows here -- so
    // counting it would report "2 units" for a host running 397.
    expect(screen.getByText("397 units \u00b7 1 failed")).toBeInTheDocument();
  });

  it("reads the sensor family's temp column", () => {
    renderOverview({
      sensorMetrics: response({
        family: "sensor",
        // The window has to contain the point. Every sensor row now reads
        // its number off the SAME gridded array its sparkline draws, so a
        // fixture whose sample sits outside its own window grids to all
        // null and the row correctly reads absent -- which is what these
        // fixtures used to hide by taking the number from the ungridded
        // series while the sparkline beside it drew nothing.
        window: { from: "2025-08-10T00:00:00Z", to: "2025-08-10T00:05:00Z" },
        requested_window: {
          from: "2025-08-10T00:00:00Z",
          to: "2025-08-10T00:05:00Z",
        },
        key_columns: ["chip", "label"],
        columns: ["temp"],
        series: [
          {
            key: { chip: "coretemp", label: "Package id 0" },
            points: [[1_754_784_000_000, 47.5]],
          },
        ],
      }),
    });
    const temperature = screen.getByRole("region", { name: /temperature/i });
    expect(
      within(temperature).getByText("coretemp Package id 0"),
    ).toBeInTheDocument();
    expect(within(temperature).getByText("48 °C")).toBeInTheDocument();
  });

  // The sensor family carries fans, voltages, currents and power now, and
  // only temperatures have a temp column. Mapping the whole family would fill
  // a panel headed "Temperature" with rows reading "nct6775 fan1 —", burying
  // the readings it exists to show under ones it cannot render.
  it("shows only temperature sensors, not the fans and rails beside them", () => {
    renderOverview({
      sensorMetrics: response({
        family: "sensor",
        window: { from: "2025-08-10T00:00:00Z", to: "2025-08-10T00:05:00Z" },
        requested_window: {
          from: "2025-08-10T00:00:00Z",
          to: "2025-08-10T00:05:00Z",
        },
        key_columns: ["chip", "label", "kind"],
        columns: ["temp", "value"],
        series: [
          {
            key: { chip: "nct6775", label: "CPU", kind: "temperature" },
            points: [[1_754_784_000_000, 45, 45]],
          },
          {
            // temp is null: a fan has no temperature.
            key: { chip: "nct6775", label: "CPU Fan", kind: "fan" },
            points: [[1_754_784_000_000, null, 1200]],
          },
          {
            key: { chip: "nct6775", label: "+12V", kind: "voltage" },
            points: [[1_754_784_000_000, null, 12.1]],
          },
        ],
      }),
    });

    const temperature = screen.getByRole("region", { name: /temperature/i });
    expect(within(temperature).getByText("nct6775 CPU")).toBeInTheDocument();
    expect(within(temperature).getByText("45 °C")).toBeInTheDocument();
    // The non-temperature series must not appear in THIS panel -- an empty
    // row is worse than an absent one, because it reads as a broken sensor.
    expect(within(temperature).queryByText("nct6775 CPU Fan")).toBeNull();
    expect(within(temperature).queryByText("nct6775 +12V")).toBeNull();

    // They are not discarded, though: each kind gets its own card, in its
    // own unit, on its own scale. Putting a 1200 RPM fan on the same axis as
    // a 45 °C package is the reason they are separated rather than merged.
    const fans = screen.getByRole("region", { name: "Fans" });
    expect(within(fans).getByText("nct6775 CPU Fan")).toBeInTheDocument();
    expect(within(fans).getByText("1200 RPM")).toBeInTheDocument();

    const power = screen.getByRole("region", { name: "Power" });
    expect(within(power).getByText("nct6775 +12V")).toBeInTheDocument();
    expect(within(power).getByText("12.10 V")).toBeInTheDocument();
  });

  // A host with no fans -- every VM, every cloud instance -- must not carry
  // an empty "Fans" card. A card that is blank on most of the fleet teaches
  // people to stop reading this column.
  it("omits the fan and power cards on a host that reports neither", () => {
    renderOverview({
      sensorMetrics: response({
        family: "sensor",
        window: { from: "2025-08-10T00:00:00Z", to: "2025-08-10T00:05:00Z" },
        requested_window: {
          from: "2025-08-10T00:00:00Z",
          to: "2025-08-10T00:05:00Z",
        },
        key_columns: ["chip", "label", "kind"],
        columns: ["temp", "value"],
        series: [
          {
            key: {
              chip: "coretemp",
              label: "Package id 0",
              kind: "temperature",
            },
            points: [[1_754_784_000_000, 45, 45]],
          },
        ],
      }),
    });

    expect(
      screen.getByRole("region", { name: /temperature/i }),
    ).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "Fans" })).toBeNull();
    expect(screen.queryByRole("region", { name: "Power" })).toBeNull();
  });

  // A fan's failure is its minimum. Averaged across a five-minute bucket a
  // stall is invisible -- the mean of a stopped fan and a spin-up is a
  // perfectly healthy number -- so the fan row must read value_min at the
  // rolled tiers, and must not fall back to the _avg that candidates()
  // prefers.
  it("reads a fan from value_min, not the average that hides a stall", () => {
    renderOverview({
      sensorMetrics: response({
        family: "sensor",
        tier: "5m",
        step_s: 300,
        window: { from: "2025-08-10T00:00:00Z", to: "2025-08-10T00:10:00Z" },
        requested_window: {
          from: "2025-08-10T00:00:00Z",
          to: "2025-08-10T00:10:00Z",
        },
        key_columns: ["chip", "label", "kind"],
        columns: ["value_avg", "value_max", "value_min"],
        series: [
          {
            key: { chip: "nct6775", label: "fan2", kind: "fan" },
            // A bucket the fan spent partly stopped: the average and the
            // maximum both look fine, and only the minimum says so.
            points: [[1_754_784_300_000, 1180, 1400, 0]],
          },
        ],
      }),
    });

    const fans = screen.getByRole("region", { name: "Fans" });
    expect(within(fans).getByText("0 RPM")).toBeInTheDocument();
    expect(within(fans).queryByText("1180 RPM")).toBeNull();
  });

  it("reports a collector that is not running, with its reason", () => {
    renderOverview();
    expect(screen.getByText("not permitted")).toBeInTheDocument();
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

describe("filesystemRows", () => {
  it("returns nothing rather than guessing when the tier lacks the columns", () => {
    expect(filesystemRows(response({ family: "filesystem" }))).toEqual([]);
    expect(filesystemRows(null)).toEqual([]);
  });

  it("keeps used and free as measured, because they do not sum to total", () => {
    const [row] = filesystemRows(fsMetrics);
    expect(row).toMatchObject({
      label: "/",
      used: 40_000_000_000,
      free: 55_000_000_000,
      total: 100_000_000_000,
    });
  });

  // A filesystem that stopped being reported has no fullness NOW, and saying
  // it does is what put one disk on the page twice: /netra/fs/ark frozen at
  // the moment its agent was upgraded, beside the /mnt/ark that replaced it,
  // both at 94 %, neither marked as the past.
  //
  // read/metrics.go emits only the rows that exist, so the retired series
  // just ends early -- its last element is a real reading. Only the window's
  // grid can tell "last thing it said" from "what it says now".
  it("reports no reading for a filesystem that stopped mid-window", () => {
    const retired = response({
      family: "filesystem",
      key_columns: ["filesystem", "mountpoint"],
      columns: ["total", "used", "free"],
      series: [
        {
          // The one that stopped: a reading in the first bucket of the
          // window and nothing after it.
          key: { filesystem: "/netra/fs/ark" },
          points: [
            [
              Date.parse("2026-08-10T00:00:00Z"),
              100_000_000_000,
              94_000_000_000,
              6_000_000_000,
            ],
          ],
        },
        {
          // The one that replaced it, reporting into the final bucket.
          key: { filesystem: "ark", mountpoint: "/mnt/ark" },
          points: [
            [
              Date.parse("2026-08-10T00:59:00Z"),
              100_000_000_000,
              94_000_000_000,
              6_000_000_000,
            ],
          ],
        },
      ],
    });

    const rows = filesystemRows(retired);
    expect(rows).toHaveLength(2);
    // Still listed -- the disk existed, and dropping the row would claim it
    // never did. It is its NUMBERS that stop claiming to be current, which is
    // what diskWarnings skips on.
    expect(rows[0]).toMatchObject({
      label: "/netra/fs/ark",
      total: null,
      used: null,
      free: null,
    });
    expect(rows[1]).toMatchObject({
      label: "/mnt/ark",
      used: 94_000_000_000,
      free: 6_000_000_000,
    });
  });
});

describe("Overview processor panel", () => {
  // The rollup tiers carry cpu_total and not the four per-state columns, so
  // above an hour this -- the headline chart on the page -- had nothing to
  // draw and rendered as not-collected, while the fleet row for the same
  // host drew a silhouette from the total. The two must not disagree about
  // whether a host's CPU can be drawn.
  it("draws the total as one band when the tier has no per-state breakdown", () => {
    render(
      <Overview
        host={host}
        filesystemMetrics={null}
        agentMetrics={null}
        sensorMetrics={null}
        containers={null}
        units={null}
        hostMetrics={response({
          family: "host",
          tier: "5m",
          columns: ["cpu_total_avg"],
          series: [
            {
              key: {},
              points: [
                [1_754_784_000_000, 30],
                [1_754_784_300_000, 35],
              ],
            },
          ],
        })}
      />,
    );

    // Scoped to the Processor panel: the page carries other panels now, and
    // at this tier some of them legitimately say "Not collected". The claim
    // here is about THIS chart falling back to a total band.
    const panel = screen.getByRole("region", { name: "Processor chart" });
    expect(panel).toBeInTheDocument();
    expect(within(panel).queryByText("Not collected")).toBeNull();
  });

  // net_rx and net_tx are BYTES per second: network.go computes them as
  // (rxBytes - prev) / elapsed. Rendered through bitrate() the card read 8x
  // low and entirely plausible -- a 2 MB/s link showed as "2 Mb/s" -- and
  // the fleet's traffic cell carried the identical bug, so nothing on screen
  // contradicted it.
  it("shows traffic in bytes per second, not bits", () => {
    renderOverview({
      netMetrics,
      host: { ...host, net_rx_bytes: 2_000_000, net_tx_bytes: 500_000 },
    });
    const traffic = screen.getByRole("region", { name: "Traffic" });

    expect(within(traffic).getByText(/2 MB\/s/)).toBeInTheDocument();
    expect(within(traffic).getByText(/500 kB\/s/)).toBeInTheDocument();
    expect(traffic.textContent).not.toMatch(/b\/s/);
  });

  // A host that stopped reporting must read as absent, never as the last
  // rate it ever sent. This card scanned backwards for the last non-null,
  // so a dead agent's traffic sat frozen at its final value -- while the
  // fleet's traffic cell, reading the latest bucket, showed the same host as
  // absent. "The agent is down" must not render as "traffic is steady".
  //
  // The rule survives the move to a gauge: host_current's columns are NULL
  // for a host that has never reported traffic, and the upsert only ever
  // writes them from a post that actually carried net samples.
  it("reads a host that stopped reporting as absent, not as its last known rate", () => {
    const stale = response({
      family: "net",
      key_columns: ["interface"],
      columns: ["rx_bytes", "tx_bytes"],
      series: [
        {
          key: { interface: "eth0" },
          // A real reading early in the window and nothing since: every
          // later bucket grids to null.
          points: [[1_786_321_800_000, 2_000_000, 500_000]],
        },
      ],
    });

    renderOverview({
      netMetrics: stale,
      host: { ...host, net_rx_bytes: null, net_tx_bytes: null },
    });
    const traffic = screen.getByRole("region", { name: "Traffic" });

    expect(traffic.textContent).not.toMatch(/MB\/s/);
    expect(
      within(traffic).getAllByText(new RegExp(ABSENT)).length,
    ).toBeGreaterThan(0);
  });

  // The reported bug, on this page's copy of the number. The sparkline is
  // drawn from the series and follows the range; the rates beside it are the
  // gauge and do not. Reading the series here made "now" mean a different
  // instant at 1h than at 6h.
  it("reads the gauge rather than the end of the series", () => {
    renderOverview({
      netMetrics,
      host: { ...host, net_rx_bytes: 9_000_000, net_tx_bytes: 9_000_000 },
    });
    const traffic = screen.getByRole("region", { name: "Traffic" });

    expect(within(traffic).getAllByText(/9 MB\/s/).length).toBe(2);
  });

  // The same rule as before the move to a gauge, now that the gauge is what
  // could break it: host_current keeps a dead host's last pair for as long
  // as the row exists, so the card gates on the host still reporting. A
  // frozen rate here would sit under a header already saying "offline".
  it("blanks the rates of a host that stopped reporting, gauge or not", () => {
    renderOverview({
      netMetrics,
      host: {
        ...host,
        last_seen: "2026-08-10T00:00:00Z",
        net_rx_bytes: 9_000_000,
        net_tx_bytes: 9_000_000,
      },
    });
    const traffic = screen.getByRole("region", { name: "Traffic" });

    expect(traffic.textContent).not.toMatch(/MB\/s/);
    expect(
      within(traffic).getAllByText(new RegExp(ABSENT)).length,
    ).toBeGreaterThan(0);
  });
});

// The exhaustion gauges. Nothing else in netra answers "am I about to hit a
// limit", and running out does not present as a resource problem: accept()
// starts failing and conntrack drops flows while the CPU and memory cards
// stay calm.
describe("Overview limits card", () => {
  function limitsResponse(
    over: Partial<MetricsResponse> = {},
  ): MetricsResponse {
    return {
      family: "host",
      tier: "raw",
      step_s: 60,
      // A one-bucket window, so the sample below IS the latest bucket. The
      // gauge deliberately reads the latest bucket rather than the last
      // number the host ever sent: a host that stopped reporting an hour
      // ago must read absent, not "comfortably at 40%".
      window: { from: "2026-08-10T00:00:00Z", to: "2026-08-10T00:01:00Z" },
      requested_window: {
        from: "2026-08-10T00:00:00Z",
        to: "2026-08-10T00:01:00Z",
      },
      warnings: [],
      key_columns: [],
      columns: ["fd_used", "fd_limit", "conntrack_count", "conntrack_limit"],
      series: [
        {
          key: {},
          points: [[1_786_320_000_000, 48231, 262144, 1800, 262144]],
        },
      ],
      truncated: false,
      ...over,
    };
  }

  it("shows a gauge against its ceiling, because the ratio is the story", () => {
    renderOverview({ hostMetrics: limitsResponse() });
    const limits = screen.getByRole("region", { name: "Limits" });
    // Grouped digits, not a rounded magnitude: "48 k of 262 k" throws away
    // the only digits that separate comfortable from nearly-full.
    expect(within(limits).getByText(/48 231 of 262 144/)).toBeInTheDocument();
  });

  // At a rolled tier the mean hides the moment that matters: accept() fails
  // at the peak, not at the average. candidates() prefers _avg, so the peak
  // has to be asked for by name.
  it("reads the peak at a rolled tier, not the average", () => {
    renderOverview({
      hostMetrics: limitsResponse({
        tier: "5m",
        step_s: 300,
        columns: ["fd_used_avg", "fd_used_max", "fd_limit"],
        series: [
          {
            key: {},
            points: [[1_786_320_000_000, 40000, 250000, 262144]],
          },
        ],
      }),
    });
    const limits = screen.getByRole("region", { name: "Limits" });
    expect(within(limits).getByText(/250 000 of 262 144/)).toBeInTheDocument();
    expect(within(limits).queryByText(/40 000/)).toBeNull();
  });

  // The capability the agent reported, in place of the meter it explains.
  // An em-dash next to a bar that never fills is indistinguishable from a
  // broken collector.
  // /proc/sys/fs/file-max is int64 max on a great many hosts. A bar against
  // 9.2 quintillion can never move, and past Number.MAX_SAFE_INTEGER the
  // figure has already lost precision in transit -- so the ratio is both
  // useless and wrong.
  it("says no limit rather than drawing a ratio against an unbounded ceiling", () => {
    renderOverview({
      hostMetrics: limitsResponse({
        columns: ["fd_used", "fd_limit"],
        series: [
          {
            key: {},
            points: [[1_786_320_000_000, 3352, 9223372036854775807]],
          },
        ],
      }),
    });
    const limits = screen.getByRole("region", { name: "Limits" });
    expect(within(limits).getByText(/3 352 · no limit/)).toBeInTheDocument();
    // The number the host never reported must not appear.
    expect(within(limits).queryByText(/776 000/)).toBeNull();
  });

  it("says why a gauge is missing when the agent explained it", () => {
    renderOverview({
      host: {
        ...host,
        capabilities: { ...host.capabilities, conntrack: "unavailable" },
      },
      hostMetrics: limitsResponse(),
    });
    const limits = screen.getByRole("region", { name: "Limits" });
    expect(within(limits).getByText("unavailable")).toBeInTheDocument();
  });

  // sockets_used and tcp_alloc have no ceiling in the schema, so they cannot
  // answer the headroom question this card exists for. They are deliberately
  // not here, and this pins that decision rather than leaving it to be
  // "fixed" later.
  it("carries no row for a gauge that has no ceiling", () => {
    renderOverview({ hostMetrics: limitsResponse() });
    const limits = screen.getByRole("region", { name: "Limits" });
    expect(within(limits).queryByText(/sockets used|tcp alloc/i)).toBeNull();
  });

  it("is absent entirely on a host that reported no limits at all", () => {
    renderOverview();
    expect(screen.queryByRole("region", { name: "Limits" })).toBeNull();
  });
});

// An OOM kill is the one memory fact no chart can carry: mem_used is back to
// normal by the time anyone looks, precisely BECAUSE the kill happened.
describe("Overview OOM attention", () => {
  function oomResponse(points: (number | null)[][]): MetricsResponse {
    return {
      family: "host",
      tier: "raw",
      step_s: 60,
      window: { from: "2026-08-10T00:00:00Z", to: "2026-08-10T00:05:00Z" },
      requested_window: {
        from: "2026-08-10T00:00:00Z",
        to: "2026-08-10T00:05:00Z",
      },
      warnings: [],
      key_columns: [],
      columns: ["oom_kill_total"],
      series: [{ key: {}, points }],
      truncated: false,
    };
  }

  it("reports kills that happened inside the window", () => {
    renderOverview({
      hostMetrics: oomResponse([
        [1_786_320_000_000, 4],
        [1_786_320_060_000, 6],
      ]),
    });
    const attention = screen.getByRole("region", { name: /attention/i });
    expect(within(attention).getByText(/2 OOM kills/)).toBeInTheDocument();
  });

  // The counter is cumulative since boot. A host that killed something a
  // year ago and nothing since is healthy, and must not carry a permanent
  // badge -- which is what reading the raw total would do.
  it("stays silent for a counter that is high but flat", () => {
    renderOverview({
      hostMetrics: oomResponse([
        [1_786_320_000_000, 4000],
        [1_786_320_060_000, 4000],
      ]),
    });
    // Nothing wrong at all, so there is no band to look inside: its absence
    // is the assertion.
    expect(screen.queryByRole("region", { name: /attention/i })).toBeNull();
    expect(screen.queryByText(/OOM/)).toBeNull();
  });
});

// "0.4.1" does not identify a build -- it is whatever was last tagged, and
// the agent in front of you may be a rebuild or a patched branch. The commit
// is what makes the answer exact, and it was collected and served all along
// while only the version was shown.
describe("Overview system card", () => {
  it("identifies the reporting agent by version and commit", () => {
    renderOverview();
    const system = screen.getByRole("region", { name: "System" });
    expect(within(system).getByText("0.4.1 · abc1234")).toBeInTheDocument();
  });

  // buildinfo.Commit() is "unknown" for a binary built without the ldflags
  // stamp -- a plain `go build` from a working tree. That is a fact about how
  // the agent was compiled, not a value worth printing: "0.4.1 · unknown"
  // reads as a bug in netra.
  it("falls back to the version alone for an unstamped build", () => {
    renderOverview({ host: { ...host, build_commit: "unknown" } });
    const system = screen.getByRole("region", { name: "System" });
    expect(within(system).getByText("0.4.1")).toBeInTheDocument();
    expect(within(system).queryByText(/unknown/)).toBeNull();
  });

  it("reads absent, never empty, when the host reported no agent at all", () => {
    renderOverview({
      host: { ...host, agent_version: null, build_commit: null },
    });
    const system = screen.getByRole("region", { name: "System" });
    const agent = within(system).getByText("Agent").nextElementSibling;
    expect(agent?.textContent).toBe(ABSENT);
  });
});

// The two facts this page and the fleet band have to agree on. Both used to
// be written twice -- the severity as a different word, the thresholds as
// bare numbers -- and both are now single-sourced. These tests pin the
// agreement rather than the constants: they import the same values the fleet
// band imports, so a threshold that moves has to move on both pages or one
// of these fails.
describe("needsAttention agrees with the fleet band", () => {
  const quiet = {
    agentMetrics: null,
    hostMetrics: null,
    filesystems: [],
    units: null,
  };

  it("calls a host that has never reported critical, the fleet's word for it", () => {
    // Given a host the hub has never heard from
    const testee = needsAttention({
      ...quiet,
      host: { ...host, last_seen: null },
    });

    // Then it is critical -- hostConditions() rates the same fact critical
    expect(testee).toEqual([{ severity: "critical", what: "never reported" }]);
  });

  it("calls a host that stopped reporting critical too", () => {
    // Given a host last seen well beyond the stale cutoff
    const testee = needsAttention({
      ...quiet,
      host: { ...host, last_seen: "2026-08-10T00:00:00Z" },
      now: new Date("2026-08-10T01:00:00Z"),
    });

    // Then the one condition is critical
    expect(testee).toHaveLength(1);
    expect(testee[0].severity).toBe("critical");
    expect(testee[0].what).toMatch(/last reported/);
  });

  // The gap that survived the first pass at this: both pages said "critical"
  // but disagreed about WHEN, this one at five minutes and hostStatus() at
  // three. A host four minutes silent had its own header call it offline and
  // this panel call it fine. Judged against the shared constant, so a change
  // to the alerting rule cannot move one page without the other.
  it("goes stale on the same threshold the header and the fleet use", () => {
    const lastSeen = new Date("2026-08-10T01:00:00Z");
    const justBefore = new Date(lastSeen.getTime() + STALE_THRESHOLD_MS);
    const justAfter = new Date(lastSeen.getTime() + STALE_THRESHOLD_MS + 1000);
    const at = (now: Date) =>
      needsAttention({
        ...quiet,
        host: { ...host, last_seen: lastSeen.toISOString() },
        now,
      });

    // Given a host exactly at the threshold, nothing is wrong yet
    expect(at(justBefore)).toEqual([]);
    // and one second past it, the panel agrees with the header
    expect(at(justAfter)).toHaveLength(1);
    expect(at(justAfter)[0].severity).toBe("critical");
  });

  it("warns and criticals on the same disk thresholds the fleet uses", () => {
    // Given four filesystems straddling both shared thresholds. used/free are
    // the only inputs -- Use% is used/(used+free), never total.
    const fs = (label: string, pct: number) => ({
      label,
      total: 100,
      used: pct,
      free: 100 - pct,
    });
    const testee = needsAttention({
      ...quiet,
      host,
      now: new Date(host.last_seen as string),
      filesystems: [
        fs("just-under", DISK_WARN_PCT - 1),
        fs("at-warn", DISK_WARN_PCT),
        fs("under-crit", DISK_CRIT_PCT - 1),
        fs("at-crit", DISK_CRIT_PCT),
      ],
    });

    // Then the boundaries fall exactly where fleet/conditions.ts puts them:
    // below warn is silent, at warn is a warning, at crit is critical.
    expect(testee.map((a) => a.severity)).toEqual([
      "warning",
      "warning",
      "critical",
    ]);
  });
});
