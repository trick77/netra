import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import type { HostRow } from "./hostColumns";
import {
  AllHostsOverlay,
  fromHostRows,
  outlier,
  type OverlayHost,
} from "./AllHostsOverlay";

function host(overrides: Partial<OverlayHost> = {}): OverlayHost {
  return {
    id: 1,
    hostname: "web-01",
    cpu: [10, 11, 10],
    mem: [4e9, 4e9, 4e9],
    memTotal: 16e9,
    ...overrides,
  };
}

describe("outlier", () => {
  it("names the host behaving differently, not the busiest-on-average pack", () => {
    const pack = [
      host({ id: 1, hostname: "a", cpu: [10, 11, 10] }),
      host({ id: 2, hostname: "b", cpu: [11, 10, 11] }),
      host({ id: 3, hostname: "c", cpu: [10, 10, 11] }),
      host({ id: 4, hostname: "d", cpu: [90, 92, 91] }),
    ];

    expect(outlier(pack, (h) => h.cpu)).toBe("d");
  });

  it("has no opinion with fewer than two hosts", () => {
    expect(outlier([host()], (h) => h.cpu)).toBeNull();
    expect(outlier([], (h) => h.cpu)).toBeNull();
  });

  it("ignores hosts with no readings rather than scoring them as zero", () => {
    const hosts = [
      host({ id: 1, hostname: "a", cpu: [50, 50] }),
      host({ id: 2, hostname: "b", cpu: [51, 49] }),
      host({ id: 3, hostname: "silent", cpu: [null, null] }),
    ];

    expect(outlier(hosts, (h) => h.cpu)).not.toBe("silent");
  });
});

describe("fromHostRows", () => {
  it("sums the stacked bands back into one silhouette per host", () => {
    const row = {
      id: 4,
      hostname: "web-04",
      mem_total: 8e9,
      cpu: [
        { name: "user", color: "var(--s1)", values: [10, 20] },
        { name: "system", color: "var(--s2)", values: [5, 5] },
      ],
      mem: [{ name: "used", color: "var(--s1)", values: [1e9, 2e9] }],
    } as unknown as HostRow;

    expect(fromHostRows([row])[0]).toMatchObject({
      hostname: "web-04",
      cpu: [15, 25],
      mem: [1e9, 2e9],
      memTotal: 8e9,
    });
  });

  it("keeps a gap a gap: a null in any band leaves the total unknown", () => {
    const row = {
      id: 5,
      hostname: "web-05",
      mem_total: null,
      cpu: [
        { name: "user", color: "var(--s1)", values: [10, null] },
        { name: "system", color: "var(--s2)", values: [5, 5] },
      ],
      mem: [],
    } as unknown as HostRow;

    expect(fromHostRows([row])[0]!.cpu).toEqual([15, null]);
  });
});

describe("AllHostsOverlay", () => {
  const fleet = [
    host({ id: 1, hostname: "a", cpu: [10, 11], mem: [4e9, 4e9] }),
    host({ id: 2, hostname: "b", cpu: [11, 10], mem: [4e9, 4.1e9] }),
    host({ id: 3, hostname: "spiky", cpu: [90, 95], mem: [15e9, 15.5e9] }),
  ];

  it("draws CPU and memory on shared axes", () => {
    render(<AllHostsOverlay hosts={fleet} />);

    expect(screen.getByRole("img", { name: /CPU/i })).toBeInTheDocument();
    expect(screen.getByRole("img", { name: /memory/i })).toBeInTheDocument();
  });

  it("de-emphasises every host and labels only the outlier", () => {
    const { container } = render(<AllHostsOverlay hosts={fleet} />);

    const chart = screen.getByRole("img", { name: /CPU/i });
    expect(
      chart.querySelector('[data-series="a"]')!.getAttribute("opacity"),
    ).toBe("0.35");
    expect(
      chart.querySelector('[data-series="spiky"]')!.getAttribute("opacity"),
    ).toBe("1");
    expect(container.textContent).toContain("spiky");
  });

  // Absolute bytes on a shared axis make a 512 GB host dwarf a 16 GB one and
  // destroy the comparison the chart exists for.
  it("scales memory as a percentage of each host's own total", () => {
    render(<AllHostsOverlay hosts={fleet} />);

    expect(screen.getByRole("img", { name: /memory/i })).toBeInTheDocument();
    expect(screen.getByText(/% of each host/i)).toBeInTheDocument();
  });

  // The chart is drawn in percent, so the outlier must be picked in percent
  // too: on raw bytes a roomy big host outranks a small host that is nearly
  // full, and the caption would name a line that is visibly in the pack.
  it("picks the memory outlier on the same scale it draws", () => {
    const mixed = [
      host({ id: 1, hostname: "big", mem: [200e9, 200e9], memTotal: 512e9 }),
      host({ id: 2, hostname: "mid", mem: [6e9, 6e9], memTotal: 16e9 }),
      host({ id: 3, hostname: "tight", mem: [15e9, 15e9], memTotal: 16e9 }),
    ];
    render(<AllHostsOverlay hosts={mixed} />);

    const mem = screen.getByRole("img", { name: /memory/i });
    expect(
      mem.querySelector('[data-series="tight"]')!.getAttribute("opacity"),
    ).toBe("1");
    expect(
      mem.querySelector('[data-series="big"]')!.getAttribute("opacity"),
    ).toBe("0.35");
  });

  it("counts one host as one host", () => {
    render(<AllHostsOverlay hosts={[host({ hostname: "only" })]} />);

    expect(screen.getAllByText(/1 host\b/).length).toBeGreaterThan(0);
  });

  it("leaves a host with no known memory total out of the memory chart", () => {
    const { container } = render(
      <AllHostsOverlay
        hosts={[...fleet, host({ id: 9, hostname: "unknown", memTotal: null })]}
      />,
    );

    const mem = screen.getByRole("img", { name: /memory/i });
    expect(mem.querySelector('[data-series="unknown"]')).toBeNull();
    // ...but it is still on the CPU chart, and its absence is stated.
    expect(container.textContent).toMatch(/1 host/i);
  });

  it("renders nothing when no host has a reading yet", () => {
    const { container } = render(
      <AllHostsOverlay hosts={[host({ cpu: [], mem: [], memTotal: null })]} />,
    );

    expect(container).toBeEmptyDOMElement();
  });
});
