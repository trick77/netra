import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { MetricsResponse } from "../../../lib/api";
import { ABSENT } from "../../../lib/format";
import { STORAGE_GROUPS } from "../chartSpecs";
import { DriveRows, FilesystemTable } from "./Storage";

const t0 = Date.parse("2026-08-10T00:00:00Z");
const window = { from: "2026-08-10T00:00:00Z", to: "2026-08-10T00:01:00Z" };
const base = {
  tier: "raw",
  step_s: 60,
  window,
  requested_window: window,
  warnings: [],
  truncated: false,
};

describe("FilesystemTable", () => {
  const mounts = [
    { id: 1, label: "root", mountpoint: "/", device_id: null },
    { id: 2, label: "data", mountpoint: "/data", device_id: null },
    { id: 3, label: "media", mountpoint: "/media", device_id: null },
    // In the inventory, never in the metrics
    { id: 4, label: "gone", mountpoint: "/gone", device_id: null },
  ];
  // root: 500 GB, 450 used, 50 free -> 90% of what df counts.
  // data: 8 TB, 800 used, 7200 free -> 10%.
  // media: 900 GB, 450 used, 450 free -> 50%, a quarter of its inodes used.
  const sizes = {
    ...base,
    family: "filesystem",
    key_columns: ["filesystem"],
    columns: ["total", "used", "free", "inodes_used", "inodes_total"],
    series: [
      {
        key: { filesystem: "root" },
        points: [[t0, 500e9, 450e9, 50e9, null, null]],
      },
      {
        key: { filesystem: "data" },
        points: [[t0, 8000e9, 800e9, 7200e9, null, null]],
      },
      {
        key: { filesystem: "media" },
        points: [[t0, 900e9, 450e9, 450e9, 250, 1000]],
      },
    ],
  } as unknown as MetricsResponse;

  // The mountpoint, without the label printed small beside it
  const firstCells = () =>
    screen
      .getAllByRole("row")
      .slice(1)
      .map(
        (r) =>
          within(r).getAllByRole("cell")[0]!.querySelector(".fsname")!
            .firstChild!.textContent,
      );

  it("puts the fullest mount first, and one never measured last", () => {
    render(<FilesystemTable rows={mounts} metrics={sizes} />);
    expect(firstCells()).toEqual(["/", "/media", "/data", "/gone"]);
  });

  it("reads what is left, how big, and the inode figure", () => {
    render(<FilesystemTable rows={mounts} metrics={sizes} />);
    const media = screen.getAllByRole("row")[2]!;
    expect(media).toHaveTextContent("450 GB");
    expect(media).toHaveTextContent("900 GB");
    expect(media).toHaveTextContent("25%");
    // A mount the metrics never answered for is not an empty disk
    const gone = screen.getAllByRole("row")[4]!;
    expect(gone.textContent).toContain(ABSENT);
    expect(gone.querySelector(".segbar")).toBeNull();
  });

  it("orders Size by bytes and not by the formatted string", async () => {
    render(<FilesystemTable rows={mounts} metrics={sizes} />);
    await userEvent.click(
      within(screen.getByRole("columnheader", { name: /size/i })).getByRole(
        "button",
      ),
    );
    // Ascending: 500 GB, 900 GB, 8 TB, then the unmeasured one
    expect(firstCells().slice(0, 3)).toEqual(["/", "/media", "/data"]);
  });

  // 96% is red on a 250 GB root and on a 4 TB array alike: the figure
  // answers "what does this number say", and the bytes beside it say the rest.
  it("reddens the figure of every mount at 96%, whatever its size", () => {
    const at96 = (used: number, free: number) => [t0, used + free, used, free];
    render(
      <FilesystemTable
        rows={mounts.slice(0, 2)}
        metrics={
          {
            ...sizes,
            columns: ["total", "used", "free"],
            series: [
              { key: { filesystem: "root" }, points: [at96(241e9, 9e9)] },
              { key: { filesystem: "data" }, points: [at96(3840e9, 160e9)] },
            ],
          } as unknown as MetricsResponse
        }
      />,
    );
    for (const label of ["root", "data"]) {
      const meter = screen
        .getByText(label, { selector: ".lab" })
        .closest(".mrow") as HTMLElement;
      expect(meter.querySelector(".val")).toHaveClass("st-crit");
    }
  });

  // The stored gauge: a host that is switched off answers nothing in the
  // window, and the hub's last reading of each mount is then the true one.
  // A mount that stopped reporting while the host kept talking is retired,
  // and keeps its row without its numbers.
  it("falls back to each mount's stored reading, unless it is retired", () => {
    const at = "2026-08-10T02:00:00Z";
    const mount = (id: number, label: string, ts: string, used: number) => ({
      id,
      label,
      mountpoint: `/mnt/${label}`,
      device_id: null,
      ts,
      total: 100e9,
      used: used * 1e9,
      free: (100 - used) * 1e9,
    });
    render(
      <FilesystemTable
        rows={[
          mount(1, "pool", at, 87),
          mount(2, "old", "2026-08-09T02:00:00Z", 94),
        ]}
        metrics={null}
        lastSeen={at}
      />,
    );
    const pool = screen.getByText("/mnt/pool").closest("tr")!;
    expect(pool).toHaveTextContent("87%");
    expect(pool).toHaveTextContent("13 GB");
    const old = screen.getByText("/mnt/old").closest("tr")!;
    expect(old.textContent).toContain(ABSENT);
    expect(old.querySelector(".segbar")).toBeNull();
  });

  // FUSE can report Bfree > Blocks: no Use% exists, so no 200% red bar.
  it("draws no meter for a negative capacity", () => {
    render(
      <FilesystemTable
        rows={mounts.slice(1, 2)}
        metrics={
          {
            ...sizes,
            columns: ["total", "used", "free"],
            series: [
              { key: { filesystem: "data" }, points: [[t0, 10, -20, 10]] },
            ],
          } as unknown as MetricsResponse
        }
      />,
    );
    expect(document.querySelector(".segbar")).toBeNull();
  });
});

