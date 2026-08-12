// The Overview tab: two or three facts from each of the other tabs plus
// what needs attention. It is deliberately a summary -- every card here
// answers "is this worth opening the tab for?", and none of them
// reproduces the tab's own content.
import type { ReactNode } from "react";
import type {
  Container,
  HostDetail,
  MetricsResponse,
  Unit,
} from "../../../lib/api";
import {
  carriesColumn,
  griddedValues,
  optionalValues,
} from "../../../lib/metrics";
import {
  ABSENT,
  bitrate,
  bytes,
  duration,
  percent,
  relative,
} from "../../../lib/format";
import { Badge, type Severity } from "../../../ui/Badge";
import { Card } from "../../../ui/Card";
import { Meter } from "../../../ui/Meter";
import { memoryBands, perCoreBands } from "../../../lib/bands";
import { ChartPanel, type Band } from "../../../ui/charts/ChartPanel";
import { Sparkline } from "../../../ui/charts/Sparkline";
import { UpDownSparkline } from "../../../ui/charts/UpDownSparkline";

/**
 * column() in lib/metrics.ts THROWS for a column the answering tier does
 * not have, and that throw happens during render -- one absent column
 * would blank the whole tab. Every lookup on this page is therefore
 * optional by construction: a column that is not there yields no values,
 * and the card renders the absent marker instead of a fabricated number.
 *
 * The same guard exists in Graphs.tsx. It is duplicated rather than
 * extracted because lib/ is owned by another task; the two copies are
 * eight lines and identical on purpose.
 */
/** The most recent non-null reading, or null when there is none. A null
 * here means "the host reported nothing", which every caller renders as a
 * gap or as a word -- never as 0. */
function latest(
  res: MetricsResponse | null,
  base: string,
  seriesIndex = 0,
): number | null {
  const vals = optionalValues(res, seriesIndex, base);
  for (let i = vals.length - 1; i >= 0; i--) {
    const v = vals[i];
    if (v !== null) return v;
  }
  return null;
}

export interface FilesystemRow {
  label: string;
  total: number | null;
  used: number | null;
  free: number | null;
}

/**
 * One row per filesystem, in bytes as measured.
 *
 * No percentage is computed here, and that is a schema fact rather than a
 * layout preference: internal/hub/read/family.go records that `used` and
 * `free` do not sum to `total` (the gap is the root reserve, which is
 * neither in use nor allocatable), and that at the 5m/1h tiers a ratio
 * built from used_max and free_min composes two different instants and is
 * not the maximum of the true ratio. Absolute bytes are the only figure
 * that stays true at every tier.
 */
export function filesystemRows(res: MetricsResponse | null): FilesystemRow[] {
  // `== null`, not `=== null`: these props are optional, so a caller that
  // simply does not have this family yet passes undefined -- and reading
  // .series off it threw during render, with no error boundary under it.
  if (res == null) return [];
  return res.series.map((series, index) => ({
    label: series.key.filesystem ?? ABSENT,
    total: latest(res, "total", index),
    used: latest(res, "used", index),
    free: latest(res, "free", index),
  }));
}

/** The latest known reading, or null when the series has none. */
function lastNumber(values: readonly (number | null)[]): number | null {
  for (let i = values.length - 1; i >= 0; i--) {
    const v = values[i];
    if (v !== null && v !== undefined) return v;
  }
  return null;
}

/**
 * A keyed family summed across its series, index by index.
 *
 * A null anywhere in a bucket makes that bucket's total unknowable rather
 * than smaller, so the sum is null there too: counting it as zero draws a
 * dip that never happened.
 */
function sumInterfaces(
  res: MetricsResponse | null | undefined,
  base: string,
): (number | null)[] {
  if (res == null || res.series.length === 0) return [];
  const columns = res.series.map((_, i) => griddedValues(res, i, base));
  const width = columns.reduce((w, c) => Math.max(w, c.length), 0);
  const out: (number | null)[] = [];
  for (let i = 0; i < width; i++) {
    let total = 0;
    let known = false;
    let unknown = false;
    for (const column of columns) {
      const v = column[i];
      if (v === undefined) continue;
      if (v === null) unknown = true;
      else {
        total += v;
        known = true;
      }
    }
    out.push(unknown || !known ? null : total);
  }
  return out;
}

export interface Attention {
  severity: Severity;
  what: ReactNode;
}

