import { describe, expect, it } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type {
  Drive,
  HostDetail,
  MetricsResponse,
  Unit,
} from "../../../lib/api";
import { ABSENT } from "../../../lib/format";
import { Overview, needsAttention } from "./Overview";
import { filesystemRows } from "./overviewTiles";
import { DISK_WARN_PCT, DISK_CRIT_PCT } from "../../fleet/conditions";
import { STALE_THRESHOLD_MS } from "../../../lib/host";

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

/** The System block's one-line summary, which is what the card looks like
 * before anyone clicks it. Five of the eight facts are on it and the same
 * five are also in the strip underneath, so a bare getByText inside the
 * region now matches twice -- every assertion about the summary has to say
 * which of the two it means. */
function systemSummary() {
  return screen
    .getByRole("region", { name: "System" })
    .querySelector("summary")!;
}

/** The disclosed strip: the eight labelled facts. */
function systemStrip() {
  return screen.getByRole("region", { name: "System" }).querySelector("dl")!;
}

describe("Overview system facts", () => {
  // GOOS is a build constant, so an agent that could not read /etc/os-release
  // falls back to it and the page used to print the compiler's token.
  it("names the operating system rather than printing GOOS", () => {
    renderOverview({ host: { ...host, os_name: "linux" } });
    expect(within(systemSummary()).getByText("Linux")).toBeInTheDocument();
    expect(within(systemStrip()).getByText("Linux")).toBeInTheDocument();
  });

  it("does not title-case its way to Darwin and Freebsd", () => {
    renderOverview({ host: { ...host, os_name: "darwin" } });
    expect(within(systemSummary()).getByText("macOS")).toBeInTheDocument();
  });

  // A distro string is already the right answer and must pass through.
  it("leaves a distribution name alone", () => {
    renderOverview({ host: { ...host, os_name: "Debian GNU/Linux 13" } });
    expect(
      within(systemSummary()).getByText("Debian GNU/Linux 13"),
    ).toBeInTheDocument();
  });

  // ~32 GiB of MemTotal. Decimally this reads "33.3 GB", which is above the
  // capacity the machine actually has.
  it("states installed memory in the binary units it was sold in", () => {
    renderOverview({ host: { ...host, memory_total: 33_260_000_000 } });
    const system = screen.getByRole("region", { name: "System" });
    expect(within(systemSummary()).getByText("31 GiB")).toBeInTheDocument();
    expect(within(system).queryByText(/33.3 GB/)).toBeNull();
  });
});

