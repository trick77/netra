import { Overlay } from "../../ui/charts/Overlay";
import type { HostRow } from "./hostColumns";

/**
 * One host's two overlay silhouettes. This is deliberately NOT `HostRow`:
 * the row carries stacked bands (user/system/iowait/steal), and an overlay
 * of every host's four bands would be forty lines nobody can read. The
 * overlay asks a different question -- who is behaving differently -- so it
 * takes one line per host per metric. `fromHostRows` below does the
 * collapsing.
 *
 * `memTotal` is the host's own ceiling, kept alongside the bytes rather than
 * pre-divided, so this type stays the raw thing and the percentage stays a
 * rendering decision (see the memory chart below for why it must be one).
 */
export interface OverlayHost {
  id: number;
  hostname: string;
  cpu: (number | null)[];
  mem: (number | null)[];
  memTotal: number | null;
}

// A running total is undefined at any index where ANY band is null -- the
// same rule geometry.ts's stackBands() follows, so the overlay and the
// row's stacked sparkline agree about where a host's history has a hole
// instead of one of them drawing straight through it.
function sumBands(bands: { values: (number | null)[] }[]): (number | null)[] {
  const n = bands.reduce((longest, b) => Math.max(longest, b.values.length), 0);
  const out: (number | null)[] = [];
  for (let i = 0; i < n; i++) {
    if (bands.some((b) => b.values[i] == null)) {
      out.push(null);
      continue;
    }
    out.push(bands.reduce((sum, b) => sum + (b.values[i] as number), 0));
  }
  return out;
}

/** Collapses the fleet list's stacked bands back into one line per host. */
export function fromHostRows(rows: readonly HostRow[]): OverlayHost[] {
  return rows.map((row) => ({
    id: row.id,
    hostname: row.hostname,
    cpu: sumBands(row.cpu),
    mem: sumBands(row.mem),
    memTotal: row.mem_total,
  }));
}

function mean(values: (number | null)[]): number | null {
  const numeric = values.filter((v): v is number => v !== null);
  if (numeric.length === 0) return null;
  return numeric.reduce((a, b) => a + b, 0) / numeric.length;
}

function median(values: number[]): number {
  const sorted = [...values].sort((a, b) => a - b);
  const mid = Math.floor(sorted.length / 2);
  return sorted.length % 2 === 1
    ? sorted[mid]!
    : (sorted[mid - 1]! + sorted[mid]!) / 2;
}

/**
 * The host furthest from the fleet's middle, by its own mean over the
 * window. The median (not the mean) is the reference on purpose: with one
 * host pegged at 95% and the rest idling, a mean reference is dragged
 * towards the very host being measured against it and the pack starts
 * looking like the anomaly.
 *
 * A host with no readings at all scores nothing rather than scoring zero --
 * silence is not idleness, and calling the one silent host the outlier
 * would hide whichever host is actually misbehaving.
 *
 * Returns null below two hosts: "differently from whom" has no answer with
 * one line on the chart.
 */
export function outlier(
  hosts: readonly OverlayHost[],
  pick: (host: OverlayHost) => (number | null)[],
): string | null {
  const scored = hosts
    .map((host) => ({ hostname: host.hostname, avg: mean(pick(host)) }))
    .filter((s): s is { hostname: string; avg: number } => s.avg !== null);
  if (scored.length < 2) return null;
  const middle = median(scored.map((s) => s.avg));
  return scored.reduce((worst, s) =>
    Math.abs(s.avg - middle) > Math.abs(worst.avg - middle) ? s : worst,
  ).hostname;
}

// Every host draws in the same series colour: with nineteen lines a per-host
// hue is nineteen colours nobody can hold in their head, and the chart's
// question ("who is shaped differently") is answered by position, not by
// identity. The outlier takes a second colour AND a caption naming it --
// colour alone never carries meaning here.
const PACK_COLOR = "var(--s1)";
const OUTLIER_COLOR = "var(--s2)";

function toSeries(
  hosts: readonly OverlayHost[],
  pick: (host: OverlayHost) => (number | null)[],
  highlight: string | null,
) {
  return hosts.map((host) => ({
    name: host.hostname,
    color: host.hostname === highlight ? OUTLIER_COLOR : PACK_COLOR,
    values: pick(host),
  }));
}

function hasReading(values: (number | null)[]): boolean {
  return values.some((v) => v !== null);
}

export interface AllHostsOverlayProps {
  hosts: readonly OverlayHost[];
  width?: number;
  height?: number;
}

/**
 * The fleet's CPU and memory on two shared axes (spec 4.4). The list's
 * columns rank who is highest *now*; this ranks nobody and shows who is
 * behaving *differently* over the window -- which is why every host is
 * de-emphasised and only the outlier is named.
 */
export function AllHostsOverlay({
  hosts,
  width = 520,
  height = 120,
}: AllHostsOverlayProps) {
  const cpuHosts = hosts.filter((h) => hasReading(h.cpu));
  // A host with no known ceiling cannot be expressed as a percentage of it,
  // and plotting its raw bytes on a percentage axis would put it somewhere
  // arbitrary. It is dropped from this chart and the drop is stated below --
  // silent omission from a fleet-wide chart reads as completeness.
  const memHosts = hosts.filter(
    (h) => hasReading(h.mem) && h.memTotal !== null,
  );
  const memExcluded = hosts.length - memHosts.length;

  if (cpuHosts.length === 0 && memHosts.length === 0) return null;

  const memPercent = (host: OverlayHost) =>
    host.mem.map((v) => (v === null ? null : (v / host.memTotal!) * 100));

  const cpuOutlier = outlier(cpuHosts, (h) => h.cpu);
  // Picked on percent, not bytes -- the same scale the chart draws. On raw
  // bytes a roomy 512 GB host at 39% outranks a 16 GB host at 94%, and the
  // caption would name a line sitting squarely in the pack.
  const memOutlier = outlier(memHosts, memPercent);

  return (
    <div className="grid2 fleet-overlay">
      <section className="smp">
        <div className="t">
          <h4>CPU, all hosts</h4>
          <span className="now">{caption(cpuOutlier, cpuHosts.length)}</span>
        </div>
        <div className="chartwrap">
          <Overlay
            series={toSeries(cpuHosts, (h) => h.cpu, cpuOutlier)}
            max={100}
            width={width}
            height={height}
            highlight={cpuOutlier ?? undefined}
            label="CPU busy, all hosts"
          />
        </div>
      </section>
      <section className="smp">
        <div className="t">
          <h4>Memory, all hosts</h4>
          <span className="now">{caption(memOutlier, memHosts.length)}</span>
        </div>
        <div className="chartwrap">
          <Overlay
            series={toSeries(memHosts, memPercent, memOutlier)}
            max={100}
            width={width}
            height={height}
            highlight={memOutlier ?? undefined}
            label="Memory used, all hosts"
          />
        </div>
        <p className="note">
          % of each host&apos;s own total, so a 512 GB host and a 16 GB one
          compare.
          {memExcluded > 0
            ? ` ${memExcluded} host${memExcluded === 1 ? "" : "s"} omitted: no reading, or no known total.`
            : ""}
        </p>
      </section>
    </div>
  );
}

function caption(name: string | null, hostCount: number): string {
  if (name === null) return `${hostCount} host${hostCount === 1 ? "" : "s"}`;
  return `${name} stands out`;
}