// Series colours are token references, never literals -- the palette lives
// in index.css and a hue chosen in a .tsx file cannot follow the theme.
const CPU_BANDS: { base: string; name: string; color: string }[] = [
  { base: "cpu_user", name: "user", color: "var(--s1)" },
  { base: "cpu_system", name: "system", color: "var(--s2)" },
  { base: "cpu_iowait", name: "iowait", color: "var(--s3)" },
  { base: "cpu_steal", name: "steal", color: "var(--s4)" },
];

// A host that has not reported for longer than this is stale rather than
// merely late: the agent scrapes every 60s, so five missed scrapes is no
// longer explainable by jitter.
const STALE_AFTER_MS = 5 * 60 * 1000;

/**
 * What is wrong right now, worst first. Current state must not sit behind
 * a tab, so this is derived here from the same responses the cards above
 * it render -- there is no second source that could disagree with them.
 */
export function needsAttention(input: {
  host: HostDetail;
  agentMetrics: MetricsResponse | null;
  filesystems: FilesystemRow[];
  units: Unit[] | null;
  now?: Date;
}): Attention[] {
  const out: Attention[] = [];
  const now = input.now ?? new Date();

  const dropped = latest(input.agentMetrics, "buffer_dropped_total");
  if (dropped !== null && dropped > 0) {
    // The agent's ring buffer overflowed: samples that were collected were
    // never delivered. Nothing else netra reports is more important, and
    // no chart can show it, because the missing data is the evidence.
    out.push({
      severity: "critical",
      what: `${dropped} samples dropped before delivery — this host's history has holes`,
    });
  }

  const failures = latest(input.agentMetrics, "post_failures_total");
  if (failures !== null && failures > 0) {
    out.push({
      severity: "warning",
      what: `${failures} failed deliveries to the hub`,
    });
  }

  if (input.host.last_seen === null) {
    out.push({ severity: "serious", what: "never reported" });
  } else {
    const age = now.getTime() - new Date(input.host.last_seen).getTime();
    if (age > STALE_AFTER_MS) {
      out.push({
        severity: "serious",
        what: `last reported ${relative(input.host.last_seen, now)}`,
      });
    }
  }

  for (const fs of input.filesystems) {
    // df's Use%: used / (used + free). total is not the denominator -- see
    // filesystemRows above. Used only to decide whether to warn; the card
    // itself still shows bytes.
    if (fs.used === null || fs.free === null) continue;
    const capacity = fs.used + fs.free;
    if (capacity === 0) continue;
    const full = (fs.used / capacity) * 100;
    if (full >= 90) {
      out.push({
        severity: full >= 95 ? "critical" : "warning",
        what: `${fs.label} is ${percent(full)} full — ${bytes(fs.free)} free`,
      });
    }
  }

  const failed = (input.units ?? []).filter((u) => u.state === "failed");
  for (const unit of failed) {
    out.push({ severity: "warning", what: `${unit.unit_name} failed` });
  }

  return out;
}

/** A labelled landmark around a Card, so each summary is reachable by name
 * (Card itself renders a plain div and has no labelling of its own). */
function Panel({
  label,
  title,
  action,
  children,
}: {
  label: string;
  title: string;
  action?: ReactNode;
  children: ReactNode;
}) {
  return (
    <section aria-label={label}>
      <Card title={title} action={action}>
        {children}
      </Card>
    </section>
  );
}

function Facts({ rows }: { rows: [string, ReactNode][] }) {
  return (
    <dl className="kv">
      {rows.map(([key, value]) => (
        <div key={key} style={{ display: "contents" }}>
          <dt>{key}</dt>
          <dd>{value}</dd>
        </div>
      ))}
    </dl>
  );
}

export interface OverviewProps {
  host: HostDetail;
  hostMetrics: MetricsResponse | null;
  filesystemMetrics: MetricsResponse | null;
  agentMetrics: MetricsResponse | null;
  sensorMetrics: MetricsResponse | null;
  /** family=cpu_core for this host, one series per logical CPU. Absent on a
   * host too large to ask for them -- see MAX_PER_CORE in hostTrends.ts. */
  coreMetrics?: MetricsResponse | null;
  /** family=net for this host, one series per interface. */
  netMetrics?: MetricsResponse | null;
  containers: Container[] | null;
  units: Unit[] | null;
  /** Injected by tests so "last reported" is deterministic. */
  now?: Date;
}

