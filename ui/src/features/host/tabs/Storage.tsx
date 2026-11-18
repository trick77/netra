// The Storage tab's two tables: a row per filesystem and a row per drive.
//
// They replace the Mounts table, the three filesystem panels and the four
// disk panels. A mount is one figure and one trend; a drive is throughput,
// utilisation and latency side by side, each column on one scale for every
// drive, so a sixth drive adds a row rather than six more unlabelled lines.
import type { ReactNode } from "react";
import type { Filesystem, MetricsResponse } from "../../../lib/api";
import type { Range } from "../../../lib/range";
import { RAIL_RANGES } from "../../../lib/range";
import { ABSENT, bytes, percent } from "../../../lib/format";
import { griddedValues, latestValue } from "../../../lib/metrics";
import { fsUsePercent } from "../../../lib/bands";
import { currentFilesystems } from "../../../lib/host";
import { Table, type Column } from "../../../ui/Table";
import { Meter, trendColor } from "../../../ui/Meter";
import { Sparkline } from "../../../ui/charts/Sparkline";
import { Chart } from "../../../ui/charts/Chart";
import { Enlargeable, type DetailData } from "../../../ui/charts/Enlargeable";
import { useMeasuredWidth } from "../../../ui/charts/useMeasuredWidth";
import type { Band } from "../../../ui/charts/ChartPanel";
import {
  specForSlug,
  type Family,
  type PanelGroup,
  type PanelSpec,
} from "../chartSpecs";
import type { GraphsProps } from "./Graphs";
import { bandPeak } from "./PairRows";
import { resolveSpec } from "./resolveSpec";

// --- Filesystems ----------------------------------------------------------

interface FsRow {
  key: string;
  label: string;
  mountpoint: string | null;
  size: number | null;
  free: number | null;
  /** df's Use%: used / (used + free), the root reserve left out. */
  pct: number | null;
  inodes: number | null;
  trend: (number | null)[];
}

const TREND_WIDTH = 140;
const TREND_HEIGHT = 24;

/**
 * One row per mount: how full, how much is left, how big, how many inodes,
 * and the Use% over the window. Fullest first: the row a reader opens the
 * tab for is the one at the top.
 */
