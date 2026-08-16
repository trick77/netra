import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import * as api from "../lib/api";
import type { Host, MetricsResponse } from "../lib/api";
import { FleetContainers } from "./fleet/FleetContainers";
import { hostColumns } from "./fleet/hostColumns";
import { Containers } from "./host/tabs/Inventory";
import { Overview } from "./host/tabs/Overview";
import { Table } from "../ui/Table";

vi.mock("../lib/api", async () => {
  const actual = await vi.importActual<typeof api>("../lib/api");
  return { ...actual, getMetrics: vi.fn() };
});

const getMetrics = vi.mocked(api.getMetrics);

beforeEach(() => {
  vi.clearAllMocks();
});

function response(over: Partial<MetricsResponse>): MetricsResponse {
  return {
    family: "host",
    tier: "raw",
    step_s: 3600,
    window: { from: "2026-08-10T00:00:00Z", to: "2026-08-10T03:00:00Z" },
    requested_window: {
      from: "2026-08-10T00:00:00Z",
      to: "2026-08-10T03:00:00Z",
    },
    warnings: [],
    key_columns: [],
    columns: [],
    series: [],
    truncated: false,
    ...over,
  } as MetricsResponse;
}

const t0 = Date.parse("2026-08-10T00:00:00Z");
const hour = 3_600_000;

/** A one-series family=container answer for `shop/api`. */
function containerMetrics(cpu: number[]): MetricsResponse {
  return response({
    family: "container",
    key_columns: ["container"],
    columns: ["cpu_pct", "mem_used", "mem_limit"],
    series: [
      {
        key: { container: "shop/api" },
        points: cpu.map((v, i) => [t0 + i * hour, v, 100, null]),
      },
    ],
  });
}

async function open(name: RegExp | string) {
  await userEvent.click(screen.getByRole("button", { name }));
  return screen.getByRole("dialog");
}

function pick(range: string) {
  return userEvent.click(
    within(screen.getByRole("dialog")).getByRole("button", { name: range }),
  );
}

/**
 * Every sparkline in the app opens the chart behind it.
 *
 * These four lists drew history and then refused to say anything more about
 * it: the enlarge affordance lived inside ChartPanel, which is a card, and a
 * card does not fit in a 32px table cell or a sensor row. One test per site,
 * because the sites wire their own fetch and each one can be wrong on its own.
 */
