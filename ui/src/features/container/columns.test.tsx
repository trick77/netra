import { describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { ABSENT } from "../../lib/format";
import { Table } from "../../ui/Table";
import { userEvent } from "@testing-library/user-event";
import {
  composeIdentity,
  containerColumns,
  containerGroupCells,
  containerGroupReading,
  containerGroupWorst,
  containerIsGone,
  GONE_AFTER_S,
  lastReported,
  trendScales,
  type ContainerRow,
} from "./columns";

function makeRow(overrides: Partial<ContainerRow> = {}): ContainerRow {
  return {
    id: 1,
    container_key: "shop/web",
    name: "shop-web-1",
    image: "nginx:1.27",
    is_agent: false,
    docker_state: null,
    health: null,
    state_since: null,
    started_at: null,
    restarts_window: 0,
    recreates_window: 0,
    last_restart: null,
    restarts_window_seconds: 86400,
    restart_count: null,
    labels: null,
    last_seen: "2026-08-10T14:00:00Z",
    host_id: 7,
    hostname: "web-01",
    ...overrides,
  };
}

function renderRows(
  rows: ContainerRow[],
  options: Parameters<typeof containerColumns>[0] = {},
) {
  return render(
    <Table
      columns={containerColumns(options)}
      rows={rows}
      rowKey={(row) => `${row.host_id}:${row.container_key}`}
    />,
  );
}

describe("composeIdentity", () => {
  it("splits a compose key into project and service", () => {
    expect(composeIdentity("shop/web")).toEqual({
      project: "shop",
      service: "web",
    });
  });

  // A key with no slash is a container the agent could not read compose
  // labels for -- it has a service and no project, not a project called "".
  it("gives a bare key no project rather than an empty one", () => {
    expect(composeIdentity("a1b2c3d4e5f6")).toEqual({
      project: ABSENT,
      service: "a1b2c3d4e5f6",
    });
  });
});

describe("lastReported", () => {
  it("skips a trailing null rather than reading it as the value", () => {
    expect(lastReported([1, 2, null])).toBe(2);
  });

  it("is null for a series that never reported, which is not zero", () => {
    expect(lastReported([null, null])).toBeNull();
    expect(lastReported(undefined)).toBeNull();
  });
});

describe("trendScales", () => {
  // Per-row auto-scaling draws an idle container and a saturated one with
  // the identical silhouette, which is the opposite of what a column is for.
  it("shares one ceiling across the whole list", () => {
    expect(
      trendScales([
        makeRow({ cpu: [10, 20], mem: [100, 200] }),
        makeRow({ container_key: "shop/db", cpu: [5, 90], mem: [50, 400] }),
      ]),
    ).toEqual({ cpuMax: 90, memMax: 400 });
  });

  it("never returns a zero ceiling, which would divide by zero", () => {
    expect(trendScales([makeRow({ cpu: [], mem: [] })])).toEqual({
      cpuMax: 1,
      memMax: 1,
    });
  });
});

describe("containerColumns", () => {
  it("shows the compose identity under the linked name", () => {
    renderRows([makeRow()]);
    expect(screen.getByRole("link", { name: "shop-web-1" })).toHaveAttribute(
      "href",
      "/containers/7/shop%2Fweb",
    );
    // The two halves the host tab used to spend two whole columns on.
    expect(screen.getByText("shop / web")).toBeInTheDocument();
  });

  it("shows a bare key as itself, with no invented project", () => {
    renderRows([makeRow({ container_key: "a1b2c3d4e5f6" })]);
    expect(screen.getByText("a1b2c3d4e5f6")).toBeInTheDocument();
    expect(screen.queryByText(`${ABSENT} / a1b2c3d4e5f6`)).toBeNull();
  });

  // "agent" is an identity, not a health state. A green badge would assert a
  // state netra does not collect -- the host tab used to.
  it("marks netra's own agent neutrally", () => {
    renderRows([makeRow({ is_agent: true })]);
    const badge = screen.getByText("agent").closest(".badge")!;
    expect(badge.className).not.toContain("st-ok");
  });

  // A mark, never a column: it is drawn on the few rows where "this came up
  // just now" is worth seeing and on no others, which is the same shape the
  // restart mark has.
  describe("the uptime mark", () => {
    const NOW = new Date("2026-08-10T14:00:00Z");

    it("says how long ago a container just came up", () => {
      renderRows([makeRow({ started_at: "2026-08-10T13:56:00Z" })], {
        now: NOW,
      });
      expect(screen.getByText(/up 4 m/)).toBeInTheDocument();
    });

    // One unit. `duration` would say "4 m 12 s", which is precision nobody
    // reads beside a name.
    it("prints one unit, not two", () => {
      renderRows([makeRow({ started_at: "2026-08-10T13:55:48Z" })], {
        now: NOW,
      });
      expect(screen.getByText(/up 4 m/)).toBeInTheDocument();
      expect(screen.queryByText(/12 s/)).toBeNull();
    });

    // Under STARTING_STUCK_S the Status column beside it may still change
    // its mind about this container.
    it("takes the warning colour while the container is very young", () => {
      const { container } = renderRows(
        [makeRow({ started_at: "2026-08-10T13:59:10Z" })],
        { now: NOW },
      );
      expect(container.querySelector(".upmark.fresh")).not.toBeNull();
    });

    it("is a plain annotation once past the starting window", () => {
      const { container } = renderRows(
        [makeRow({ started_at: "2026-08-10T13:30:00Z" })],
        { now: NOW },
      );
      expect(container.querySelector(".upmark")).not.toBeNull();
      expect(container.querySelector(".upmark.fresh")).toBeNull();
    });

    // Above UPTIME_MARK_S there is nothing to say, and it says nothing --
    // no dash, no "up 41 d".
    it("draws nothing at all once the container is no longer new", () => {
      const { container } = renderRows(
        [makeRow({ started_at: "2026-08-10T02:00:00Z" })],
        { now: NOW },
      );
      expect(container.querySelector(".upmark")).toBeNull();
    });

    // Every host whose socket refuses inspect reports no start time. That is
    // the case a column would have turned into four hundred dashes.
    it("draws nothing when the agent could not report a start time", () => {
      const { container } = renderRows([makeRow({ started_at: null })], {
        now: NOW,
      });
      expect(container.querySelector(".upmark")).toBeNull();
    });

    // A host clock ahead of the hub's is skew, not a container that has been
    // up for negative time.
    it("clamps a start time in the future rather than printing it", () => {
      renderRows([makeRow({ started_at: "2026-08-10T14:05:00Z" })], {
        now: NOW,
      });
      expect(screen.getByText(/up 0 s/)).toBeInTheDocument();
    });
  });

  // The whole point of the column: the words are deriveState's, so a
  // container reads the same here as on the page this row links to.
  describe("the Status column", () => {
    const NOW = new Date("2026-08-10T14:00:00Z");

    it("says Reporting for a container whose samples are current", () => {
      renderRows([makeRow({ host_last_seen: "2026-08-10T14:00:00Z" })], {
        now: NOW,
      });
      expect(screen.getByText("reporting")).toBeInTheDocument();
    });

    // Five minutes of missed posts on a host that is still reporting: past
    // SILENT_AFTER_S and well inside GONE_AFTER_S, which is the window this
    // word now names on its own.
    it("says Silent once the samples stop", () => {
      renderRows(
        [
          makeRow({
            last_seen: "2026-08-10T13:55:00Z",
            host_last_seen: "2026-08-10T14:00:00Z",
          }),
        ],
        { now: NOW },
      );
      expect(screen.getByText("silent")).toBeInTheDocument();
    });

    // And past the gone window it is the stronger word, not both words.
    it("says Gone once its host has outlived it by the gone window", () => {
      renderRows(
        [
          makeRow({
            last_seen: "2026-08-10T12:00:00Z",
            host_last_seen: "2026-08-10T14:00:00Z",
          }),
        ],
        { now: NOW },
      );
      expect(screen.getByText("gone")).toBeInTheDocument();
      expect(screen.queryByText("silent")).toBeNull();
    });

    // The same rule the detail badge follows: a host that went quiet stopped
    // every container on it, and blaming the container for that is the
    // contradiction containerIsGone has always avoided.
    it("names the host when the host is the thing that stopped", () => {
      renderRows(
        [
          makeRow({
            last_seen: "2026-08-10T12:00:00Z",
            host_last_seen: "2026-08-10T12:00:00Z",
          }),
        ],
        { now: NOW },
      );
      expect(screen.getByText("host offline")).toBeInTheDocument();
      expect(screen.queryByText("silent")).toBeNull();
    });

    // The host keeps posting while no container sample can land, so last_seen
    // ages forever. containerIsGone already returns false on this host; a
    // Silent badge would blame the container for the agent's blind spot.
    it("does not call a container silent when its host cannot sample any", () => {
      renderRows(
        [
          makeRow({
            last_seen: "2026-08-10T09:00:00Z",
            host_last_seen: "2026-08-10T14:00:00Z",
            host_containers_capability: "no-cgroup-scopes",
          }),
        ],
        { now: NOW },
      );
      expect(screen.queryByText("silent")).toBeNull();
      expect(screen.getByText("no samples")).toBeInTheDocument();
    });

    // The same blind spot with a different cause -- see containerSamplesBlocked.
    it("does not call a container silent when the socket has gone quiet", () => {
      renderRows(
        [
          makeRow({
            last_seen: "2026-08-10T09:00:00Z",
            host_last_seen: "2026-08-10T14:00:00Z",
            host_containers_capability: "docker-socket-silent",
          }),
        ],
        { now: NOW },
      );
      expect(screen.queryByText("silent")).toBeNull();
      expect(screen.getByText("no samples")).toBeInTheDocument();
    });

    // The fleet grid spans 24h, so a container created an hour ago is mostly
    // leading nulls. Warning on that would light up a healthy fleet.
    it("does not call a young container's leading nulls a gap", () => {
      renderRows(
        [
          makeRow({
            host_last_seen: "2026-08-10T14:00:00Z",
            cpu: [null, null, 1, 2],
            mem: [null, null, 1e8, 1e8],
            mem_limit_bytes: 1e9,
          }),
        ],
        { now: NOW },
      );
      expect(screen.queryByText("series gap")).toBeNull();
      expect(screen.getByText("reporting")).toBeInTheDocument();
    });

    it("still calls a hole between readings a gap", () => {
      renderRows(
        [
          makeRow({
            host_last_seen: "2026-08-10T14:00:00Z",
            cpu: [1, null, 2],
            mem: [1e8, null, 1e8],
            mem_limit_bytes: 1e9,
          }),
        ],
        { now: NOW },
      );
      expect(screen.getByText("series gap")).toBeInTheDocument();
    });

    // The detail page reads the last READING; off the latest bucket instead,
    // one empty trailing bucket hid pressure here that the page still showed.
    it("reads memory past an empty trailing bucket", () => {
      renderRows(
        [
          makeRow({
            host_last_seen: "2026-08-10T14:00:00Z",
            cpu: [1, 2, 3],
            mem: [9.6e8, 9.7e8, null],
            mem_limit_bytes: 1e9,
          }),
        ],
        { now: NOW },
      );
      expect(screen.getByText("near mem_limit")).toBeInTheDocument();
    });

    // Warnings that need a series are simply out of reach on a listing that
    // fetched none -- the row says what it knows rather than nothing.
    it("warns on memory near the limit once trends are fetched", () => {
      renderRows(
        [
          makeRow({
            host_last_seen: "2026-08-10T14:00:00Z",
            cpu: [1, 2],
            mem: [9.6e8, 9.7e8],
            mem_limit_bytes: 1e9,
          }),
        ],
        { now: NOW },
      );
      expect(screen.getByText("near mem_limit")).toBeInTheDocument();
    });
  });

  // Last seen and Status are NOT trend columns: both come off the listing
  // itself, so they are there whether or not anyone asked for metrics. Status
  // simply cannot reach the two states that need a series -- it still says
  // Reporting, Silent or Host offline, which is what the listing knows.
  it("has no trend columns when nobody fetched metrics", () => {
    renderRows([makeRow()]);
    expect(
      screen.getAllByRole("columnheader").map((h) => h.textContent),
    ).toEqual(["Container", "Status", "Image", "Last seen"]);
  });

  // The one filled colour a container row can honestly carry, and the one
  // question a memory sparkline cannot answer: how close to being OOM-killed.
  it("bars memory against the container's own limit", () => {
    const { container } = renderRows(
      [makeRow({ mem: [900], mem_limit_bytes: 1000, cpu: [1] })],
      { cpuMax: 1, memMax: 1000 },
    );
    // The fleet row's segmented bar, not this list's old continuous Meter.
    const bar = container.querySelector(".segbar");
    expect(bar).not.toBeNull();
    expect(bar!.getAttribute("aria-valuenow")).toBe("90");
    expect(screen.getByText("of 1 kB")).toBeInTheDocument();
  });

  // The gap this rework closes. A container with no mem_limit -- which on a
  // real fleet is nearly all of them -- used to draw a silhouette with no bar
  // and no figure at all. Measured against the host it is holding 20 % of the
  // machine, and the caption says which denominator that is.
  it("bars an unlimited container against the host's memory", () => {
    const { container } = renderRows(
      [
        makeRow({
          mem: [2_000_000_000],
          mem_limit_bytes: null,
          host_mem_total: 10_000_000_000,
          cpu: [1],
        }),
      ],
      { cpuMax: 1, memMax: 1e10 },
    );
    const bar = container.querySelector(".segbar");
    expect(bar).not.toBeNull();
    expect(bar!.getAttribute("aria-valuenow")).toBe("20");
    expect(screen.getByText(/host$/)).toBeInTheDocument();
  });

  // With NEITHER denominator there is still nothing to be a percentage of, and
  // a bar against an invented one would be a number netra made up.
  it("draws no memory bar without a limit or a host total", () => {
    const { container } = renderRows(
      [makeRow({ mem: [900], mem_limit_bytes: null, cpu: [1] })],
      { cpuMax: 1, memMax: 1000 },
    );
    expect(container.querySelector(".segbar")).toBeNull();
  });

  // cpu_pct is percent of ONE core, so ordering on it ranked a container using
  // 90 % of one core above one using 600 % of a 32-thread box. The column now
  // sorts the way its bars read: by share of the host.
  it("sorts CPU on the share of the host, not the raw percentage", () => {
    const cpu = containerColumns({ cpuMax: 1, memMax: 1000 }).find(
      (c) => c.key === "cpu",
    )!;
    expect(
      cpu.sortValue!(makeRow({ cpu: [4, 400, null], host_threads: 8 })),
    ).toBe(50);
    // No denominator, so it cannot be compared with the rows that have one.
    expect(cpu.sortValue!(makeRow({ cpu: [4, 61, null] }))).toBeNull();
  });

  // Sorting on percent-of-limit would drop every unlimited container into
  // the unknown group, which on most fleets is nearly all of them.
  it("sorts memory on bytes, so an unlimited container still has a place", () => {
    const memory = containerColumns({ cpuMax: 1, memMax: 1000 }).find(
      (c) => c.key === "memory",
    )!;
    expect(
      memory.sortValue!(makeRow({ mem: [10, 900], mem_limit_bytes: null })),
    ).toBe(900);
  });
});
// What a collapsed group header prints. One definition for both lists -- the
// host page's, grouped by compose project, and the fleet's, grouped by stack --
// for the same reason the column set is one definition.
describe("containerGroupReading", () => {
  it("sums the latest reported reading, not the latest bucket", () => {
    const got = containerGroupReading([
      // The newest bucket has not materialised for either; a container does
      // not stop using memory because the grid ticked over.
      makeRow({
        cpu: [10, 20, null],
        mem: [100, 200, null],
        host_threads: 4,
        host_mem_total: 1000,
      }),
      makeRow({
        id: 2,
        cpu: [1, 2, null],
        mem: [10, 20, null],
        host_threads: 4,
        host_mem_total: 1000,
      }),
    ]);

    // 22 % of one core over four of them.
    expect(got.cpuPct).toBeCloseTo(5.5);
    expect(got.memBytes).toBe(220);
    expect(got.memPct).toBeCloseTo(22);
  });

  // The denominator is the HOST's, never a sum of the containers' own limits:
  // a group is a share of one machine, and summing limits could not be done
  // honestly for a group where only some are capped.
  it("measures memory against the host, not against the group's limits", () => {
    const got = containerGroupReading([
      makeRow({ mem: [500], mem_limit_bytes: 1000, host_mem_total: 10_000 }),
      makeRow({
        id: 2,
        mem: [500],
        mem_limit_bytes: null,
        host_mem_total: 10_000,
      }),
    ]);
    // 1000 of the machine's 10 000, not of the 1000 one of them declared.
    expect(got.memPct).toBeCloseTo(10);
  });

  // Absent is not zero. A group nobody fetched metrics for has not reported
  // 0 % CPU, and one on a host that never said how many cores it has cannot be
  // a percentage of anything.
  it("stays absent when there is nothing to read or nothing to read against", () => {
    expect(
      containerGroupReading([makeRow(), makeRow({ id: 2 })]),
    ).toMatchObject({
      cpuPct: null,
      memPct: null,
      memBytes: null,
    });
    expect(
      containerGroupReading([makeRow({ cpu: [10], mem: [100] })]),
    ).toMatchObject({ cpuPct: null, memPct: null, memBytes: 100 });
  });
});

describe("containerGroupCells", () => {
  it("puts the group's bars in the CPU and Memory columns", () => {
    const cells = containerGroupCells([
      makeRow({
        cpu: [40],
        mem: [1024],
        host_threads: 2,
        host_mem_total: 4096,
        last_seen: new Date().toISOString(),
      }),
    ]);
    const { container } = render(
      <>
        {cells.cpu}
        {cells.memory}
      </>,
    );

    const bars = container.querySelectorAll(".segbar");
    expect(bars).toHaveLength(2);
    // 40 % of one core over two of them, and 1024 of 4096.
    expect(bars[0]!.getAttribute("aria-valuenow")).toBe("20");
    expect(bars[1]!.getAttribute("aria-valuenow")).toBe("25");
    // The bare denominator: a heading is always a share of one machine, so
    // there is nothing to tell it apart from.
    expect(screen.getByText("of 4 KiB")).toBeInTheDocument();
  });

  it("draws nothing for a group with no denominator", () => {
    const cells = containerGroupCells([makeRow({ cpu: [40], mem: [1024] })]);
    expect(cells.cpu).toBeNull();
    expect(cells.memory).toBeNull();
  });
});

// What lets a folded group be honest: a heading that says "nothing here needs
// you" has to be able to say the opposite.
describe("containerGroupWorst", () => {
  const NOW = new Date("2026-08-10T14:00:00Z");
  const healthy = {
    last_seen: "2026-08-10T14:00:00Z",
    host_last_seen: "2026-08-10T14:00:00Z",
  };

  it("is null for a group where everything is reporting", () => {
    expect(
      containerGroupWorst(
        [makeRow(healthy), makeRow({ id: 2, ...healthy })],
        NOW,
      ),
    ).toBeNull();
  });

  it("names the worst kind and how many rows carry it", () => {
    const got = containerGroupWorst(
      [
        makeRow(healthy),
        makeRow({ id: 2, ...healthy, docker_state: "restarting" }),
        makeRow({ id: 3, ...healthy, health: "unhealthy" }),
      ],
      NOW,
    );
    // Unhealthy outranks restarting -- see KIND_RANK.
    expect(got?.state.kind).toBe("unhealthy");
    expect(got?.count).toBe(1);
  });
});

// The rule that decides both the pill and whether a purge is offered.
describe("containerIsGone", () => {
  const HOST_SEEN = "2026-08-10T14:00:00Z";
  const hostMs = Date.parse(HOST_SEEN);
  const at = (offsetS: number) =>
    new Date(hostMs - offsetS * 1000).toISOString();

  it("is gone once its host kept reporting well past its own last sample", () => {
    const row = makeRow({
      host_last_seen: HOST_SEEN,
      last_seen: at(GONE_AFTER_S + 60),
    });
    expect(containerIsGone(row)).toBe(true);
  });

  it("is not gone inside the window", () => {
    const row = makeRow({
      host_last_seen: HOST_SEEN,
      last_seen: at(GONE_AFTER_S - 60),
    });
    expect(containerIsGone(row)).toBe(false);
  });

  // The whole reason the rule is not `now() - last_seen`. A host that has
  // been offline for a week drags every container on it into the past
  // together, and marking all of them gone would offer to delete the history
  // of a machine that is merely unreachable.
  it("marks nothing gone on a host that went quiet with it", () => {
    const row = makeRow({
      host_last_seen: at(0),
      last_seen: at(30),
    });
    expect(containerIsGone(row)).toBe(false);
  });

  // Nothing to measure against, and the wrong direction to fail in is the
  // one that offers to delete something.
  it("is not gone when the host has never reported", () => {
    expect(containerIsGone(makeRow({ host_last_seen: null }))).toBe(false);
    expect(containerIsGone(makeRow({ host_last_seen: undefined }))).toBe(false);
  });

  it("is not gone when a timestamp does not parse", () => {
    const row = makeRow({ host_last_seen: HOST_SEEN, last_seen: "not a date" });
    expect(containerIsGone(row)).toBe(false);
  });
});

describe("the gone state and the purge action", () => {
  const HOST_SEEN = "2026-08-10T14:00:00Z";
  const goneRow = (overrides: Partial<ContainerRow> = {}) =>
    makeRow({
      host_last_seen: HOST_SEEN,
      last_seen: new Date(
        Date.parse(HOST_SEEN) - (GONE_AFTER_S + 3600) * 1000,
      ).toISOString(),
      ...overrides,
    });

  // The Status column says it, and it is the ONLY thing that does. The pill
  // that used to sit beside the name is gone with the contradiction it
  // caused: it stood next to a Status column reading "silent" about the same
  // container in the same instant.
  //
  // Counted rather than case-checked. This used to tell the two apart by
  // their capitals -- "Gone" was the column, "gone" was the pill -- and every
  // status word in the app is lowercase now, so that distinction no longer
  // exists to assert. What the test is actually about survives it: exactly
  // one thing in the row says the word.
  it("states a gone row in the Status column and leaves a reporting one alone", () => {
    renderRows([goneRow()]);
    expect(screen.getAllByText("gone")).toHaveLength(1);
    expect(screen.queryByText("silent")).toBeNull();

    cleanup();
    renderRows([makeRow({ host_last_seen: HOST_SEEN })]);
    expect(screen.queryByText("gone")).toBeNull();
  });

  // A state, so it carries the dot every state in that column carries -- and
  // the severity the row had when it read Silent, since nothing about the
  // container improved when the word changed.
  it("draws Gone as a warning state with a dot", () => {
    renderRows([goneRow()]);
    const badge = screen.getByText("gone").closest(".badge")!;
    expect(badge.querySelector(".dot")).not.toBeNull();
    expect(badge.classList.contains("st-warn")).toBe(true);
  });

  // The fleet list passes no onPurge, and this is what that buys: no column,
  // no button, nothing to mis-click several hundred rows from the host.
  it("offers no purge at all when the caller passed no handler", () => {
    renderRows([goneRow()]);
    expect(screen.queryByRole("button", { name: /purge/i })).toBeNull();
  });

  it("offers purge on a gone row only", () => {
    renderRows([goneRow(), makeRow({ id: 2, host_last_seen: HOST_SEEN })], {
      onPurge: () => {},
    });
    expect(screen.getAllByRole("button", { name: "Purge" })).toHaveLength(1);
  });

  it("asks for a confirm before it calls the handler", async () => {
    const user = userEvent.setup();
    const onPurge = vi.fn();
    renderRows([goneRow()], { onPurge, purgeConfirming: null });
    await user.click(screen.getByRole("button", { name: "Purge" }));
    expect(onPurge).toHaveBeenCalledTimes(1);

    // The caller owns the two-step state, so the second render is what a
    // confirming row looks like.
    screen.getByRole("button", { name: "Purge" }).remove();
    renderRows([goneRow()], { onPurge, purgeConfirming: 1 });
    expect(
      screen.getByRole("button", { name: "Confirm purge" }),
    ).toBeInTheDocument();
  });
});

// A host whose cgroup hierarchy is not mounted reports host samples and no
// container samples at all (lib/containers.ts: no-cgroup-scopes means NOTHING
// is collected), so every container on it ages past the window together.
// Marking them gone would offer to delete a running container's history.
describe("a host that cannot collect containers at all", () => {
  const HOST_SEEN = "2026-08-10T14:00:00Z";
  const stale = new Date(
    Date.parse(HOST_SEEN) - (GONE_AFTER_S + 3600) * 1000,
  ).toISOString();

  it("marks nothing gone when the cgroup scopes are missing", () => {
    const row = makeRow({
      host_last_seen: HOST_SEEN,
      last_seen: stale,
      host_containers_capability: "no-cgroup-scopes",
    });
    expect(containerIsGone(row)).toBe(false);
  });

  // Same argument, different cause: a MOUNTED socket that stops naming
  // containers means the agent reports none of the scopes it measured, so
  // every row on that host ages past the window at once. Badging them Gone
  // would put a Purge button on containers that are still running -- and this
  // module used to answer the question from a set of its own, which is exactly
  // how it missed this value while the panels above the list had it.
  it("marks nothing gone when the Docker socket has gone silent", () => {
    const row = makeRow({
      host_last_seen: HOST_SEEN,
      last_seen: stale,
      host_containers_capability: "docker-socket-silent",
    });
    expect(containerIsGone(row)).toBe(false);
  });

  // The milder one: cgroup v2 still yields CPU, memory and I/O, so samples
  // keep landing and last_seen keeps advancing -- only the names are missing.
  // A container that stopped being sampled there really has stopped.
  it("still marks gone when only the Docker socket is unreadable", () => {
    const row = makeRow({
      host_last_seen: HOST_SEEN,
      last_seen: stale,
      host_containers_capability: "no-docker-socket",
    });
    expect(containerIsGone(row)).toBe(true);
  });

  // One shape for "a chart and the figure it ends on", across both lists: the
  // container CPU cell used to draw its own muted span in a 44px column while
  // the fleet drew a block with a unit line. A reader switching lists should
  // not have to re-learn what a reading is.
  it("draws its CPU reading with the same block the fleet uses", () => {
    const cpu = containerColumns({ cpuMax: 100 }).find(
      (c) => c.header === "CPU",
    )!;
    const { container } = render(<>{cpu.cell(makeRow({ cpu: [1, 2, 34] }))}</>);

    expect(container.querySelector(".metric-read .v")?.textContent).toBe("34%");
  });

  // The meter says how close to the limit; it never said what the limit IS,
  // so two containers with the same bar and a tenfold difference in headroom
  // read identically.
  it("names the limit its memory bar is measured against", () => {
    const memory = containerColumns({ memMax: 1e9 }).find(
      (c) => c.header === "Memory",
    )!;
    const { container } = render(
      <>
        {memory.cell(makeRow({ mem: [5e8], mem_limit_bytes: 2_000_000_000 }))}
      </>,
    );

    // A bar that says how close to the limit without saying what the limit IS
    // reads identically for two containers with a tenfold difference in
    // headroom.
    expect(container.querySelector(".metric-now-wrap .u")?.textContent).toBe(
      "of 2 GB",
    );
  });
});