export function FilesystemTable({
  rows,
  metrics,
  range,
  lastSeen,
  fetchFamily,
}: {
  /** The inventory, or null when it has not been read. */
  rows: readonly Filesystem[] | null;
  metrics: MetricsResponse | null;
  range?: Range;
  /** The filesystem family at another range, for a trend's enlarged view. */
  fetchFamily?: (family: Family, range: Range) => Promise<MetricsResponse>;
  /**
   * The host's last_seen, which dates the stored gauge on each row. The
   * window answers nothing for a host that is switched off, and the hub's
   * last reading of each mount is then the true one -- unless the mount
   * stopped reporting while the host kept talking (currentFilesystems).
   */
  lastSeen?: string | null;
}) {
  const index = new Map(
    (metrics?.series ?? []).map((s, i) => [s.key.filesystem, i]),
  );
  const fresh = new Set(
    (
      currentFilesystems({
        last_seen: lastSeen ?? null,
        filesystems: [...(rows ?? [])],
      }) ?? []
    ).map((fs) => fs.id),
  );
  const joined: FsRow[] = (rows ?? []).map((row) => {
    // The stored gauge, for a figure the window does not carry.
    const gauge = fresh.has(row.id) ? row : null;
    const gaugePct =
      gauge?.used != null && gauge.free != null && gauge.used + gauge.free > 0
        ? (gauge.used / (gauge.used + gauge.free)) * 100
        : null;
    const i = index.get(row.label);
    if (metrics === null || i === undefined)
      return {
        key: String(row.id),
        label: row.label,
        mountpoint: row.mountpoint ?? null,
        size: gauge?.total ?? null,
        free: gauge?.free ?? null,
        pct: gaugePct,
        inodes: null,
        trend: [],
      };
    // The latest bucket, a gap included, and the stored gauge where it is
    // empty: a mount that stopped reporting mid-window has no reading NOW,
    // and the gauge says so too once it is stale (currentFilesystems).
    const trend = fsUsePercent(metrics, i);
    const at = (column: string) =>
      latestValue(griddedValues(metrics, i, column));
    const used = at("inodes_used");
    const total = at("inodes_total");
    return {
      key: String(row.id),
      label: row.label,
      mountpoint: row.mountpoint ?? null,
      size: at("total") ?? gauge?.total ?? null,
      free: at("free") ?? gauge?.free ?? null,
      pct: latestValue(trend) ?? gaugePct,
      inodes:
        used !== null && total !== null && total > 0
          ? (used / total) * 100
          : null,
      trend,
    };
  });

  const spec = specForSlug("host-filesystem")!;
  const columns: Column<FsRow>[] = [
    {
      key: "mount",
      header: "Mount",
      cell: (row) => (
        <span className="fsname">
          {row.mountpoint ?? row.label}
          {row.mountpoint !== null && row.mountpoint !== row.label && (
            <small>{row.label}</small>
          )}
        </span>
      ),
      sortValue: (row) => row.mountpoint ?? row.label,
    },
    {
      key: "used",
      header: "Used",
      cell: (row) =>
        row.pct === null ? (
          ABSENT
        ) : (
          // Meter's 70/95, the fleet disk cell's and the Overview's rule.
          <div className="usage-cell">
            <Meter value={row.pct} max={100} label={row.label} />
          </div>
        ),
      sortValue: (row) => row.pct,
    },
    {
      key: "left",
      header: "Left",
      align: "right",
      cell: (row) => bytes(row.free),
      sortValue: (row) => row.free,
    },
    {
      key: "size",
      header: "Size",
      align: "right",
      cell: (row) => bytes(row.size),
      sortValue: (row) => row.size,
    },
    {
      key: "inodes",
      header: "Inodes",
      align: "right",
      // A figure: inode exhaustion presents as "disk full" with space
      // showing, and this column is what explains it.
      cell: (row) => (row.inodes === null ? ABSENT : percent(row.inodes)),
      sortValue: (row) => row.inodes,
    },
    {
      key: "trend",
      header: "Trend",
      cell: (row) =>
        row.trend.length === 0 ? null : (
          <Enlargeable
            title={`${spec.title}: ${row.mountpoint ?? row.label}`}
            series={[
              {
                name: row.mountpoint ?? row.label,
                color: trendColor(row.pct),
                values: row.trend,
              },
            ]}
            min={0}
            max={100}
            filled
            fmt={spec.fmt}
            window={metrics?.window ?? null}
            range={range}
            ranges={RAIL_RANGES}
            fetchSeries={
              fetchFamily
                ? async (next: Range) => {
                    const res = await fetchFamily("filesystem", next);
                    const j = res.series.findIndex(
                      (s) => s.key.filesystem === row.label,
                    );
                    return {
                      series:
                        j === -1
                          ? []
                          : [
                              {
                                name: row.mountpoint ?? row.label,
                                color: trendColor(row.pct),
                                values: fsUsePercent(res, j),
                              },
                            ],
                      window: res.window ?? null,
                    };
                  }
                : undefined
            }
            label={`${row.mountpoint ?? row.label}: open the chart`}
          >
            <Sparkline
              values={row.trend}
              width={TREND_WIDTH}
              height={TREND_HEIGHT}
              min={0}
              max={100}
              color={trendColor(row.pct)}
              label={`${row.label} use over time`}
            />
          </Enlargeable>
        ),
    },
  ];

  // The table alone: the caller gives it its frame -- a card beside the
  // Events card on the Overview, a section head on the Storage tab. A bare
  // header row says nothing, so an unread or empty inventory says which.
  if (joined.length === 0)
    return (
      <p className="note">
        {rows === null
          ? "No filesystems have been read yet."
          : "This host reports no filesystems."}
      </p>
    );
  return (
    <Table
      columns={columns}
      rows={joined}
      rowKey={(row) => row.key}
      defaultSort={{ key: "used", dir: "desc" }}
    />
  );
}