// The card is shut when the page opens: the five facts an operator reads in
// passing are on one line, and the other three cost a click. That is the
// whole point of the shape -- a permanently open eight-fact table spent more
// of the page's best position on its own frame than on its facts.
describe("Overview System summary", () => {
  it("states OS, kernel, processor, memory and uptime without a click", () => {
    renderOverview();
    const summary = systemSummary();

    expect(summary.closest("details")!.hasAttribute("open")).toBe(false);
    for (const fact of [
      "Ubuntu 24.04",
      "6.8.0-31-generic",
      "EPYC 7003",
      "7.5 GiB",
    ]) {
      expect(within(summary).getByText(fact)).toBeInTheDocument();
    }
    expect(summary.textContent).toContain("up ");
  });

  // Architecture, cores and the agent build are the three facts behind the
  // click. They are looked up deliberately rather than read in passing --
  // amd64 is implied by the processor model, the core count is a number you
  // go and get when sizing something, and the commit is what you read when a
  // host reports something the code should not be able to report. If any of
  // them creeps onto the summary the disclosure stops paying for its click.
  it("keeps architecture, cores and the agent build behind the click", () => {
    renderOverview();
    const summary = systemSummary();
    const strip = systemStrip();

    for (const hidden of ["amd64", "4 cores · 8 threads", "0.4.1 · abc1234"]) {
      expect(within(summary).queryByText(hidden)).toBeNull();
      expect(within(strip).getByText(hidden)).toBeInTheDocument();
    }
  });

  // The processor model is the raw string, long enough on a real host to push
  // the line onto a second row on its own, so it is the one summary value
  // allowed to ellipsize -- which makes its title the only place the full
  // text survives.
  it("carries the processor model's full text on the summary too", () => {
    const cpu = "AMD EPYC 7402P 24-Core Processor";
    renderOverview({ host: { ...host, cpu_model: cpu } });
    const model = within(systemSummary()).getByText(cpu);

    expect(model.getAttribute("title")).toBe(cpu);
    expect(model.className).toContain("cpu");
  });

  // The worst case the store can actually produce: ingest.go NULLIFs every
  // one of these columns, and an agent in a container that cannot read the
  // host's /etc/os-release reports no os_name at all. Unguarded, the OS span
  // rendered osLabel(null) and the whole summary read "—" -- a line whose
  // only content is an em dash, which is the failure the omission rule exists
  // to prevent rather than a milder version of it.
  it("writes no facts at all rather than a line of dashes", () => {
    renderOverview({
      host: {
        ...host,
        os_name: null,
        kernel: null,
        cpu_model: null,
        memory_total: null,
        uptime_s: null,
      },
    });
    const summary = systemSummary();

    expect(summary.textContent).not.toContain(ABSENT);
    // No FACTS. What is left is the screen-reader label, which is the whole
    // point of it: a summary with no text at all is an unlabelled disclosure
    // to anyone listening. The chevron is still there too -- it is the
    // control, and a fold with nothing to click is not this state.
    expect(summary.textContent).toBe("System details");
    expect(summary.querySelector(".sr-only")).not.toBeNull();
    expect(summary.querySelector("svg.chev")).not.toBeNull();
    expect(summary.querySelector("svg.osicon")).toBeNull();
    // The labelled strip still answers for all eight, dashes included.
    expect(within(systemStrip()).getAllByText(ABSENT).length).toBeGreaterThan(
      3,
    );
  });

  // The summary line writes the OS name and nothing else in front of it. The
  // distribution mark was here until the line was found to be carrying two
  // marks: the chevron, which is the control, and a logo that only repeated
  // the name spelled out beside it. The fleet list keeps its own, where the
  // mark is the fastest way to tell forty rows apart.
  it("writes the OS name with no distribution mark", () => {
    renderOverview();
    const os = systemSummary().querySelector(".strong")!;

    expect(os.querySelector("svg.osicon")).toBeNull();
    expect(os.textContent).toBe("Ubuntu 24.04");
  });

  // An absent fact is not written at all, rather than written as an em dash.
  // The separators are drawn by CSS on `span + span`, so a placeholder span
  // would leave a dot with nothing on one side of it -- the same reason the
  // fleet list dropped the site line's dash and the Temperature card stopped
  // rendering itself empty. The labelled strip below still shows ABSENT,
  // because a labelled row is where a gap does need a mark.
  it("omits a fact the host never reported rather than writing a dash", () => {
    renderOverview({
      // uptime_s included deliberately. It is the one summary fact whose
      // formatter absorbs null itself -- duration(null) is ABSENT -- so an
      // unguarded span writes "up —" rather than nothing, and a fixture that
      // keeps an uptime passes this test while the page prints the dash.
      host: {
        ...host,
        kernel: null,
        cpu_model: null,
        memory_total: null,
        uptime_s: null,
      },
    });
    const summary = systemSummary();

    expect(summary.textContent).not.toContain(ABSENT);
    expect(summary.textContent).not.toContain("up ");
    expect(summary.querySelector(".cpu")).toBeNull();
    // Still eight labelled rows underneath, three of them absent.
    expect(
      within(systemStrip()).getAllByText(ABSENT).length,
    ).toBeGreaterThanOrEqual(3);
  });
});