describe("DriveRows", () => {
  const diskIo = {
    ...base,
    family: "disk_io",
    key_columns: ["device"],
    columns: [
      "read_bytes",
      "write_bytes",
      "io_util_pct",
      "r_await_ms",
      "w_await_ms",
      "read_ops",
      "write_ops",
    ],
    series: [
      {
        key: { device: "sda" },
        points: [[t0, 1000, 2000, 40, 1, 2, 10, 20]],
      },
      {
        key: { device: "sdb" },
        points: [[t0, 10, 20, 1, 3, 4, 1, 2]],
      },
    ],
  } as unknown as MetricsResponse;
  const group = STORAGE_GROUPS.find((g) => g.form === "drives")!;

  it("draws one row per drive, its figures above each column", () => {
    render(<DriveRows group={group} sources={{ diskIo }} />);
    const rows = screen.getAllByRole("row").slice(1);
    expect(
      rows.map((r) => within(r).getByRole("rowheader").textContent),
    ).toEqual(["sdaoperations/s", "sdboperations/s"]);
    expect(rows[0]).toHaveTextContent("1 kB/s · 2 kB/s");
    expect(rows[0]).toHaveTextContent("40% · peak 40%");
    expect(rows[1]).toHaveTextContent("4 ms · peak 4 ms");
  });

  // Operations per second are no longer a chart on the tab; they are one
  // click from the drive they belong to.
  it("opens a drive's operations per second from its row", () => {
    render(<DriveRows group={group} sources={{ diskIo }} />);
    expect(
      screen.getByRole("button", { name: "sdb: operations per second" }),
    ).toBeInTheDocument();
  });

  it("says why when no drive reported", () => {
    render(<DriveRows group={group} sources={{ diskIo: null }} />);
    expect(
      screen.getByText("No data has been read for this family yet."),
    ).toBeInTheDocument();
  });

  // A drive with no latency columns has no latency, not a 0 ms one.
  it("prints no latency for a drive that reported none", () => {
    const noAwait = {
      ...diskIo,
      columns: ["read_bytes", "write_bytes", "io_util_pct"],
      series: diskIo.series.map((s) => ({
        ...s,
        points: s.points.map((p) => p.slice(0, 4)),
      })),
    } as unknown as MetricsResponse;
    render(<DriveRows group={group} sources={{ diskIo: noAwait }} />);
    const row = screen.getAllByRole("row")[1]!;
    expect(row).toHaveTextContent(`${ABSENT} · peak ${ABSENT}`);
    expect(row).not.toHaveTextContent("Infinity");
  });

  // The enlarged cell keeps its range picker: the spec at another range,
  // narrowed to this drive and drawn in the same colours.
  it("widens a drive's cell to that drive alone", async () => {
    const fetchFamily = vi.fn().mockResolvedValue(diskIo);
    render(
      <DriveRows group={group} sources={{ diskIo, fetchFamily }} range="1h" />,
    );
    await userEvent.click(
      screen.getByRole("button", {
        name: "Disk throughput: sdb: open the chart",
      }),
    );
    const dialog = await screen.findByRole("dialog");
    // The rail asks for the other ranges of the same family...
    await waitFor(() =>
      expect(fetchFamily).toHaveBeenCalledWith("disk_io", "6h"),
    );
    // ...and the dialog names this drive's read and write, not sda's
    await userEvent.click(
      within(dialog).getByRole("button", { name: /last 6h$/ }),
    );
    await waitFor(() =>
      expect(
        within(dialog)
          .getAllByRole("row")
          .map((r) => r.textContent),
      ).toEqual([
        expect.stringMatching(/^Series/),
        expect.stringMatching(/^read/),
        expect.stringMatching(/^write/),
      ]),
    );
  });
});
