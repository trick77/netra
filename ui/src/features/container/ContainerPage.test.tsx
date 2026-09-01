// The fixtures here are shaped like a real family=container response
// (internal/hub/read/metrics.go's Result, transcribed in lib/api.ts): the
// point array leads with an epoch-millis timestamp and every metric column
// is nullable, because the schema declares all six of them nullable and a
// container with no memory limit reports mem_limit as null rather than
// omitting the column.
import { describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Container, MetricsResponse } from "../../lib/api";
import { ContainerPage, deriveState, displayTitle } from "./ContainerPage";
import { ABSENT } from "../../lib/format";

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
  last_seen: "2026-08-10T14:00:00Z",
};

const HOST = {
  id: 3,
  hostname: "web-01",
  last_seen: "2026-08-10T14:00:00Z",
};

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

  // The contradiction this branch exists to end: containerIsGone measures
  // against the host and marks nothing gone on an offline host, so the badge
  // must not call the same container Silent in the same breath.
  it("names the host, not the container, when the host stopped reporting", () => {
    const state = deriveState({
      lastSampleMs: NOW.getTime() - 3_600_000,
      memUsed: 1,
      memLimit: null,
      gap: false,
      now: NOW,
      hostState: { severity: "critical", label: "offline" },
    });
    expect(state.label).toBe("Host offline");
    expect(state.label).not.toMatch(/silent/i);
  });

  // The host carries the severity on its own page and in its fleet row.
  // Repeating it here counts one outage twice.
  it("leaves the host's severity to the host", () => {
    const state = deriveState({
      lastSampleMs: null,
      memUsed: null,
      memLimit: null,
      gap: false,
      now: NOW,
      hostState: { severity: "critical", label: "never seen" },
    });
    expect(state.severity).toBe("neutral");
    expect(state.label).toBe("Host never seen");
  });

  // A host answering badly is still answering: its containers' own readings
  // are current, so the badge keeps measuring them.
  it("still judges the container when the host is merely sporadic", () => {
    const state = deriveState({
      lastSampleMs: NOW.getTime() - 3_600_000,
      memUsed: 1,
      memLimit: null,
      gap: false,
      now: NOW,
      hostState: { severity: "warning", label: "sporadic" },
    });
    expect(state.label).toMatch(/silent/i);
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
      // The explicit tab, matching every other host link in the app --
      // /hosts/3 resolves to the same page, but two spellings of one link is
      // how the two container lists drifted in the first place.
      expect(link).toHaveAttribute("href", "/hosts/3/overview");
    }
    expect(screen.getAllByText("nginx:1.27").length).toBeGreaterThan(0);
  });

  // An empty traffic chart claims this container moved no bytes. When the
  // agent has told us it could not enter the host's network namespaces, that
  // claim is false and the agent's own sentence is the answer.
  it("explains an empty network chart with the capability that caused it", () => {
    renderPage({ containerNetwork: "no-host-netns" });
    // ChartPanel renames its own landmark in the not-collected state, so the
    // panel announces the fact rather than presenting itself as a chart.
    const network = screen.getByLabelText("Network, not collected", {
      selector: "section",
    });
    expect(network).toHaveTextContent(/Not collected/i);
    expect(network).toHaveTextContent(/network namespaces/i);
  });

  // "namespaced" is the value most easily misread as healthy -- it sounds
  // like a description rather than a failure. It means cgroup.procs names
  // host PIDs that the agent, running without the host PID namespace,
  // resolves in its own and finds nothing. containers.go sets the key ONLY
  // to record why networking produced nothing, and clears it on every scrape
  // that works, so this value blanks the panel exactly as the other does.
  it("explains a namespaced agent too, not only a missing host netns", () => {
    renderPage({ containerNetwork: "namespaced" });
    const network = screen.getByLabelText("Network, not collected", {
      selector: "section",
    });
    expect(network).toHaveTextContent(/PID namespace/i);
  });

  // A working collector reports no key at all, so absence -- not a
  // particular value -- is what means "draw the chart".
  it("draws the network chart when the agent reported no capability", () => {
    renderPage();
    const network = screen.getByLabelText("Network chart", {
      selector: "section",
    });
    expect(network).not.toHaveTextContent(/Not collected/i);
  });

  // A value netra does not know the wording of is still the agent reporting
  // a failure, so it blanks the panel and quotes what the agent said rather
  // than drawing the empty chart this prop exists to prevent.
  it("blanks the panel for an unrecognised capability value, quoting it", () => {
    renderPage({ containerNetwork: "some-future-reason" });
    const network = screen.getByLabelText("Network, not collected", {
      selector: "section",
    });
    expect(network).toHaveTextContent(/some-future-reason/);
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
  // multi-series panel names its bands. The network panel is ingress/egress
  // now rather than rx/tx: the direction is the point of that chart, and
  // "rx" is the kernel's word for it rather than the reader's.
  it("legends the multi-series panels", () => {
    renderPage();

    const legends = [...document.querySelectorAll(".legend")];
    expect(legends.map((l) => l.textContent)).toContain("inout");
    expect(legends.map((l) => l.textContent)).toContain("readwrite");
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
    // Inside the Memory panel, not merely somewhere on the page. The bar used
    // to be rendered after the small-multiples grid, which put it under Disk
    // I/O -- `.sm` is auto-fill, so the row after it follows the LAST panel at
    // every width, never the chart the bar is the ceiling for.
    const memory = screen.getByLabelText("Memory chart", {
      selector: "section",
    });
    expect(memory.querySelector(".meter")).not.toBeNull();

    // The bar carries the percentage and nothing else: the panel header above
    // it already prints "used · limit", so repeating the pair inside the same
    // card would say it twice.
    const row = memory.querySelector(".mrow") as HTMLElement;
    expect(within(row).getByText("50%")).toBeInTheDocument();
    expect(row).not.toHaveTextContent("· 1 GB");
  });

  // The panel that collected nothing has no reading to qualify, so the footer
  // must not survive into the not-collected branch -- a bar under the words
  // "Not collected" states a measurement the panel just said it does not have.
  it("draws no memory meter when the panel is not collected", () => {
    renderPage({ containerNetwork: "namespaced" });

    const network = screen.getByLabelText("Network, not collected", {
      selector: "section",
    });
    expect(network.querySelector(".meter")).toBeNull();
    expect(network.querySelector(".foot")).toBeNull();
  });

  // The header pairs a value against the limit, so the value has to be the
  // container's WHOLE memory. Split into anon/file/shmem/kernel, series[0] is
  // the anon band alone, and pairing it against mem_limit read as half the
  // limit used for a container at 90% of it.
  it("headlines the container's total memory, not the anon band", () => {
    const split: MetricsResponse = {
      ...LIMITED,
      columns: [...COLUMNS, "mem_anon", "mem_file", "mem_shmem", "mem_kernel"],
      series: [
        {
          key: { container: "shop/web" },
          points: [
            point(
              "2026-08-10T13:59:00Z",
              [14, 9e8, 1e9, 1.1e6, 2.1e5, 0, 4.2e5, 5e8, 3e8, 6e7, 4e7],
            ),
          ],
        },
      ],
    };
    renderPage({ metrics: split });

    const panel = screen.getByLabelText("Memory chart", {
      selector: "section",
    });
    const now = panel.querySelector(".now")?.textContent;
    expect(now).toBe("0.9 · 1 GB");
    expect(now).not.toContain("anon");
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

    expect(screen.getAllByText(ABSENT).length).toBeGreaterThanOrEqual(2);
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

  // The page renders both the badge and the gone pill from the same host, so
  // this is where the two used to contradict each other: no pill, because
  // containerIsGone spares an offline host's containers, beside a badge
  // calling the container Silent.
  it("blames the host, not the container, when the host went quiet", () => {
    renderPage({
      host: { ...HOST, last_seen: "2026-08-10T12:00:00Z" },
    });

    expect(screen.getByText("Host offline")).toBeInTheDocument();
    expect(screen.queryByText("Silent")).toBeNull();
    expect(screen.queryByText("gone")).toBeNull();
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
    ).toHaveTextContent("14%");
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