describe("the list sparklines enlarge", () => {
  describe("the fleet's container list", () => {
    const rows = [
      {
        host_id: 7,
        hostname: "ark",
        container_key: "shop/api",
        name: "api",
        image: "api:1",
        is_agent: false,
        first_seen: null,
        last_seen: null,
        cpu: [1, 2, 3],
        mem: [10, 20, 30],
        mem_limit_bytes: null,
      },
    ] as never;

    it("opens a chart named after the container, and widens it", async () => {
      getMetrics.mockResolvedValue(containerMetrics([40, 50]));

      render(<FleetContainers rows={rows} range="1h" />);
      // Named for the row: twenty "Enlarge CPU" buttons name twenty charts
      // identically.
      await open("Enlarge CPU for api");
      await pick("6h");

      await waitFor(() => expect(getMetrics).toHaveBeenCalledTimes(1));
      expect(getMetrics).toHaveBeenCalledWith(
        7,
        expect.objectContaining({ family: "container" }),
      );
    });

    it("offers only the ranges the fleet page itself offers", async () => {
      render(<FleetContainers rows={rows} range="1h" />);
      const dialog = await open("Enlarge Memory for api");

      // 30d is a range lib/range knows and this page does not: a dialog that
      // offered it would hand the page a range its own picker cannot express.
      expect(
        within(dialog).queryByRole("button", { name: "30d" }),
      ).not.toBeInTheDocument();
      expect(
        within(dialog).getByRole("button", { name: "24h" }),
      ).toBeInTheDocument();
    });
  });

  describe("the host page's container list", () => {
    it("widens the container family for its own host", async () => {
      getMetrics.mockResolvedValue(containerMetrics([40, 50]));

      render(
        <Containers
          hostId={7}
          rows={[
            {
              container_key: "shop/api",
              name: "api",
              image: "api:1",
              is_agent: false,
              first_seen: null,
              last_seen: null,
            } as never,
          ]}
          metrics={containerMetrics([1, 2, 3])}
          range="1h"
        />,
      );
      await open("Enlarge CPU for api");
      await pick("6h");

      await waitFor(() =>
        expect(getMetrics).toHaveBeenCalledWith(
          7,
          expect.objectContaining({ family: "container" }),
        ),
      );
    });
  });

  describe("the fleet's host rows", () => {
    const host = {
      id: 7,
      hostname: "ark",
      site_id: null,
      last_seen: new Date(t0).toISOString(),
      cpu_total: 12,
      mem_used: 1,
      mem_total: 4,
      threads: 4,
      net_rx_bytes: 1,
      net_tx_bytes: 2,
      uptime_s: 1,
    } as unknown as Host;

    const row = {
      ...host,
      site_name: null,
      cpu: [{ name: "busy", color: "var(--s1)", values: [1, 2, 3] }],
      mem: [{ name: "used", color: "var(--s2)", values: [1, 2, 3] }],
      reporting: [1, 2, 3],
      rx: [1, 2, 3],
      tx: [4, 5, 6],
      fullest: null,
      disk: [],
      oomKills: null,
    } as never;

    function renderRow(over: Record<string, unknown> = {}) {
      render(
        <Table
          columns={hostColumns("1h")}
          rows={[{ ...(row as object), ...over } as never]}
          rowKey={() => "r"}
        />,
      );
    }

    it("widens CPU as the family it is actually drawing", async () => {
      getMetrics.mockResolvedValue(response({ family: "cpu_core" }));

      renderRow();
      await open("Enlarge CPU for ark");
      await pick("6h");

      // Both: the per-core stack, and the cpu_total it falls back to.
      await waitFor(() => expect(getMetrics).toHaveBeenCalledTimes(2));
      const families = getMetrics.mock.calls.map(([, p]) => p.family);
      expect(families).toContain("cpu_core");
      expect(families).toContain("host");
    });

    // The guard that stops a 128-core host shipping 128 series from a dialog:
    // above MAX_PER_CORE the cell draws cpu_total, and the enlarged view has
    // to ask for the family it is actually drawing rather than the one the
    // small host beside it draws.
    it("does not ask a large host for its cores", async () => {
      getMetrics.mockResolvedValue(response({}));

      renderRow({ threads: 128 });
      await open("Enlarge CPU for ark");
      await pick("6h");

      await waitFor(() => expect(getMetrics).toHaveBeenCalledTimes(1));
      expect(getMetrics.mock.calls.map(([, p]) => p.family)).toEqual(["host"]);
    });

    it("widens memory and traffic from their own families", async () => {
      getMetrics.mockResolvedValue(response({}));

      renderRow();
      await open("Enlarge Memory for ark");
      await pick("6h");
      await waitFor(() =>
        expect(getMetrics).toHaveBeenCalledWith(
          7,
          expect.objectContaining({ family: "host" }),
        ),
      );
      await userEvent.click(
        within(screen.getByRole("dialog")).getByRole("button", {
          name: "Close",
        }),
      );

      await open("Enlarge Traffic for ark");
      await pick("6h");
      await waitFor(() =>
        expect(getMetrics).toHaveBeenCalledWith(
          7,
          expect.objectContaining({ family: "net" }),
        ),
      );
    });
  });

  describe("the host overview's sensors", () => {
    const sensorMetrics = response({
      family: "sensor",
      key_columns: ["chip", "label", "kind"],
      columns: ["temp", "value"],
      series: [
        {
          key: { chip: "k10temp", label: "Tctl", kind: "temperature" },
          points: [
            [t0, 44, 44],
            [t0 + hour, 47, 47],
          ],
        },
      ],
    });

    function renderOverview(
      fetchFamily?: (family: string, range: never) => Promise<MetricsResponse>,
    ) {
      render(
        <Overview
          host={
            {
              id: 7,
              hostname: "ark",
              capabilities: {},
              last_seen: new Date(t0).toISOString(),
            } as never
          }
          hostMetrics={null}
          filesystemMetrics={null}
          agentMetrics={null}
          sensorMetrics={sensorMetrics}
          containers={null}
          units={null}
          range="1h"
          fetchFamily={fetchFamily as never}
          now={new Date(t0 + hour)}
        />,
      );
    }

    it("opens the temperature chart named after the sensor", async () => {
      renderOverview();
      await open("Enlarge temperature for k10temp Tctl");
    });

    // The card free-scales every row to its own extent, and the chart it
    // opens has to keep that floor: a 44-47 degree package drawn from zero is
    // a flat line, so the big chart would say less than the small one.
    it("keeps the sensor's own floor rather than snapping to zero", async () => {
      renderOverview();
      const dialog = await open("Enlarge temperature for k10temp Tctl");

      const axis = Array.from(
        dialog.querySelector(".cd-y")?.children ?? [],
      ).map((el) => el.textContent);
      expect(axis).toContain("44 °C");
      expect(axis).not.toContain("0 °C");
    });

    it("refetches the sensor family when widened", async () => {
      const fetchFamily = vi.fn().mockResolvedValue(sensorMetrics);

      renderOverview(fetchFamily);
      await open("Enlarge temperature for k10temp Tctl");
      await pick("6h");

      await waitFor(() =>
        expect(fetchFamily).toHaveBeenCalledWith("sensor", "6h"),
      );
    });
  });
});
