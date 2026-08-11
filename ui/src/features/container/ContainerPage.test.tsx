// The fixtures here are shaped like a real family=container response
// (internal/hub/read/metrics.go's Result, transcribed in lib/api.ts): the
// point array leads with an epoch-millis timestamp and every metric column
// is nullable, because the schema declares all six of them nullable and a
// container with no memory limit reports mem_limit as null rather than
// omitting the column.
import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Container, MetricsResponse } from "../../lib/api";
import { ContainerPage, deriveState, displayTitle } from "./ContainerPage";

const COLUMNS = [
  "cpu_pct",
  "mem_used",
  "mem_limit",
  "net_rx",
  "net_tx",
  "io_read",
  "io_write",
];

const NOW = new Date("2026-08-10T14:00:00Z");

type Cell = number | null;

function point(ts: string, cells: Cell[]): unknown[] {
  return [new Date(ts).getTime(), ...cells];
}

function metrics(rows: unknown[][]): MetricsResponse {
  return {
    family: "container",
    tier: "raw",
    step_s: 60,
    window: { from: "2026-08-10T13:00:00Z", to: "2026-08-10T14:00:00Z" },
    requested_window: {
      from: "2026-08-10T13:00:00Z",
      to: "2026-08-10T14:00:00Z",
    },
    warnings: [],
    key_columns: ["container"],
    columns: COLUMNS,
    series: [{ key: { container: "shop/web" }, points: rows }],
    truncated: false,
  };
}

// mem_limit as the third cell: present and null on every point is exactly
// what an unlimited container looks like on the wire.
const UNLIMITED = metrics([
  point("2026-08-10T13:58:00Z", [12, 5e8, null, 1e6, 2e5, 0, 4e5]),
  point("2026-08-10T13:59:00Z", [14, 5.2e8, null, 1.1e6, 2.1e5, 0, 4.2e5]),
]);

const LIMITED = metrics([
  point("2026-08-10T13:58:00Z", [12, 5e8, 1e9, 1e6, 2e5, 0, 4e5]),
  point("2026-08-10T13:59:00Z", [14, 5e8, 1e9, 1.1e6, 2.1e5, 0, 4.2e5]),
]);

const CONTAINER: Container = {
  id: 7,
  container_key: "shop/web",
  name: "shop-web-1",
  image: "nginx:1.27",
  is_agent: false,
};

const HOST = { id: 3, hostname: "web-01" };

function renderPage(
  overrides: Partial<Parameters<typeof ContainerPage>[0]> = {},
) {
  const onRangeChange = vi.fn();
  render(
    <ContainerPage
      container={CONTAINER}
      host={HOST}
      metrics={LIMITED}
      range="1h"
      onRangeChange={onRangeChange}
      now={NOW}
      {...overrides}
    />,
  );
  return { onRangeChange };
}

describe("displayTitle", () => {
  // container_key IS the compose project + service (ingest.proto's
  // ContainerSample, agent/collector/containers.go's containerKey), so the
  // Display title is the key -- until the key falls all the way through to
  // the Docker id, which is the one thing the header must never be.
  it("uses container_key, which is already project/service", () => {
    expect(displayTitle(CONTAINER)).toBe("shop/web");
  });

  it("falls back to the name when the key degraded to a Docker id", () => {
    expect(
      displayTitle({
        ...CONTAINER,
        container_key: "3f2b1c8d9e0a4b5c6d7e8f90a1b2c3d4",
      }),
    ).toBe("shop-web-1");
  });
});

describe("deriveState", () => {
  it("says no samples rather than reporting zero", () => {
    const state = deriveState({
      lastSampleMs: null,
      memUsed: null,
      memLimit: null,
      gap: false,
      now: NOW,
    });
    expect(state.label).toMatch(/no samples/i);
  });

  it("calls a container that stopped appearing silent", () => {
    const state = deriveState({
      lastSampleMs: NOW.getTime() - 3_600_000,
      memUsed: 1,
      memLimit: null,
      gap: false,
      now: NOW,
    });
    expect(state.label).toMatch(/silent/i);
    expect(state.severity).toBe("serious");
  });

  // Memory approaching mem_limit is a warning (spec 11); a gap is reported
  // as a gap, never as the restart it probably was.
  it("warns on memory approaching mem_limit", () => {
    const state = deriveState({
      lastSampleMs: NOW.getTime() - 60_000,
      memUsed: 9.6e8,
      memLimit: 1e9,
      gap: false,
      now: NOW,
    });
    expect(state.label).toMatch(/mem_limit/);
    expect(state.severity).toBe("warning");
  });

  it("reports a gap as a gap, not as a restart", () => {
    const state = deriveState({
      lastSampleMs: NOW.getTime() - 60_000,
      memUsed: 1e8,
      memLimit: 1e9,
      gap: true,
      now: NOW,
    });
    expect(state.label).toMatch(/gap/i);
    expect(state.label).not.toMatch(/restart/i);
    expect(state.why).toMatch(/restart/i);
  });
});

