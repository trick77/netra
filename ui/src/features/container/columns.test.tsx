import { describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { ABSENT } from "../../lib/format";
import { Table } from "../../ui/Table";
import { userEvent } from "@testing-library/user-event";
import {
  composeIdentity,
  containerColumns,
  containerGroupTotals,
  ContainerGroupTotals,
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

  // The whole point of the column: the words are deriveState's, so a
  // container reads the same here as on the page this row links to.
  describe("the Status column", () => {
    const NOW = new Date("2026-08-10T14:00:00Z");

    it("says Reporting for a container whose samples are current", () => {
      renderRows([makeRow({ host_last_seen: "2026-08-10T14:00:00Z" })], {
        now: NOW,
      });
      expect(screen.getByText("Reporting")).toBeInTheDocument();
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
      expect(screen.getByText("Silent")).toBeInTheDocument();
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
      expect(screen.getByText("Gone")).toBeInTheDocument();
      expect(screen.queryByText("Silent")).toBeNull();
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
      expect(screen.getByText("Host offline")).toBeInTheDocument();
      expect(screen.queryByText("Silent")).toBeNull();
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
      expect(screen.queryByText("Silent")).toBeNull();
      expect(screen.getByText("No samples")).toBeInTheDocument();
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
      expect(screen.queryByText("Series gap")).toBeNull();
      expect(screen.getByText("Reporting")).toBeInTheDocument();
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
      expect(screen.getByText("Series gap")).toBeInTheDocument();
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
      expect(screen.getByText("Near mem_limit")).toBeInTheDocument();
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
      expect(screen.getByText("Near mem_limit")).toBeInTheDocument();
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
  it("meters memory against the container's own limit", () => {
    const { container } = renderRows(
      [makeRow({ mem: [900], mem_limit_bytes: 1000, cpu: [1] })],
      { cpuMax: 1, memMax: 1000 },
    );
    expect(container.querySelector(".meter")).not.toBeNull();
    expect(screen.getByText("90%")).toBeInTheDocument();
  });

  // A container running unlimited has nothing to be a percentage of, and a
  // bar against an invented denominator would be a number netra made up.
  it("draws no meter for a container with no mem_limit", () => {
    const { container } = renderRows(
      [makeRow({ mem: [900], mem_limit_bytes: null, cpu: [1] })],
      { cpuMax: 1, memMax: 1000 },
    );
    expect(container.querySelector(".meter")).toBeNull();
  });

  // Without it CPU was the one column in the set that could not answer its
  // own question, while Container, Image and Memory all sorted.
  it("sorts CPU on the latest reported percentage", () => {
    const cpu = containerColumns({ cpuMax: 1, memMax: 1000 }).find(
      (c) => c.key === "cpu",
    )!;
    expect(cpu.sortValue!(makeRow({ cpu: [4, 61, null] }))).toBe(61);
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
// host page's, grouped by compose project, and the fleet's, grouped by host --
// for the same reason the column set is one definition.
describe("containerGroupTotals", () => {
  it("sums the latest reported reading, not the latest bucket", () => {
    const totals = containerGroupTotals([
      // The newest bucket has not materialised for either; a container does
      // not stop using memory because the grid ticked over.
      makeRow({ cpu: [10, 20, null], mem: [100, 200, null] }),
      makeRow({ id: 2, cpu: [1, 2, null], mem: [10, 20, null] }),
    ]);

    expect(totals.cpu).toBe(22);
    expect(totals.mem).toBe(220);
  });

  // Absent is not zero. A group nobody fetched metrics for has not reported
  // 0% CPU.
  it("stays absent when nothing in the group has reported", () => {
    expect(containerGroupTotals([makeRow(), makeRow({ id: 2 })])).toEqual({
      cpu: null,
      mem: null,
      limit: null,
    });
  });

  // A group of two where one is capped has no ceiling to be a percentage of,
  // and summing only the capped one would put the numerator above a
  // denominator it can legitimately exceed.
  it("has no limit unless every container in the group has one", () => {
    expect(
      containerGroupTotals([
        makeRow({ cpu: [1], mem: [10], mem_limit_bytes: 100 }),
        makeRow({ id: 2, cpu: [1], mem: [10], mem_limit_bytes: null }),
      ]).limit,
    ).toBeNull();

    expect(
      containerGroupTotals([
        makeRow({ cpu: [1], mem: [10], mem_limit_bytes: 100 }),
        makeRow({ id: 2, cpu: [1], mem: [10], mem_limit_bytes: 400 }),
      ]).limit,
    ).toBe(500);
  });
});

describe("ContainerGroupTotals", () => {
  it("prints CPU as a percentage and memory against the group's ceiling", () => {
    render(
      <ContainerGroupTotals
        rows={[
          makeRow({ cpu: [40], mem: [1024], mem_limit_bytes: 4096 }),
          makeRow({ id: 2, cpu: [20], mem: [1024], mem_limit_bytes: 4096 }),
        ]}
      />,
    );

    expect(screen.getByText("60%")).toBeInTheDocument();
    expect(screen.getByText("2 kB")).toBeInTheDocument();
    expect(screen.getByText("/ 8.2 kB")).toBeInTheDocument();
  });

  // Meter's own rule, applied to a group: no bar against a denominator nobody
  // set.
  it("draws no bar for a group with an uncapped container in it", () => {
    const { container } = render(
      <ContainerGroupTotals
        rows={[makeRow({ cpu: [40], mem: [1024], mem_limit_bytes: null })]}
      />,
    );

    expect(container.querySelector(".meter")).toBeNull();
    expect(screen.getByText("1 kB")).toBeInTheDocument();
  });

  it("renders the absent marker for a group that has reported nothing", () => {
    render(<ContainerGroupTotals rows={[makeRow()]} />);

    expect(screen.getAllByText(ABSENT)).toHaveLength(2);
  });

  // A capped group that has not reported: the ceiling is known, the reading
  // is not. One absent marker for the reading, and no Meter behind it -- with
  // no value Meter prints an absent marker of its own, so the header would
  // otherwise say "Mem — / 4.1 kB —".
  it("draws no bar for a capped group that has reported no memory", () => {
    const { container } = render(
      <ContainerGroupTotals rows={[makeRow({ mem_limit_bytes: 4096 })]} />,
    );

    expect(container.querySelector(".meter")).toBeNull();
    expect(screen.getByText("/ 4.1 kB")).toBeInTheDocument();
    expect(screen.getAllByText(ABSENT)).toHaveLength(2);
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

  // The Status column says it, and it is the only thing that does. The
  // lowercase pill beside the name is gone with the contradiction it caused:
  // it stood next to a Status column reading "Silent" about the same
  // container in the same instant.
  it("states a gone row in the Status column and leaves a reporting one alone", () => {
    renderRows([goneRow()]);
    expect(screen.getByText("Gone")).toBeInTheDocument();
    expect(screen.queryByText("gone")).toBeNull();
    expect(screen.queryByText("Silent")).toBeNull();

    cleanup();
    renderRows([makeRow({ host_last_seen: HOST_SEEN })]);
    expect(screen.queryByText("Gone")).toBeNull();
  });

  // A state, so it carries the dot every state in that column carries -- and
  // the severity the row had when it read Silent, since nothing about the
  // container improved when the word changed.
  it("draws Gone as a serious state with a dot", () => {
    renderRows([goneRow()]);
    const badge = screen.getByText("Gone").closest(".badge")!;
    expect(badge.querySelector(".dot")).not.toBeNull();
    expect(badge.classList.contains("st-serious")).toBe(true);
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
  it("names the limit its memory meter is measured against", () => {
    const memory = containerColumns({ memMax: 1e9 }).find(
      (c) => c.header === "Memory",
    )!;
    const { container } = render(
      <>
        {memory.cell(makeRow({ mem: [5e8], mem_limit_bytes: 2_000_000_000 }))}
      </>,
    );

    expect(container.querySelector(".climit")?.textContent).toBe("of 2 GB");
  });
});