export function Overview({
  host,
  hostMetrics,
  coreMetrics,
  netMetrics,
  filesystemMetrics,
  agentMetrics,
  sensorMetrics,
  containers,
  units,
  now,
}: OverviewProps) {
  const perState: Band[] = CPU_BANDS.map((band) => ({
    name: band.name,
    color: band.color,
    // On the window grid, so an outage is a hole in the silhouette rather
    // than a line drawn straight across it.
    values: griddedValues(hostMetrics, 0, band.base),
    // An all-null band is not a band, and in a STACK it is actively
    // destructive: stackBands() breaks every band at any index where any
    // series is null, because a running total is undefined there. A bare
    // metal host reports cpu_steal as NULL in every bucket -- correctly, it
    // has no hypervisor to steal from -- and that one empty series erased
    // the whole chart, legend still listing four states above a blank box.
  })).filter((band) => band.values.some((v) => v !== null));

  // The headline chart is the per-core stack, the same one the fleet row for
  // this host draws -- the two must not disagree about the same machine.
  // Falling back to cpu_total when there are no per-core series: one true
  // band beats a not-collected panel where a silhouette is available.
  //
  // The user/system/iowait/steal breakdown is NOT the fallback any more. It
  // answers a different question -- where the time went, rather than which
  // core spent it -- so it has its own panel below rather than standing in
  // for this one.
  const total = griddedValues(hostMetrics, 0, "cpu_total");
  const perCore = perCoreBands(coreMetrics ?? null);
  const cpuBands: Band[] =
    perCore.length > 0
      ? perCore
      : total.length > 0
        ? [{ name: "busy", color: "var(--s1)", values: total }]
        : [];

  const memBands = memoryBands(hostMetrics);

  // A host's traffic is the sum over its interfaces, and a null anywhere in
  // a bucket makes that bucket's total unknowable rather than smaller --
  // counting it as zero would draw a dip that never happened. Same rule the
  // fleet row uses, so the two agree about one host.
  const ingress = sumInterfaces(netMetrics, "rx_bytes");
  const egress = sumInterfaces(netMetrics, "tx_bytes");

  const memTotal = latest(hostMetrics, "mem_total") ?? host.memory_total;
  const memUsed = latest(hostMetrics, "mem_used") ?? host.mem_used;
  const swapTotal = latest(hostMetrics, "swap_total");
  const swapUsed = latest(hostMetrics, "swap_used");

  const filesystems = filesystemRows(filesystemMetrics);
  const attention = needsAttention({
    host,
    agentMetrics,
    filesystems,
    units,
    now,
  });

  const failedUnits = (units ?? []).filter((u) => u.state === "failed").length;
  const capabilities = Object.entries(host.capabilities);

  return (
    <div className="grid2">
      {/* Overlay (inside ChartPanel) renders the full legend itself once a
          panel carries two or more bands, so none is built here. */}
      <section aria-label="Processor">
        {/* No unit prop: percent() prints one already, and passing both
            rendered "12 % %". See ChartPanel's unit prop. */}
        <ChartPanel
          title="Processor"
          series={cpuBands}
          // No ceiling for the per-core stack: the bands are each core's real
          // utilisation, so the stack runs to cores x 100 and the height is a
          // shape rather than a quantity. The cpu_total fallback is a
          // percentage of the host and keeps the 0-100 axis.
          max={perCore.length > 0 ? undefined : 100}
          hideAxis={perCore.length > 0}
          fmt={(n) => percent(n)}
          window={hostMetrics?.window ?? null}
          // Each core contributes busy/N, so the stack's top edge is the mean
          // across cores -- cpu_total -- and 100 stays the right ceiling
          // however many cores the host has.
          //
          // Stacked for the cpu_total fallback too, even though one band is
          // not much of a stack: the mark must not change depending on
          // whether a host happened to be small enough to ask for its cores,
          // or two machines side by side would look like different metrics.
          stacked={cpuBands.length > 0}
          // No legend once the bands are cores: thirty-two entries are
          // longer than the chart, and the enlarged view's table already
          // names every core beside its colour. This used to suppress the
          // legend by passing `highlight`, which ALSO dims every other series
          // to 35% -- the whole stack went pale to hide a list.
          legend={perCore.length <= 6}
          // An empty band list is a tier that does not carry the columns, and
          // an empty chart asserts the host reported nothing. Say which it is.
          unavailable={
            cpuBands.length === 0
              ? "The host reported no processor samples in this window."
              : undefined
          }
        />
      </section>

      {/* The breakdown keeps its own panel rather than being displaced by the
          per-core stack: "which core" and "doing what" are different
          questions and a reader wants both. It used to vanish above an hour
          because cpu_user/system/iowait/steal lived only in the raw table;
          they reach the 5m and 1h rollups now, so this survives the range
          control. */}
      <ChartPanel
        title="CPU time breakdown"
        series={perState}
        max={100}
        fmt={(n) => percent(n)}
        stacked
        window={hostMetrics?.window ?? null}
        unavailable={
          perState.length === 0
            ? "cpu_user, cpu_system, cpu_iowait and cpu_steal are not stored at this resolution."
            : undefined
        }
      />

      {/* The stack and the meter answer different questions and both stay:
          the meter says how full the host is right now, the chart says how it
          got there. The ceiling is mem_total rather than the stack's own
          running total, so the gap at the top is free memory -- a stack
          scaled to itself always touches the top and would report every host
          as full. */}
      <ChartPanel
        title="Memory"
        series={memBands}
        // Headroom above total so the rule marking it reads as a rule rather
        // than as the top border of the plot.
        max={memTotal === null ? undefined : memTotal * 1.08}
        reference={memTotal ?? undefined}
        referenceLabel={memTotal === null ? undefined : bytes(memTotal)}
        fmt={(n) => bytes(n)}
        stacked
        window={hostMetrics?.window ?? null}
        unavailable={
          memBands.length === 0
            ? "The host reported no memory samples in this window."
            : memTotal === null
              ? "The host's total memory is unknown, so there is no ceiling to draw the bands against."
              : undefined
        }
      />

      <Panel label="Memory" title="Memory">
        <Meter
          label="used"
          value={memUsed}
          max={memTotal}
          formatValue={(value, max) => `${bytes(value)} of ${bytes(max)}`}
        />
        {/* Three states, not two. swap_total lives only in the raw table
            (0001_init.sql) -- the 5m and 1h rollups do not carry it -- so at
            any range above an hour the value is missing because the TIER
            has no such column, not because the host has no swap. Collapsing
            those told a host with 8 GB of swap in use that it had none: an
            absent column rendered as a positive fact about the machine. */}
        {!carriesColumn(hostMetrics, "swap_total") ? (
          <div className="mrow">
            <div>
              <div className="lab">swap</div>
            </div>
            <div className="val">not at this resolution</div>
          </div>
        ) : swapTotal === null ? (
          // Meter's absent state renders the em-dash marker and its
          // noLimit state says "no limit" -- the container-limit wording.
          // Neither is the fact here, which is that this host has no swap
          // configured at all, so the row is written out in Meter's own
          // markup with the one word that is true.
          <div className="mrow">
            <div>
              <div className="lab">swap</div>
            </div>
            <div className="val">none</div>
          </div>
        ) : (
          <Meter
            label="swap"
            value={swapUsed}
            max={swapTotal}
            formatValue={(value, max) => `${bytes(value)} of ${bytes(max)}`}
          />
        )}
      </Panel>

      {/* Ingress above the line, egress below -- the same mark the fleet row
          draws, because a reader moving between them should not have to
          re-learn the chart. There was no traffic card on this page at all:
          the only network chart lived in the Graphs tab, so the overview
          summarised every subsystem except the one most likely to explain a
          problem. */}
      <Panel label="Traffic" title="Traffic">
        {ingress.length === 0 && egress.length === 0 ? (
          <p className="note">No interface samples in this window.</p>
        ) : (
          <div className="traffic-cell">
            <UpDownSparkline
              up={ingress}
              down={egress}
              width={260}
              height={64}
              label="Ingress and egress over time"
            />
            <div className="traffic-rates">
              <span className="rate">↑ {bitrate(lastNumber(ingress))} in</span>
              <span className="rate">↓ {bitrate(lastNumber(egress))} out</span>
            </div>
          </div>
        )}
      </Panel>

      <Panel label="Disk" title="Disk">
        {filesystems.length === 0 ? (
          <p className="note">No filesystem samples in this window.</p>
        ) : (
          <div className="fs-list">
            {filesystems.map((fs) => (
              <Meter
                key={fs.label}
                label={fs.label}
                // df's Use%: used / (used + free), never used / total. total
                // includes the root reserve, which is neither in use nor
                // allocatable, so dividing by it reports a full disk as less
                // full than df does -- and df's number is the one the
                // operator has already seen over SSH. Same definition the
                // fleet's disk column uses, so the two cannot disagree about
                // one filesystem.
                value={fs.used}
                max={
                  fs.used === null || fs.free === null
                    ? null
                    : fs.used + fs.free
                }
                formatValue={() =>
                  `${bytes(fs.used)} used · ${bytes(fs.free)} free · ${bytes(fs.total)} size`
                }
              />
            ))}
            <p className="note">
              Bytes as measured: used and free do not sum to size, and the
              difference is the root reserve.
            </p>
          </div>
        )}
      </Panel>

      <Panel label="Needs attention" title="Needs attention">
        {attention.length === 0 ? (
          <p className="allclear">All clear</p>
        ) : (
          <ul className="ai-list">
            {attention.map((a, index) => (
              <li key={index}>
                <Badge severity={a.severity}>{a.severity}</Badge> {a.what}
              </li>
            ))}
          </ul>
        )}
      </Panel>

      <Panel label="System" title="System">
        <Facts
          rows={[
            ["OS", host.os_name ?? ABSENT],
            ["Kernel", host.kernel ?? ABSENT],
            ["Architecture", host.arch ?? ABSENT],
            ["Processor", host.cpu_model ?? ABSENT],
            [
              "Cores",
              host.cores === null
                ? ABSENT
                : `${host.cores} cores · ${host.threads ?? ABSENT} threads`,
            ],
            [
              "Uptime",
              duration(latest(hostMetrics, "uptime_s") ?? host.uptime_s),
            ],
            ["Agent", host.agent_version ?? ABSENT],
          ]}
        />
      </Panel>

      <Panel label="Inventory" title="Inventory">
        <Facts
          rows={[
            [
              "Containers",
              containers === null ? ABSENT : `${containers.length} containers`,
            ],
            [
              "Units",
              units === null
                ? ABSENT
                : `${units.length} units · ${failedUnits} failed`,
            ],
            [
              "Filesystems",
              filesystems.length === 0
                ? ABSENT
                : `${filesystems.length} mounted`,
            ],
          ]}
        />
      </Panel>

      <Panel label="Temperature" title="Temperature">
        {sensorMetrics === null || sensorMetrics.series.length === 0 ? (
          <p className="note">No sensor readings in this window.</p>
        ) : (
          // A temperature is only interesting as a movement. One number says
          // 48 °C, which a reader cannot judge without knowing whether it has
          // been 48 all day or climbing for an hour -- so every sensor gets
          // its recent history beside its reading.
          <div className="sensor-list">
            {sensorMetrics.series.map((series, index) => {
              const name =
                [series.key.chip, series.key.label].filter(Boolean).join(" ") ||
                ABSENT;
              const value = latest(sensorMetrics, "temp", index);
              const history = griddedValues(sensorMetrics, index, "temp");
              return (
                <div className="sensor-row" key={`${name}-${index}`}>
                  <span className="lab">{name}</span>
                  {/* Free-scaled to its own extent, deliberately: these sit
                      in one list but a CPU package and an NVMe drive do not
                      share a sensible axis, and a shared one would flatten
                      every sensor against the hottest. The question here is
                      "is this one moving", not "which is hottest". */}
                  <Sparkline
                    values={history}
                    width={110}
                    height={24}
                    color="var(--s7)"
                    label={`${name} temperature trend`}
                  />
                  <span className="val">
                    {value === null ? ABSENT : `${Math.round(value)} °C`}
                  </span>
                </div>
              );
            })}
          </div>
        )}
      </Panel>

      <Panel label="Collectors" title="Collectors">
        {capabilities.length === 0 ? (
          <p className="note">The agent reported no capabilities.</p>
        ) : (
          <Facts
            rows={capabilities.map(([name, state]) => [
              name,
              // The reason is the value the agent sent; a collector that
              // cannot run says why rather than showing an empty chart.
              state === "ok" ? (
                <Badge severity="ok">ok</Badge>
              ) : (
                <span>{state}</span>
              ),
            ])}
          />
        )}
      </Panel>
    </div>
  );
}