describe("ContainerPage", () => {
  it("heads the page with project/service, the host as a link, and the image", () => {
    renderPage();

    expect(
      screen.getByRole("heading", { level: 1, name: "shop/web" }),
    ).toBeInTheDocument();
    // Twice on purpose: once in the header, once in the Identity card.
    for (const link of screen.getAllByRole("link", { name: "web-01" })) {
      expect(link).toHaveAttribute("href", "/hosts/3");
    }
    expect(screen.getAllByText("nginx:1.27").length).toBeGreaterThan(0);
  });

  it("labels the status badge as derived", () => {
    renderPage();
    expect(screen.getByText(/derived from samples/i)).toBeInTheDocument();
  });

  it("renders the four small multiples from ChartPanel", () => {
    renderPage();

    for (const title of ["CPU", "Memory", "Network", "Disk I/O"]) {
      expect(
        screen.getByLabelText(`${title} chart`, { selector: "section" }),
      ).toBeInTheDocument();
    }
  });

  // Two series on one axis cannot be told apart by colour alone, so each
  // two-series panel names its bands.
  it("legends the two-series panels", () => {
    renderPage();

    const legends = [...document.querySelectorAll(".legend")];
    expect(legends).toHaveLength(2);
    expect(legends.map((l) => l.textContent)).toEqual(["rxtx", "readwrite"]);
  });

  it("shows 'no limit' rather than a bar when mem_limit is null", () => {
    renderPage({ metrics: UNLIMITED });

    expect(screen.getByText("no limit")).toBeInTheDocument();
    expect(document.querySelector(".meter")).toBeNull();
  });

  it("renders network and disk as bytes per second, never as bits", () => {
    renderPage();

    const network = screen.getByLabelText("Network chart", {
      selector: "section",
    });
    expect(network).toHaveTextContent("1.1 MB/s");
    expect(network).not.toHaveTextContent("b/s");
  });

  it("meters memory against mem_limit when there is one", () => {
    renderPage();

    expect(screen.queryByText("no limit")).toBeNull();
    expect(document.querySelector(".meter")).not.toBeNull();
    expect(screen.getByText(/of 1 GB/)).toBeInTheDocument();
  });

  it("identifies the container by key, name, image, host and is_agent", () => {
    renderPage();

    for (const label of [
      "container_key",
      "name",
      "image",
      "Host",
      "is_agent",
      "Last sample",
    ]) {
      expect(screen.getByText(label)).toBeInTheDocument();
    }
    expect(screen.getByText("shop-web-1")).toBeInTheDocument();
    expect(screen.getByText("no")).toBeInTheDocument();
  });

  it("renders an absent name or image as the absent marker, never blank", () => {
    renderPage({ container: { ...CONTAINER, name: null, image: null } });

    expect(screen.getAllByText("—").length).toBeGreaterThanOrEqual(2);
  });

  // The four fields that reach neither the wire (ingest.proto's
  // ContainerSample) nor the schema (the containers table).
  it("names the four fields that are not collected", () => {
    renderPage();

    const card = screen.getByText("Not collected").closest(".card")!;
    for (const field of ["Health", "Restarts", "State", "Labels"]) {
      expect(card).toHaveTextContent(field);
    }
  });

  it("hands a range change back to the caller", async () => {
    const { onRangeChange } = renderPage();

    await userEvent.click(screen.getByRole("button", { name: "24h" }));

    expect(onRangeChange).toHaveBeenCalledWith("24h");
  });

  // A host's response carries every container on that host, in whatever
  // order the hub returned them.
  it("charts the series belonging to this container, not the first one", () => {
    renderPage({
      metrics: {
        ...LIMITED,
        series: [
          {
            key: { container: "shop/worker" },
            points: [point("2026-08-10T13:59:00Z", [99, 1e8, 1e9, 0, 0, 0, 0])],
          },
          ...LIMITED.series,
        ],
      },
    });

    expect(
      screen.getByLabelText("CPU chart", { selector: "section" }),
    ).toHaveTextContent("14 %");
  });

  it("survives a container that has never been sampled", () => {
    renderPage({ metrics: { ...LIMITED, series: [] } });

    expect(screen.getByText(/no samples/i)).toBeInTheDocument();
    expect(
      screen.getByLabelText("CPU chart", { selector: "section" }),
    ).toBeInTheDocument();
    // Never sampled is not the same claim as "no limit configured".
    expect(screen.queryByText("no limit")).toBeNull();
  });
});