// --- Drives ---------------------------------------------------------------

/** A drive's bands from one spec, by the device that names them. */
function byDevice(spec: PanelSpec, bands: Band[]): Map<string, Band[]> {
  const out = new Map<string, Band[]>();
  for (const band of bands) {
    let device = band.name;
    for (const { label } of spec.bases) {
      if (band.name.endsWith(` ${label}`)) {
        device = band.name.slice(0, -label.length - 1);
        break;
      }
    }
    out.set(device, [...(out.get(device) ?? []), band]);
  }
  return out;
}

const CELL_HEIGHT = 40;

/**
 * One row per drive: throughput mirrored (read above, write below),
 * utilisation and latency. One scale per column for every drive, so a quiet
 * disk reads as quiet next to a busy one. Operations per second, which the
 * tab no longer charts, are one click away under the device name.
 */
export function DriveRows({
  group,
  sources,
  range,
}: {
  group: PanelGroup;
  sources: GraphsProps;
  range?: Range;
}) {
  const { ref, width } = useMeasuredWidth<HTMLDivElement>(200);
  const view = (slug: string) => {
    const spec = group.specs.find((s) => s.slug === slug)!;
    const v = resolveSpec(spec, sources, sources.fetchFamily);
    return { spec, ...v, devices: byDevice(spec, v.series) };
  };
  const io = view("disk-throughput");
  const util = view("disk-utilisation");
  const lat = view("disk-latency");
  const ops = view("disk-operations");

  const devices = [...io.devices.keys()];
  if (devices.length === 0)
    return (
      <section className="driverows" aria-label="Drives">
        <h3 className="grouphead">Drives</h3>
        <p className="note">{io.unavailable}</p>
      </section>
    );

  const ioPeak = {
    up: Math.max(...devices.map((d) => bandPeak(io.devices.get(d)?.[0]))),
    down: Math.max(...devices.map((d) => bandPeak(io.devices.get(d)?.[1]))),
  };
  const latMax = Math.max(
    1,
    ...devices.flatMap((d) => (lat.devices.get(d) ?? []).map(bandPeak)),
  );
  const window = io.res?.window ?? null;

  /**
   * One device's bands from one spec, named by their own label and coloured
   * by the rule: a direction pair in the traffic greys, a lone series in
   * ink. The same shaping for the row and for the enlarged view at another
   * range, so widening a cell cannot redraw a different chart.
   */
  const shape = (
    v: ReturnType<typeof view>,
    device: string,
    bands: Band[] = v.devices.get(device) ?? [],
  ): Band[] => {
    const own = bands.length === 1;
    return bands.map((b, k) => ({
      ...b,
      name: b.name.slice(device.length + 1) || b.name,
      color: own
        ? "var(--ink-2)"
        : k === 0
          ? "var(--traffic-in)"
          : "var(--traffic-out)",
    }));
  };
  /** The spec at another range, narrowed to this device and shaped. */
  const widen = (v: ReturnType<typeof view>, device: string) =>
    v.fetchSeries
      ? async (next: Range) => {
          const wide = await v.fetchSeries!(next);
          return {
            ...wide,
            series: shape(
              v,
              device,
              byDevice(v.spec, wide.series).get(device) ?? [],
            ),
          };
        }
      : undefined;

  const cell = (
    title: string,
    series: Band[],
    figure: string,
    chart: ReactNode,
    extra: {
      mirrored?: boolean;
      filled?: boolean;
      max?: number;
      min?: number;
      fmt?: PanelSpec["fmt"];
      unit?: string;
      fetchSeries?: (range: Range) => Promise<DetailData>;
    },
  ) => (
    <>
      <div className="fig">{figure}</div>
      <Enlargeable
        title={title}
        series={series}
        window={window}
        range={range}
        ranges={RAIL_RANGES}
        label={`${title}: open the chart`}
        {...extra}
      >
        {chart}
      </Enlargeable>
    </>
  );

  return (
    <section className="driverows" aria-label="Drives">
      <h3 className="grouphead">
        Drives <small>one row per drive, one scale per column</small>
      </h3>
      <div className="tablewrap">
        <table>
          <thead>
            <tr>
              <th scope="col">Drive</th>
              <th scope="col">Read above, write below</th>
              <th scope="col">Utilisation</th>
              <th scope="col">Latency</th>
            </tr>
          </thead>
          <tbody>
            {devices.map((device, i) => {
              const pair = shape(io, device);
              const u = shape(util, device);
              const l = shape(lat, device);
              const o = shape(ops, device);
              // The latest bucket, a gap included: a drive on a host that
              // stopped reporting has no current reading, and none of these
              // figures may print its last one as "now".
              const uNow = latestValue(u[0]?.values ?? []);
              const uPeak = u[0] ? bandPeak(u[0]) : null;
              const lNows = l
                .map((b) => latestValue(b.values))
                .filter((v): v is number => v !== null);
              const lNow = lNows.length === 0 ? null : Math.max(...lNows);
              const lPeak =
                l.length === 0 ? null : Math.max(...l.map(bandPeak));
              const msFig = (v: number | null) =>
                v === null ? ABSENT : `${lat.spec.fmt?.(v) ?? v} ms`;
              return (
                <tr key={device}>
                  <th scope="row">
                    <span className="drivename">{device}</span>
                    {o.length > 0 && (
                      <Enlargeable
                        title={`${ops.spec.title}: ${device}`}
                        about={ops.spec.about}
                        unit={ops.spec.unit}
                        series={o}
                        mirrored
                        fmt={ops.spec.fmt}
                        window={window}
                        range={range}
                        ranges={RAIL_RANGES}
                        fetchSeries={widen(ops, device)}
                        label={`${device}: operations per second`}
                      >
                        <span className="opslink">operations/s</span>
                      </Enlargeable>
                    )}
                  </th>
                  <td className="drivecell">
                    <div ref={i === 0 ? ref : undefined}>
                      {cell(
                        `${io.spec.title}: ${device}`,
                        pair,
                        `${bytes(latestValue(pair[0]?.values ?? []))}/s · ${bytes(latestValue(pair[1]?.values ?? []))}/s`,
                        <Chart
                          series={pair}
                          width={width}
                          height={CELL_HEIGHT}
                          max={Math.max(ioPeak.up, ioPeak.down)}
                          mark="mirror"
                          mirrorPeak={ioPeak}
                          label={`${device} throughput over time`}
                          gapRule
                        />,
                        {
                          mirrored: true,
                          fmt: io.spec.fmt,
                          unit: io.spec.unit,
                          fetchSeries: widen(io, device),
                        },
                      )}
                    </div>
                  </td>
                  <td className="drivecell">
                    {cell(
                      `${util.spec.title}: ${device}`,
                      u,
                      `${percent(uNow)} · peak ${percent(uPeak)}`,
                      <Chart
                        series={u}
                        width={width}
                        height={CELL_HEIGHT}
                        min={0}
                        max={100}
                        mark="area"
                        label={`${device} utilisation over time`}
                        gapRule
                      />,
                      {
                        min: 0,
                        max: 100,
                        filled: true,
                        fmt: util.spec.fmt,
                        fetchSeries: widen(util, device),
                      },
                    )}
                  </td>
                  <td className="drivecell">
                    {cell(
                      `${lat.spec.title}: ${device}`,
                      l,
                      `${msFig(lNow)} · peak ${msFig(lPeak)}`,
                      <Chart
                        series={l}
                        width={width}
                        height={CELL_HEIGHT}
                        min={0}
                        max={latMax}
                        mark="line"
                        label={`${device} latency over time`}
                        gapRule
                      />,
                      {
                        min: 0,
                        max: latMax,
                        fmt: lat.spec.fmt,
                        unit: lat.spec.unit,
                        fetchSeries: widen(lat, device),
                      },
                    )}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </section>
  );
}