describe("Overview", () => {
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
  // A card inside the mosaic is at most half the page wide. The band has to
  // be outside the grid altogether to span the page and be read first.
  it("puts the band above the mosaic, not inside it", () => {
    const { container } = renderOverview({ agentMetrics: agentMetrics(12) });
    const band = screen.getByRole("region", { name: /needs attention/i });
    const grid = container.querySelector(".mosaic");
    expect(grid).not.toBeNull();
    expect(grid?.contains(band)).toBe(false);
    expect(
      band.compareDocumentPosition(grid!) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
  });

  // The System card is lifted out for a different reason: it is a strip of
  // eight facts about the machine rather than a reading, and full width above
  // the columns is where it can be laid out four across.
  it("puts the System card above the mosaic, not inside it", () => {
    const { container } = renderOverview();
    const system = screen.getByRole("region", { name: "System" });
    const grid = container.querySelector(".mosaic");
    expect(grid).not.toBeNull();
    expect(grid?.contains(system)).toBe(false);
    expect(
      system.compareDocumentPosition(grid!) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
  });

  // The only link between the hoisted card and the CSS that lays it out four
  // across, label above value. Without it the card is a thin column of eight
  // one-line rows down the left of a full-width card -- which renders
  // perfectly, passes every other test here, and is exactly what the hoist
  // and then the strip were meant to fix. jsdom applies no stylesheet, so the
  // class name is the whole assertion available.
  it("lays the System card's facts out four across, label above value", () => {
    renderOverview();
    const system = screen.getByRole("region", { name: "System" });
    const dl = system.querySelector("dl")!;

    expect(dl.className).toContain("sysstrip");
    // Every pair is its own grid item -- a name-value group -- and all
    // eleven facts survive the shape change.
    expect(dl.querySelectorAll(":scope > .f").length).toBe(11);
    // Where the machine is, ahead of what it is: the reader asks where this
    // box sits and whose it is before they ask which processor.
    expect([...dl.querySelectorAll("dt")].map((dt) => dt.textContent)).toEqual([
      "Location",
      "Provider",
      "Facility",
      "OS",
      "Kernel",
      "Architecture",
      "Processor",
      "Cores",
      "Memory",
      "Uptime",
      "Agent",
    ]);
  });

  // A host whose agent reports none of the three is not missing three facts,
  // it was never given them, and three dashes would say otherwise. Report ANY
  // of them and the rule flips: a provider with no facility beside it IS a gap
  // somebody meant to fill, and a labelled strip is where a gap gets marked.
  it("writes no location facts at all for a host that reports none", () => {
    renderOverview({
      host: { ...host, location: null, provider: null, facility: null },
    });
    const dl = screen
      .getByRole("region", { name: "System" })
      .querySelector("dl")!;

    expect([...dl.querySelectorAll("dt")].map((dt) => dt.textContent)).toEqual([
      "OS",
      "Kernel",
      "Architecture",
      "Processor",
      "Cores",
      "Memory",
      "Uptime",
      "Agent",
    ]);
  });

  // The strip marks the gap where the fleet row would not: an agent that set
  // a provider and no facility has a field somebody meant to fill, and this
  // is the labelled place that can say so without spending a fleet row on it.
  it("dashes a fact the agent left unset once it reports any of them", () => {
    renderOverview({
      host: {
        ...host,
        location: "Roubaix, France",
        provider: "OVH",
        facility: null,
      },
    });
    const dl = screen
      .getByRole("region", { name: "System" })
      .querySelector("dl")!;
    const value = (label: string) =>
      [...dl.querySelectorAll("dt")].find((dt) => dt.textContent === label)!
        .nextElementSibling!.textContent;

    expect(value("Location")).toBe("Roubaix, France");
    expect(value("Provider")).toBe("OVH");
    expect(value("Facility")).toBe(ABSENT);
  });

  // The processor model is the one value long enough to be cut off by the
  // strip's ellipsis, so the full string has to stay reachable.
  it("carries the full text of a fact as the value's title", () => {
    renderOverview();
    const system = screen.getByRole("region", { name: "System" });
    const processor = [...system.querySelectorAll("dt")].find(
      (dt) => dt.textContent === "Processor",
    )!.nextElementSibling!;

    expect(processor.getAttribute("title")).toBe(processor.textContent);
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

// The Traffic card became two tiles and a spec panel. The rules these
// assertions carry did not change with the shape: the figures are
// host_current's gauges, blanked when the host stops reporting, and they are
// bytes per second, never bits.
describe("Overview network tiles", () => {
  it("enlarges the traffic chart into the dialog", async () => {
    renderOverview({ netMetrics });
    // The chart, not the tiles: the Traffic panel is the host-traffic spec,
    // drawn here exactly as the Network tab draws it.
    const traffic = screen.getByRole("region", { name: "Traffic chart" });

    await userEvent.click(
      within(traffic).getByRole("button", { name: /Enlarge/ }),
    );

    expect(screen.getByRole("dialog")).toBeInTheDocument();
  });

  // Each tile is a link to the panel that draws the same column in full,
  // through /hosts/{id}/chart/<slug> -- the URL the router already resolves
  // to whichever tab owns that panel.
  it("links a tile to the panel that draws its column", () => {
    renderOverview({ netMetrics });
    const network = screen.getByRole("region", { name: "Network" });

    expect(
      within(network).getByRole("link", { name: /Traffic in/ }),
    ).toHaveAttribute("href", "/hosts/7/chart/host-traffic");
  });

  it("shows traffic in bytes per second, not bits", () => {
    renderOverview({
      netMetrics,
      host: { ...host, net_rx_bytes: 2_000_000, net_tx_bytes: 500_000 },
    });
    const traffic = screen.getByRole("region", { name: "Network" });

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
    const traffic = screen.getByRole("region", { name: "Network" });

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
    const traffic = screen.getByRole("region", { name: "Network" });

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
    const traffic = screen.getByRole("region", { name: "Network" });

    expect(traffic.textContent).not.toMatch(/MB\/s/);
    expect(
      within(traffic).getAllByText(new RegExp(ABSENT)).length,
    ).toBeGreaterThan(0);
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
    drives: null,
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

  // The same four cases the fleet's conditions.test.ts pins, so the two pages
  // can be seen agreeing about bytes and not only about percentages.
  it("weighs the bytes left, not the percentage alone", () => {
    const GB = 1024 ** 3;
    const at = (used: number, free: number) =>
      needsAttention({
        ...quiet,
        host,
        now: new Date(host.last_seen as string),
        filesystems: [{ label: "/mnt/ark", total: used + free, used, free }],
      });

    // Given 6.7 TB at 90%, 674 GB is left and there is nothing to do
    expect(at(6100 * GB, 674 * GB)).toEqual([]);
    // and the same percentage on a small root, where 2 GB is left, warns
    expect(at(18 * GB, 2 * GB)[0]?.severity).toBe("warning");
    // 96% of a big array with 67 GB left is worth a word, not an emergency,
    // and with 500 GB left neither floor binds and there is nothing to say
    expect(at(1600 * GB, 67 * GB)[0]?.severity).toBe("warning");
    expect(at(12000 * GB, 500 * GB)).toEqual([]);
    // and 96% with under 20 GiB left is the real thing
    expect(at(19 * GB, 0.8 * GB)[0]?.severity).toBe("critical");
  });
});

// The panel this tab is judged on used to read clean on a host whose Drives
// table, one tab away, was showing a critical disk in red.
describe("needsAttention reads the host's drives", () => {
  const quietDrives = {
    agentMetrics: null,
    hostMetrics: null,
    filesystems: [],
    units: null,
  };
  const disk = (device: string, attrs: Record<number, number>): Drive => ({
    device,
    model: "ST16000NM000J",
    serial: "ZR5A1M0K",
    last_seen: "2026-08-10T13:00:00Z",
    attributes: Object.entries(attrs).map(([id, raw]) => ({
      id: Number(id),
      raw,
      normalized: null,
    })),
  });

  it("names each failing drive, one line per thing to replace", () => {
    // Given two disks in trouble
    const testee = needsAttention({
      ...quietDrives,
      host,
      now: new Date(host.last_seen as string),
      drives: [disk("sda", { 197: 3 }), disk("sdb", { 5: 12 })],
    });

    // Then both are on the list, worst first, each naming its own device --
    // this panel is what to DO, and two disks are two replacements
    expect(testee).toEqual([
      { severity: "critical", what: "sda — 3 pending sectors" },
      { severity: "serious", what: "sdb — 12 reallocated sectors" },
    ]);
  });

  it("stays silent when the drives could not be fetched", () => {
    // null is "netra did not look", which must not read as "the disks are
    // fine" -- the same line units: null draws
    expect(
      needsAttention({
        ...quietDrives,
        host,
        now: new Date(host.last_seen as string),
        drives: null,
      }),
    ).toEqual([]);
  });
});
