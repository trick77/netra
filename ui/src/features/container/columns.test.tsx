import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { ABSENT } from "../../lib/format";
import { Table } from "../../ui/Table";
import {
  composeIdentity,
  containerColumns,
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

  it("has no trend columns when nobody fetched metrics", () => {
    renderRows([makeRow()]);
    expect(
      screen.getAllByRole("columnheader").map((h) => h.textContent),
    ).toEqual(["Container", "Image"]);
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
